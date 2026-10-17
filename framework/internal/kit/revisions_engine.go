package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Where a store's versions are kept (ADR 0007 §3): the SDK's document store
// keeps them with each record, in the record's own write, and prunes them in
// that write unless a legal hold keeps the record. Each engine opens its
// document store with them, stamps each write with what its caller says —
// who, which command, whether it makes no version —, and hands its versions
// on as they rest (plug.VersionKeeper); the engines that wrap one — sealing,
// history — hand them on too.

// versionsOn is how a store's engine keeps its records' versions: how many
// former versions, the clock that stamps them, and what keeps them from
// pruning. A zero keep keeps none: the engine writes as it always did.
type versionsOn struct {
	keep  int
	clock clock.Clock
	held  func(ctx context.Context, key string) bool
}

// versionsOn is how the store keeps its versions in a: none without
// Revisions.
func (s *storeCore) versionsOn(a *App) versionsOn {
	if s.revisions == 0 || a == nil {
		return versionsOn{}
	}
	return versionsOn{keep: s.revisions, clock: a.clock, held: s.versionsHeld}
}

// versionsHeld reports whether a legal hold keeps the record under key, so
// that no write prunes its versions (ADR 0006 §5, ADR 0007 §4): a hold
// placed on it or on its person, or the store's HeldUntil before its
// instant, as the record is stored. It is asked by a write that would prune,
// under the store's writers' lock or in its transaction; when kit cannot
// tell, it is held — what a hold may keep is never pruned on a guess.
func (s *storeCore) versionsHeld(ctx context.Context, key string) bool {
	a := s.app()
	if a == nil {
		return true
	}
	if _, held, err := a.holdOf(ctx, s.id, key); held || err != nil {
		return true
	}
	if s.heldFor == nil {
		return false
	}
	v, err := s.read(ctx, key)
	switch {
	case isNotFound(err):
		return false
	case err != nil:
		return true
	}
	until, held := s.heldUntil(v)
	return held && until.After(a.clock.Now())
}

// pluggedVersions are the SDK's versions as the store's engine hands them on.
func pluggedVersions(vs []docstore.Version) []plug.VersionValue {
	if vs == nil {
		return nil
	}
	out := make([]plug.VersionValue, len(vs))
	for i, v := range vs {
		out[i] = plug.VersionValue{Number: v.Number, At: v.At, Meta: v.Meta, JSON: v.JSON}
	}
	return out
}

// sdkVersions are the store engine's versions as the SDK takes them back.
func sdkVersions(vs []plug.VersionValue) []docstore.Version {
	if vs == nil {
		return nil
	}
	out := make([]docstore.Version, len(vs))
	for i, v := range vs {
		out[i] = docstore.Version{Number: v.Number, At: v.At, Meta: v.Meta, JSON: v.JSON}
	}
	return out
}

// rewritingSDK is fn, a rewrite of the store engine's versions, as the
// SDK's document store applies it.
func rewritingSDK(fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) func([]docstore.Version) ([]docstore.Version, error) {
	return func(vs []docstore.Version) ([]docstore.Version, error) {
		out, err := fn(pluggedVersions(vs))
		if err != nil {
			return nil, err
		}
		return sdkVersions(out), nil
	}
}

// The document store, in memory and in the data directory ---------------------

// stamp is what a write under ctx says about the version it makes; nothing
// on a store that keeps no versions.
func (e *docEngine) stamp(ctx context.Context) docstore.Stamp {
	if e.keep == 0 {
		return docstore.Stamp{}
	}
	return stampOf(ctx)
}

// StoredVersions are the versions of the record under key as they rest.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *docEngine) StoredVersions(_ context.Context, key string) ([]plug.VersionValue, error) {
	vs, err := e.ds.Versions(key)
	return pluggedVersions(vs), err
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, under the writers' lock.
//
//ktn:allow-unused-param: the VersionKeeper interface passes a context the local document store does not take
func (e *docEngine) RewriteVersions(_ context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return e.ds.RewriteVersions(key, rewritingSDK(fn))
}

// head is the number of the current version of the record under key,
// before a write of a transaction replaces it: a rollback takes every
// version above it out of the history. Zero on a store that keeps no
// versions.
func (e *docEngine) head(key string) uint64 {
	if e.keep == 0 {
		return 0
	}
	all, err := e.ds.Versions(key)
	if err != nil || len(all) == 0 {
		return 0
	}
	return all[0].Number
}

