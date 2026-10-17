package kit

import (
	"context"
	"fmt"
	"iter"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The keys of a store's records (ADR 0178 C6). A module that watches a store
// may list its keys — to catch up on the writes a crash took the notices of —,
// and nothing else may: the mark that decides what a module hears decides what
// it may list. A listing reads the keys alone, never a record: the SDK's
// document store lists them without copying a document (docstore.KeyLister),
// and each listing is one entry in the privacy journal, however many keys it
// yields, since a key can be personal data — an e-mail as a key.

// sqlKeysChunk is how many keys one call of a database's engine lists, each
// under its own <name>-timeout: a long listing is many short calls, never one
// call the timeout ends halfway.
const sqlKeysChunk int = 256

// engineKeys are the keys eng lists above after, or its refusal: an engine
// that lists none says so once.
func engineKeys(ctx context.Context, eng plug.StoreEngine, after string) iter.Seq2[string, error] {
	if k, ok := eng.(plug.Keyser); ok {
		return k.Keys(ctx, after)
	}
	return func(yield func(string, error) bool) {
		yieldKeys(yield, nil, Unavailable("this store's engine cannot list its keys"))
	}
}

// yieldKeys yields keys in order, then failure when there is one, and reports
// whether the caller wants more: false once it stopped, and after a failure,
// which is a listing's last yield, whatever the caller answered to it.
func yieldKeys(yield func(string, error) bool, keys []string, failure error) bool {
	for _, key := range keys {
		if !yield(key, nil) {
			return false
		}
	}
	if failure == nil {
		return true
	}
	return yield("", failure) && failure == nil
}

// Keys lists the document store's keys above after: in memory, without a
// document copied.
//
//ktn:allow-unused-param: the keyser port passes a context the local document store does not take
func (e *docEngine) Keys(_ context.Context, after string) iter.Seq2[string, error] {
	return e.ds.Keys(after)
}

// Keys lists the table's keys above after, sqlKeysChunk at a time, each
// chunk one call on the transaction ctx carries for the database, else on
// the pool, within the database's timeout.
func (e *sqlEngine) Keys(ctx context.Context, after string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for {
			chunk, err := e.keysChunk(ctx, after)
			if !yieldKeys(yield, chunk, err) || len(chunk) < sqlKeysChunk {
				return
			}
			after = chunk[len(chunk)-1]
		}
	}
}

// keysChunk is at most sqlKeysChunk keys above after, read in one call.
func (e *sqlEngine) keysChunk(ctx context.Context, after string) ([]string, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	keys := make([]string, 0, sqlKeysChunk)
	for key, err := range e.ds.Keys(c, after) {
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
		if len(keys) == sqlKeysChunk {
			break
		}
	}
	return keys, nil
}

// Keys lists the keys of the records the sealing engine keeps: its inner
// engine's, which keys a record by its clear members.
func (e *sealedEngine) Keys(ctx context.Context, after string) iter.Seq2[string, error] {
	return engineKeys(ctx, e.inner, after)
}

// Keys lists the keys of the engine under the store's history.
func (h *historied) Keys(ctx context.Context, after string) iter.Seq2[string, error] {
	return engineKeys(ctx, h.StoreEngine, after)
}

// keys are every key the store holds, in key order, its engine's refusals
// worded as the store words them.
func (s *storeCore) keys(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		eng := s.engine()
		if eng == nil {
			yieldKeys(yield, nil, notRunning(&s.nodeBase))
			return
		}
		for key, err := range engineKeys(ctx, eng, "") {
			if err != nil {
				yieldKeys(yield, nil, s.said(err, "", ""))
				return
			}
			if !yield(key, nil) {
				return
			}
		}
	}
}

// recordKeys lists the store's keys for a module that watches it: refused to
// any other caller, inside the listing's span, and journaled once.
func (s *storeCore) recordKeys(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		a := s.app()
		if a == nil {
			yieldKeys(yield, nil, notRunning(&s.nodeBase))
			return
		}
		if err := s.listedBy(ctx, a); err != nil {
			yieldKeys(yield, nil, err)
			return
		}
		ctx, sp := a.beginPrivacy(ctx, s.id, model.EdgeReads, "", "Keys")
		err := a.journal(ctx, &journalLine{op: model.JournalList, store: s.id})
		if err == nil {
			err = s.keysTo(ctx, yield)
		}
		sp.end(err)
		if err != nil {
			yieldKeys(yield, nil, err)
		}
	}
}

// keysTo yields the store's keys until the caller stops, and returns the
// failure that ended the listing, unyielded; nil once every key went, or the
// caller stopped.
func (s *storeCore) keysTo(ctx context.Context, yield func(string, error) bool) error {
	for key, err := range s.keys(ctx) {
		if err != nil {
			return err
		}
		if !yield(key, nil) {
			return nil
		}
	}
	return nil
}

// listedBy refuses the listing of the store's keys to a caller none of whose
// module's watches the store feeds: the node ctx runs inside names the
// caller, and code that runs inside none is refused too.
func (s *storeCore) listedBy(ctx context.Context, a *App) error {
	caller := currentNode(ctx)
	if n := a.findNode(caller); n != nil && n.base().svc != nil && a.watchedBy(n.base().svc.module, s.id) {
		return nil
	}
	cause := failure(CodeRecordsKeys, "RECORDS_KEYS_REFUSED", "only a module that watches a store lists its keys", nil,
		errs.String("store", s.id), errs.String("caller", caller))
	return Forbidden(fmt.Sprintf("%s lists its keys only to a module one of whose watches it feeds", s.id)).Wrap(cause)
}

// watchedBy reports whether a watch of module — the product's when nil —
// hears store.
func (a *App) watchedBy(module *Module, store string) bool {
	for _, svc := range a.services {
		if svc == nil || svc.module != module {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			w, ok := n.(*Watch)
			if !ok {
				continue
			}
			for _, st := range w.feeders(a) {
				if st.base().id == store {
					return true
				}
			}
		}
	}
	return false
}
