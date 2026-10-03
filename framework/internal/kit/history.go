// Package kit — history: the former values of the fields that keep them.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// Field history (ADR 0007 §1). A field tagged history=N keeps its N former
// values, newest first, each with when it was replaced and by whom — the
// caller's kit.UserID, nobody for a write without one. A value is recorded
// only when its field changes, so a login counter written at every attempt
// never pushes a former password out; a zero value is no former value, so a
// field set for the first time records nothing.
//
// The former values live beside the store, one document per record, in
// <service>/<store>.history.json, where the store keeps its own data — on a
// database, in the table <service>__<store>__history, written in the
// record's transaction. Every write of the store goes through historied,
// which writes the record's history inside the record's own write — the
// engine's Update, under the store's writers' lock —, before the record:
//
//   - a write that fails after its history — a crash — leaves the record's
//     value at the head of its history, which kit reads past and the next
//     change replaces: a duplicate, never a change unrecorded;
//   - a write the store refuses — a unique index, a full disk — puts the
//     history back as it was;
//   - a deleted record takes its history with it; a history whose record is
//     gone is removed at the next start, as a workflow reconciles its
//     journal with its store.
//
// A held record's former values are not pruned while it is held: its first
// write after the release prunes them. An erasure takes the former values
// of the members it clears with them (ADR 0006): an erased value is no one's
// former value.
//
// A former value is sealed at rest as its field is sealed — or the field it
// sits in —, under the data key of the record it was part of, bound to the
// store, the record, the field and its being a former value (ADR 0006 §4,
// ADR 0007 §4): toHistory and fromHistory (history_write.go) are the one
// place a value enters the history and leaves it. A former value whose key
// is destroyed is erased: it is read as gone.

// historyRecord is what kit keeps of one record's past: each field's former
// values, newest first, by the field's JSON pointer.
type historyRecord struct {
	Key    string                   `json:"key"`
	Fields map[string][]formerEntry `json:"fields,omitempty"`
}

// key is the record's key in kit's store.
func (h historyRecord) key() string { return h.Key }

// formerEntry is one former value, as the history keeps it.
type formerEntry struct {
	// Value is the field's value as JSON, whatever its class: a secret's is
	// read by kit alone. At rest, a sealed field's is a box.
	Value json.RawMessage `json:"value" kit:"personal"`
	// Until is when it was replaced, on the app's clock; By who replaced it.
	Until time.Time `json:"until"`
	By    string    `json:"by,omitempty"`
	// sealed says, once read, that the value rests sealed.
	sealed bool
}

// historied is a store's engine with what kit adds to its writes: the former
// values of the fields that keep them, and the check of its password
// policies (passwords.go). Reads pass through to the engine.
type historied[T any] struct {
	storeEngine[T]
	s *StoreService[T]
	// hist keeps the records' histories; nil when no field keeps one — a
	// password policy that refuses no former password.
	hist storeEngine[historyRecord]
	// keeps is how many former values each field keeps, by JSON pointer.
	keeps map[string]int
	// seal is the store's sealing engine, nil when it seals nothing: former
	// values are sealed as their fields are.
	seal *sealedEngine[T]
}

// keeps is how many former values each field of the store keeps, by JSON
// pointer: its history=N, or the former passwords its policy refuses — the
// larger (a smaller tag is refused at the declaration).
func (s *StoreService[T]) keeps() map[string]int {
	out := s.plan().historyFields()
	for _, p := range s.passwords {
		if p.member != nil && p.notReused > out[p.member.pointer] {
			out[p.member.pointer] = p.notReused
		}
	}
	return out
}

// historyFields are the members that keep their former values, with how
// many: a field tagged history=N, reached without crossing a list or a map.
func (p *classPlan) historyFields() map[string]int {
	out := map[string]int{}
	for _, m := range p.members {
		if m.tag.history > 0 && !m.inCollection() {
			out[m.pointer] = m.tag.history
		}
	}
	return out
}

// historyFile is where the store keeps its records' histories, beside its
// own snapshot.
func (s *StoreService[T]) historyFile() string { return s.svc.name + "/" + s.name + ".history.json" }

// historyOn is where a store keeps its records' former values: beside its
// files — in memory for a store in memory —, or on the database that keeps
// it.
type historyOn struct {
	files storeFS
	sql   *databaseRun
}