// forget takes out of the history of the record under key the versions a
// rolled back write made — every former version numbered above head, the
// version the write found — and gives head back the document it held, prev:
// a write in place, a workflow's transition of the state alone, changed it
// without a new number. What never committed never shows. The version the
// rollback made stays: a number is never given twice.
func (e *docEngine) forget(key string, head uint64, prev docRecord) error {
	if e.keep == 0 || head == 0 {
		return nil
	}
	held, merr := json.Marshal(prev)
	err := e.ds.RewriteVersions(key, func(former []docstore.Version) ([]docstore.Version, error) {
		former = slices.DeleteFunc(former, func(v docstore.Version) bool { return v.Number > head })
		for i := range former {
			if former[i].Number == head && merr == nil {
				former[i].JSON = held
			}
		}
		return former, nil
	})
	if errors.Is(err, docstore.WriteUnconfirmed) {
		return nil
	}
	return err
}

// The document store over SQL -------------------------------------------------

// stamp is what a write under ctx — the caller's, never the transaction's
// own, which carries the outermost caller's — says about the version it
// makes; nothing on a store that keeps no versions.
func (e *sqlEngine) stamp(ctx context.Context) docstore.Stamp {
	if e.keep == 0 {
		return docstore.Stamp{}
	}
	return stampOf(ctx)
}

// StoredVersions are the versions of the record under key as the table
// keeps them.
func (e *sqlEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return nil, err
	}
	defer done()
	vs, err := e.ds.Versions(c, key)
	return pluggedVersions(vs), err
}

// RewriteVersions rewrites the former versions of the record under key
// with fn, in one write.
func (e *sqlEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	c, done, err := e.call(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	return e.ds.RewriteVersions(c, key, rewritingSDK(fn))
}

// The engines that wrap one ---------------------------------------------------

// keptVersions are the versions eng keeps, or VersionsNotKept for an engine
// that keeps none.
func keptVersions(ctx context.Context, eng plug.StoreEngine, key string) ([]plug.VersionValue, error) {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return nil, docstore.VersionsNotKept
	}
	return vk.StoredVersions(ctx, key)
}

// rewriteKept rewrites the versions eng keeps, or is VersionsNotKept for an
// engine that keeps none.
func rewriteKept(ctx context.Context, eng plug.StoreEngine, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	vk, ok := eng.(plug.VersionKeeper)
	if !ok {
		return docstore.VersionsNotKept
	}
	return vk.RewriteVersions(ctx, key, fn)
}

// StoredVersions are the versions the engine a history wraps keeps.
func (h *historied) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return keptVersions(ctx, h.StoreEngine, key)
}

// RewriteVersions rewrites the versions the engine a history wraps keeps.
func (h *historied) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return rewriteKept(ctx, h.StoreEngine, key, fn)
}

// StoredVersions are a sealing store's versions as they rest: its sealed
// members boxes, bound where the record's are.
func (e *sealedEngine) StoredVersions(ctx context.Context, key string) ([]plug.VersionValue, error) {
	return keptVersions(ctx, e.inner, key)
}

// RewriteVersions rewrites a sealing store's versions as they rest.
func (e *sealedEngine) RewriteVersions(ctx context.Context, key string, fn func(vs []plug.VersionValue) ([]plug.VersionValue, error)) error {
	return rewriteKept(ctx, e.inner, key, fn)
}

// A sealing store's writes, with versions ---------------------------------------

// A sealing store seals a record again at every write, each member under a
// nonce of its own: the same record never rests twice as the same bytes, and
// the document store would make a version of a write that changed nothing.
// So a write whose record means what it meant keeps the record as it rests
// — no version — and one that must seal it again with the same meaning —
// under another data key, a hold placed; a member sealed, or kept in clear,
// anew — writes it in place, making none either.

var (
	// errReseal ends a write whose record means what it meant but must rest
	// otherwise: it is written again in place.
	errReseal = errs.New(CodeResealInPlace, "RESEAL_IN_PLACE", "the record is sealed again, unchanged",
		"kit: a write that changed nothing ended so the record is sealed again in place, without a version")
	// errRestMoved ends that second write when another write changed the
	// record meanwhile.
	errRestMoved = errs.New(CodeResealMoved, "RESEAL_MOVED", "the record changed meanwhile",
		"kit: a reseal in place found the record changed by another write")
)

// resealed is what a write that seals a record again in place writes: the
// record as it rested when the first write read it, and the record opened.
type resealed struct {
	was json.RawMessage
	d   plug.DocumentValue
}

// keptUpdate is Update — or, replacing, a write of the record fn sets
// whatever the record held — on a store that keeps versions: a record that
// means what it meant is kept as it rests, or sealed again in place.
func (e *sealedEngine) keptUpdate(ctx context.Context, key string, fn func(d *plug.DocumentValue) error, replacing bool) (plug.DocumentValue, error) {
	var d plug.DocumentValue
	var err error
	for range writeAttempts {
		var redo *resealed
		d, err = e.updateOnce(ctx, key, fn, replacing, &redo)
		if redo == nil {
			return d, err
		}
		d, err = e.sealInPlace(ctx, key, redo)
		if !errors.Is(err, errRestMoved) {
			return d, err
		}
	}
	// The record kept changing under the second write: written as a change,
	// a version perhaps made for nothing, never a change lost.
	return e.update(ctx, key, fn)
}

