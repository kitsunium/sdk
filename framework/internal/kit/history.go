package kit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
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
type historied struct {
	plug.StoreEngine
	s *storeCore
	// hist keeps the records' histories; nil when no field keeps one — a
	// password policy that refuses no former password.
	hist plug.StoreEngine
	// keeps is how many former values each field keeps, by JSON pointer.
	keeps map[string]int
	// seal is the store's sealing engine, nil when it seals nothing: former
	// values are sealed as their fields are.
	seal *sealedEngine
}

// keeps is how many former values each field of the store keeps, by JSON
// pointer: its history=N, or the former passwords its policy refuses — the
// larger (a smaller tag is refused at the declaration).
func (s *storeCore) keeps() map[string]int {
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
func (s *storeCore) historyFile() string { return s.svc.name + "/" + s.name + ".history.json" }

// historyOn is where a store keeps its records' former values: beside its
// files — in memory for a store in memory —, or on the database that keeps
// it.
type historyOn struct {
	files storeFS
	sql   *databaseRun
}

// historyConfig is the engine of a store's history: its records, keyed by
// the record they remember.
var historyConfig = &engineConfig{
	key: func(d plug.DocumentValue) string {
		rec, _ := d.Value.(historyRecord)
		return rec.Key
	},
	decode: func(raw json.RawMessage) (any, error) {
		var rec historyRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, undecodable("history", err)
		}
		return rec, nil
	},
}

// historyOf is the history d holds.
func historyOf(d plug.DocumentValue) (historyRecord, error) {
	if rec, ok := d.Value.(historyRecord); ok {
		return rec, nil
	}
	v, err := historyConfig.decode(d.JSON)
	if err != nil {
		return historyRecord{}, err
	}
	rec, _ := v.(historyRecord)
	return rec, nil
}

// historyDoc is rec as the history's engine keeps it.
func historyDoc(rec historyRecord) (plug.DocumentValue, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return plug.DocumentValue{}, unencodable("history", err)
	}
	return plug.DocumentValue{JSON: raw, Value: rec}, nil
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
func (s *storeCore) withHistory(ctx context.Context, eng plug.StoreEngine, where historyOn) (plug.StoreEngine, error) {
	keeps := s.keeps()
	if len(keeps) == 0 && len(s.passwords) == 0 {
		return eng, nil
	}
	h := &historied{StoreEngine: eng, s: s, keeps: keeps}
	h.seal, _ = eng.(*sealedEngine)
	if len(keeps) == 0 {
		return h, nil
	}
	if r := where.sql; r != nil {
		hist, err := s.historyOnSQL(r)
		if err != nil {
			return nil, err
		}
		h.hist = hist
		return h, nil
	}
	files := where.files
	if files.fs != nil {
		files.path = s.historyFile()
	}
	hist, err := openDocEngine(historyConfig, files, versionsOn{})
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

// historyOnSQL opens the store's history in its table on r.
func (s *storeCore) historyOnSQL(r *databaseRun) (*sqlEngine, error) {
	hist, err := openSQLEngine(s.app(), sqlTable{run: r, table: s.historyTable(), store: s.id}, historyConfig)
	if err != nil {
		return nil, failure(CodeHistoryLoad, "HISTORY_LOAD", "the store's history cannot be opened on its database", err,
			errs.String("store", s.id), errs.String("database", r.d.name))
	}
	return hist, nil
}

// reconcile removes, at start, the histories whose record is gone — a crash
// between a record's deletion and its history's — and those that keep
// nothing.
func (h *historied) reconcile(ctx context.Context) error {
	docs, err := h.hist.List(ctx)
	if err != nil {
		return err
	}
	for _, d := range docs {
		doc, err := historyOf(d)
		if err != nil {
			return err
		}
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
// cannot decode is there, and so is one in the trash (trash.go), whose
// former values its restoration brings back.
func (h *historied) recordExists(ctx context.Context, key string) bool {
	_, err := h.Get(ctx, key)
	if !errors.Is(err, docstore.DocumentNotFound) {
		return true
	}
	vk, ok := h.StoreEngine.(plug.VersionKeeper)
	if !ok {
		return false
	}
	_, err = vk.StoredVersions(ctx, key)
	return err == nil
}

// Close closes the store's engine, then its history's.
func (h *historied) Close() error {
	err := h.StoreEngine.Close()
	if h.hist != nil {
		err = errors.Join(err, h.hist.Close())
	}
	return err
}

// Fold folds the files of the store and of its history: what an erasure
// overwrote leaves both.
func (h *historied) Fold(ctx context.Context) error {
	var failed []error
	for _, e := range []any{h.StoreEngine, h.hist} {
		if f, ok := e.(plug.Folder); ok {
			failed = append(failed, f.Fold(ctx))
		}
	}
	return errors.Join(failed...)
}

// failed is a refusal of the history's engine as the store's caller reads
// it: kit's failure, its cause for the logs only — Store.said words the
// record's own refusals, and must never take the history's for them.
func (h *historied) failed(code errs.Code, err error) error {
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
func (h *historied) warn(ctx context.Context, err error) {
	if a := h.s.app(); a != nil {
		logger.Warn(ctx, a.log, "a store's history could not be put back after a refused write",
			logger.String("store", h.s.id), logger.String("error", err.Error()))
	}
}