// withHistory is the engine the store runs on: eng itself when the store
// remembers nothing and has no password policy, else eng wrapped by
// historied, its history opened where the store keeps its data — in memory
// for a store in memory, and reconciled with its records; on its database,
// in a table the record's transaction writes, where nothing is left to
// reconcile.
//
// IFACE-PLUGIN: the store runs on any engine — documents, history, SQL — each
// behind this port.
func (s *StoreService[T]) withHistory(ctx context.Context, eng storeEngine[T], where historyOn) (storeEngine[T], error) {
	keeps := s.keeps()
	if len(keeps) == 0 && len(s.passwords) == 0 {
		return eng, nil
	}
	h := &historied[T]{storeEngine: eng, s: s, keeps: keeps}
	h.seal, _ = eng.(*sealedEngine[T])
	if len(keeps) == 0 {
		return h, nil
	}
	if r := where.sql; r != nil {
		ds, err := docstore.OpenSQL(docstore.SQLConfig[historyRecord]{
			Key: historyRecord.key, Transactor: r.transactor(),
			Dialect: r.d.dialect(), Table: s.historyTable(),
		})
		if err != nil {
			return nil, failure(CodeHistoryLoad, "HISTORY_LOAD", "the store's history cannot be opened on its database", err,
				errs.String("store", s.id), errs.String("database", r.d.name))
		}
		h.hist = &sqlEngine[historyRecord]{ds: ds, key: historyRecord.key, run: r, a: s.app(), store: s.id}
		return h, nil
	}
	files := where.files
	if files.fs != nil {
		files.path = s.historyFile()
	}
	hist, err := openDocEngine(historyRecord.key, files, nil, versionsOn{})
	if err == nil {
		hist.name = s.id + " history"
		h.hist = hist
		if err = h.reconcile(ctx); err != nil {
			err = errors.Join(err, hist.Close())
		}
	}
	if err != nil {
		return nil, failure(CodeHistoryLoad, "HISTORY_LOAD", "the store's history cannot be opened", err,
			errs.String("store", s.id), errs.String("file", files.path))
	}
	return h, nil
}

// reconcile removes, at start, the histories whose record is gone — a crash
// between a record's deletion and its history's — and those that keep
// nothing.
func (h *historied[T]) reconcile(ctx context.Context) error {
	docs, err := h.hist.List(ctx)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if len(doc.Fields) > 0 && h.recordExists(ctx, doc.Key) {
			continue
		}
		if err := h.hist.Delete(ctx, doc.Key); err != nil && !errors.Is(err, docstore.WriteUnconfirmed) {
			return err
		}
	}
	return nil
}

// recordExists reports whether the store holds a record under key; one it
// cannot decode is there.
func (h *historied[T]) recordExists(ctx context.Context, key string) bool {
	_, err := h.Get(ctx, key)
	return !errors.Is(err, docstore.DocumentNotFound)
}

// Close closes the store's engine, then its history's.
func (h *historied[T]) Close() error {
	err := h.storeEngine.Close()
	if h.hist != nil {
		err = errors.Join(err, h.hist.Close())
	}
	return err
}

// Fold folds the files of the store and of its history: what an erasure
// overwrote leaves both.
func (h *historied[T]) Fold(ctx context.Context) error {
	var failed []error
	for _, e := range []any{h.storeEngine, h.hist} {
		if f, ok := e.(folder); ok {
			failed = append(failed, f.Fold(ctx))
		}
	}
	return errors.Join(failed...)
}

// failed is a refusal of the history's engine as the store's caller reads
// it: kit's failure, its cause for the logs only — Store.said words the
// record's own refusals, and must never take the history's for them.
func (h *historied[T]) failed(code errs.Code, err error) error {
	if errors.Is(err, docstore.StoreClosed) {
		return notRunning(&h.s.nodeBase)
	}
	public := "the store's history could not be written"
	if code == CodeHistoryRead {
		public = "the store's history could not be read"
	}
	return explain(code, "HISTORY", public, err, errs.String("store", h.s.id))
}

// warn says, on kit's log, what went wrong with a history kit could not put
// back: its record's write was refused all the same.
func (h *historied[T]) warn(ctx context.Context, err error) {
	if a := h.s.app(); a != nil {
		logger.Warn(ctx, a.log, "a store's history could not be put back after a refused write",
			logger.String("store", h.s.id), logger.String("error", err.Error()))
	}
}