// updateOnce is one write of keptUpdate: redo is set when the record must be
// sealed again in place.
func (e *sealedEngine) updateOnce(ctx context.Context, key string, fn func(d *plug.DocumentValue) error, replacing bool, redo **resealed) (plug.DocumentValue, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	var out plug.DocumentValue
	_, err := e.inner.Update(ctx, key, func(cur *plug.DocumentValue) error {
		return e.rewrite(ctx, key, cur, keptWrite{fn: fn, replacing: replacing, release: &release, redo: redo, out: &out})
	})
	if errors.Is(err, errReseal) {
		return plug.DocumentValue{}, nil
	}
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return out, nil
}

// keptWrite is what one write of keptUpdate does, and what it tells its
// caller: fn, whether it replaces a record that does not open, what releases
// the seal once the write ends, the record to seal again in place, and the
// record opened as the write left it.
type keptWrite struct {
	fn        func(d *plug.DocumentValue) error
	replacing bool
	release   *func()
	redo      **resealed
	out       *plug.DocumentValue
}

// rewrite is the update one write of keptUpdate hands the document store:
// the record cur opened, changed by fn, sealed again — kept as it rests when
// it means what it meant, or left for w.redo to seal again in place.
func (e *sealedEngine) rewrite(ctx context.Context, key string, cur *plug.DocumentValue, w keptWrite) error {
	d, before, err := e.openedBefore(ctx, key, cur.JSON, w.replacing)
	if err != nil {
		return err
	}
	if err := w.fn(&d); err != nil {
		return err
	}
	if d.Value == nil {
		if d.Value, err = e.s.valueOf(d); err != nil {
			return err
		}
	}
	next, rel, err := e.sealRecord(ctx, d)
	if err != nil {
		return err
	}
	*w.release = rel
	*w.out = d
	if after, err := json.Marshal(d.Value); err != nil || before == nil || !bytes.Equal(before, after) {
		*cur = next
		return nil
	}
	if sameRest(cur.JSON, next.JSON) {
		cur.Value = next.Value // as it rests, opened for its indexes and the caller
		return nil
	}
	*w.redo = &resealed{was: bytes.Clone(cur.JSON), d: d}
	return errReseal
}

// openedBefore is the record raw rests as, opened, and its JSON before a
// write — none when it does not encode; a record that does not open is the
// zero value for a write that replaces it, and refuses any other.
func (e *sealedEngine) openedBefore(ctx context.Context, key string, raw []byte, replacing bool) (plug.DocumentValue, []byte, error) {
	v, doc, err := e.openRecord(ctx, key, raw)
	if err != nil {
		if replacing {
			return plug.DocumentValue{Value: e.s.addressable(nil).Interface()}, nil, nil
		}
		return plug.DocumentValue{}, nil, err
	}
	d := plug.DocumentValue{JSON: doc, Value: v}
	before, err := json.Marshal(v)
	if err != nil {
		return d, nil, nil
	}
	return d, before, nil
}

// sealInPlace seals again, in place — no version —, the record a first write
// found meaning what it meant, unless another write changed it since.
func (e *sealedEngine) sealInPlace(ctx context.Context, key string, redo *resealed) (plug.DocumentValue, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	_, err := e.inner.Update(withInPlace(ctx), key, func(cur *plug.DocumentValue) error {
		if !bytes.Equal(cur.JSON, redo.was) {
			return errRestMoved
		}
		next, rel, err := e.sealRecord(ctx, redo.d)
		if err != nil {
			return err
		}
		release = rel
		*cur = next
		return nil
	})
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return redo.d, nil
}

// sameRest reports whether a record sealed anew, next, may rest as it rested,
// was: the same members sealed, each under the same data key — what a write
// of the same record keeps, where a hold, or a member sealed or kept in
// clear anew, changes it. The values are the caller's to compare.
func sameRest(was, next []byte) bool {
	a, ra := restShape(was)
	b, rb := restShape(next)
	return bytes.Equal(a, b) && slices.Equal(ra, rb)
}

// restShape is a record as it rests with every box replaced by one mark,
// compacted, and the data keys its boxes name, sorted.
func restShape(raw []byte) ([]byte, []string) {
	var refs []string
	shape, _, err := boxWalk(raw, nil, "", false, func(_ memberAt, value []byte) ([]byte, bool, error) {
		if ref, _ := boxRef(value); !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
		return []byte(`"#"`), false, nil
	})
	if err != nil {
		// The mark replaces every box and fails none: kept for a walk that
		// ever would, as the bytes it rests as.
		return raw, nil
	}
	slices.Sort(refs)
	var out bytes.Buffer
	if json.Compact(&out, shape) != nil {
		return shape, refs
	}
	return out.Bytes(), refs
}
