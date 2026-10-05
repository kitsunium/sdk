package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kitsunium/sdk/pkg/v1/concur/snapshot"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A store that seals (ADR 0006 §4): the SDK's document store keeps each
// record as it rests — its sealed members boxes —, and kit opens a record
// when it reads it and seals it when it writes it. The document store's
// key and index functions see the record opened: a record kit writes
// carries itself, opened, to them; one the store loads is opened once, by
// the first of them to need it. The product's indexes, kit's subject index
// and its workflows see plain values; the files, the snapshot and every
// write since, never do.
//
// A record's boxes are bound to where they lie — the store, the record's
// key, the member's pointer —, so opening one needs the record's key before
// the record is open. kit reads it from the members it does not seal: a
// store's key must not read a member kit seals. When one does — a store of
// accounts keyed by its subject —, the write that finds it keeps that
// member in clear, as the key it makes already is in the store's files,
// and the store says so once.

// sealedDoc is a record as a sealing store's document store holds it: its
// JSON at rest, and — once kit has it — the record opened, shared by the
// copies the document store makes of one decoded document. It is one type
// for every store's record: the engines under it are compiled once, not
// once per record type.
type sealedDoc struct {
	raw  json.RawMessage
	open *openedDoc
}

// openedDoc is a record opened, and its key.
type openedDoc struct {
	v   any
	key string
	ok  bool
}

// openedAs is the record o holds, as its store's type.
func openedAs[T any](o *openedDoc) T {
	v, _ := o.v.(T)
	return v
}

// MarshalJSON is the record as it rests.
func (d sealedDoc) MarshalJSON() ([]byte, error) { return d.raw, nil }

// UnmarshalJSON keeps the record as it rests; opening it is kit's.
func (d *sealedDoc) UnmarshalJSON(b []byte) error {
	d.raw = bytes.Clone(b)
	d.open = &openedDoc{}
	return nil
}

// sealedEngine is a store's engine when it seals: an engine of sealedDocs
// — whichever the app places the store on: the SDK's document store in
// memory or in the data directory, its store over SQL —, and kit's sealing
// around it. A sealed member is a string to that engine, kept as it is.
type sealedEngine[T any] struct {
	inner storeEngine[sealedDoc]
	s     *StoreService[T]
	a     *App
	z     *sealer
	// clear are the members, by pointer, that the store's key reads: kept
	// in clear from the write that found them on. Copy-on-write, the SDK's
	// snapshot.Value: a seal reads them with no lock, and learn adds to them
	// in one serialised read-modify-write, so two writes learning at once
	// both keep what they found.
	clear snapshot.Value[map[string]bool]
	// warned says the store said so.
	warned atomic.Bool
	// loadErr is the first record the document store's load could not
	// open: it refuses the start.
	loadMu  sync.Mutex
	loadErr error
}

// sealedOpener opens the engine of sealedDocs a sealing store runs on, keyed
// and indexed by what sealing gives it.
type sealedOpener[T any] func(key func(sealedDoc) string, specs []docstore.IndexSpec[sealedDoc]) (storeEngine[sealedDoc], error)

// openSealedEngine opens the engine of a store that seals, with open — a
// loading engine's records opened once to key and index them.
func openSealedEngine[T any](s *StoreService[T], a *App, z *sealer, specs []docstore.IndexSpec[T], open sealedOpener[T]) (*sealedEngine[T], error) {
	e := &sealedEngine[T]{s: s, a: a, z: z}
	wrapped := make([]docstore.IndexSpec[sealedDoc], len(specs))
	for i, spec := range specs {
		keys := spec.Keys
		wrapped[i] = docstore.IndexSpec[sealedDoc]{Name: spec.Name, Unique: spec.Unique, Keys: func(d sealedDoc) []string {
			v, ok := e.plainOf(d)
			if !ok {
				return nil
			}
			return keys(v)
		}}
	}
	inner, err := open(e.docKey, wrapped)
	if err != nil {
		// What the load could not open says why better than what it made
		// the engine say: a record that does not decode, rather than one
		// under another key.
		if lerr := e.loaded(); lerr != nil {
			return nil, lerr
		}
		return nil, err
	}
	if err := e.loaded(); err != nil {
		return nil, errors.Join(err, inner.Close())
	}
	e.inner = inner
	return e, nil
}

// loaded is what the load could not open.
func (e *sealedEngine[T]) loaded() error {
	e.loadMu.Lock()
	defer e.loadMu.Unlock()
	return e.loadErr
}

// failedLoad keeps the first record the load could not open.
func (e *sealedEngine[T]) failedLoad(err error) {
	e.loadMu.Lock()
	defer e.loadMu.Unlock()
	if e.loadErr == nil {
		e.loadErr = err
	}
}

// docKey is the document store's key function: the key of the record kit
// wrote, or read from the members of the record loaded kit does not seal —
// decoded once when it holds no box, and kept for its indexes.
func (e *sealedEngine[T]) docKey(d sealedDoc) string {
	key, _ := e.docKeyOf(d)
	return key
}

// docKeyOf is docKey, and false when the record loaded does not decode:
// the load fails, and its key is none.
func (e *sealedEngine[T]) docKeyOf(d sealedDoc) (string, bool) {
	if d.open != nil && d.open.ok {
		return d.open.key, true
	}
	if hasBoxes(d.raw) {
		return e.s.keyOf(e.lenient(d.raw)), true
	}
	var v T
	if err := json.Unmarshal(d.raw, &v); err != nil {
		e.failedLoad(errs.Wrap(docstore.DocumentUndecodable, errs.WrapParams{}, errs.String("store", e.s.id), errs.String("cause", err.Error())))
		return "", false
	}
	key := e.s.keyOf(v)
	if d.open != nil {
		d.open.v, d.open.key, d.open.ok = v, key, true
	}
	return key, true
}

// plainOf is the record d, opened: the one kit wrote, or the one loaded,
// opened now and kept for the document store's next function.
func (e *sealedEngine[T]) plainOf(d sealedDoc) (T, bool) {
	if d.open != nil && d.open.ok {
		return openedAs[T](d.open), true
	}
	key := e.docKey(d)
	v, err := e.openRecord(context.Background(), key, d.raw)
	if err != nil {
		e.failedLoad(err)
		var zero T
		return zero, false
	}
	if d.open != nil {
		d.open.v, d.open.key, d.open.ok = v, key, true
	}
	return v, true
}

// lenient is the record raw holds without its sealed members: what the
// store's key reads, since it reads none of them.
func (e *sealedEngine[T]) lenient(raw []byte) T {
	var v T
	doc, _, err := boxWalk(raw, nil, "", false, func(memberAt, []byte) ([]byte, bool, error) { return nil, true, nil })
	if err != nil {
		return v
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		// Decoded as far as it decodes: the key reads what it can.
		return v
	}
	return v
}

// openRecord is the record under key, raw as it rests, opened and decoded.
// A member whose data key is destroyed reads as its zero value.
func (e *sealedEngine[T]) openRecord(ctx context.Context, key string, raw []byte) (T, error) {
	var v T
	doc, err := e.openDoc(ctx, key, raw)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return v, errs.Wrap(docstore.DocumentUndecodable, errs.WrapParams{}, errs.String("store", e.s.id), errs.String("cause", err.Error()))
	}
	return v, nil
}

// openDoc opens every box of raw, the record under key as it rests: a
// member whose data key is destroyed is left out, and decodes as its zero
// value. A box in a member no longer classified opens all the same: a field
// whose class changed reads what it held.
func (e *sealedEngine[T]) openDoc(ctx context.Context, key string, raw []byte) ([]byte, error) {
	doc, _, err := boxWalk(raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		plain, err := e.z.open(ctx, value, e.s.id, key, at.pointer)
		switch {
		case errors.Is(err, errErased):
			return nil, true, nil
		case err != nil && at.f == nil && errs.HasCode(err, CodeSealOpen):
			// Text of a member kit does not seal that reads like a box: the
			// product's, kept as it is.
			return value, false, nil
		case err != nil:
			return nil, false, err
		}
		return plain, false, nil
	})
	return doc, err
}

// sealRecord is v as the store keeps it at rest: its members kit seals
// sealed under its data key — its subject's, or its own — bound to the
// store, its key and their pointers. It returns what releases the data
// key's stripe, held until the write stands: inside a transaction
// ([Transact]), until it ends (heldToTheEnd).
func (e *sealedEngine[T]) sealRecord(ctx context.Context, v T) (sealedDoc, func(), error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return sealedDoc{}, func() {}, errs.Wrap(docstore.DocumentUnencodable, errs.WrapParams{}, errs.String("store", e.s.id), errs.String("cause", err.Error()))
	}
	key := e.s.keyOf(v)
	d := sealedDoc{raw: raw, open: &openedDoc{v: v, key: key, ok: true}}
	if key == "" || !e.s.plan().sealsAny() {
		return d, func() {}, nil // nothing to seal; an empty key, the document store says why
	}
	ref, err := e.refOf(ctx, key, v)
	if err != nil {
		return sealedDoc{}, func() {}, err
	}
	lock := e.z.locks.of(ref)
	lock.RLock()
	d.raw, err = e.sealKeyed(ctx, raw, key, ref)
	if err != nil {
		lock.RUnlock()
		return sealedDoc{}, func() {}, err
	}
	return d, heldToTheEnd(ctx, lock.RUnlock), nil
}

// heldToTheEnd is release — a data key's stripe, held shared since a write
// sealed under it — as the write's caller runs it once the write stands: at
// once outside a transaction; inside one, when the transaction ends — its
// commit makes the write stand, its rollback takes it back —, so that no
// destruction of the key lands between the write and its commit, where
// what it looks for is not yet to be seen. The stripes are released before
// the transaction's held effects run, a destruction among them.
func heldToTheEnd(ctx context.Context, release func()) func() {
	if atEnd(ctx, release) {
		return func() {}
	}
	return release
}

// refOf is the data key the record under key is sealed under: its
// subject's, else its own. A record a hold keeps — placed, or the store's
// HeldUntil — is sealed under its own: its person's erasure destroys their
// key, and a held record is kept whoever asks. When kit cannot tell whether
// a hold keeps it, its own key is the one that keeps it readable.
func (e *sealedEngine[T]) refOf(ctx context.Context, key string, v T) (string, error) {
	keys, err := e.a.referenceKeys(ctx)
	if err != nil {
		return "", err
	}
	id := e.s.subjectOf(v)
	if id == "" || e.held(ctx, key, v) {
		return keys.ownRef(e.s.id, key), nil
	}
	return keys.personRef(id), nil
}

// held reports whether a hold keeps the record under key, v as it is
// written: one placed on it, or the store's HeldUntil at the app's now. When
// kit cannot tell, it is held.
func (e *sealedEngine[T]) held(ctx context.Context, key string, v T) bool {
	if until, ok := e.s.heldUntil(v); ok && until.After(e.a.clock.Now()) {
		return true
	}
	_, held, err := e.a.holdOf(ctx, e.s.id, key)
	return held || err != nil
}

// sealKeyed seals raw, the record under key, and makes sure what the store's
// key reads stays readable: when the members kit leaves in clear no longer
// give the key, the members the key reads are found, kept in clear from now
// on, and the store says so.
func (e *sealedEngine[T]) sealKeyed(ctx context.Context, raw []byte, key, ref string) ([]byte, error) {
	sealed, err := e.seal(ctx, raw, key, ref, e.clearNow())
	if err != nil || e.s.keyOf(e.lenient(sealed)) == key {
		return sealed, err
	}
	learned := e.keyReads(raw, key)
	e.learn(learned)
	if learned == nil {
		// The key reads a member inside a list or a map: the record rests in
		// clear, as the store's key must be read.
		return raw, nil
	}
	return e.seal(ctx, raw, key, ref, e.clearNow())
}

// seal seals the members of raw kit seals — but those in clear — under ref,
// bound to the store, key and each member's pointer. An erasure's write
// leaves the members it cleared as they are: they hold nothing, and the
// record says it was erased.
func (e *sealedEngine[T]) seal(ctx context.Context, raw []byte, key, ref string, clear map[string]bool) ([]byte, error) {
	erasing := intentOf(ctx).erases != nil
	out, _, err := sealWalk(raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		switch {
		case isNull(value), !at.inList && clear[at.pointer], erasing && zeroJSON(value, at.f.typ):
			return value, false, nil
		}
		box, err := e.z.seal(ctx, ref, value, e.s.id, key, at.pointer)
		return box, false, err
	})
	return out, err
}

// clearNow is the members the store keeps in clear because its key reads
// them.
func (e *sealedEngine[T]) clearNow() map[string]bool {
	if m := e.clear.Load(); m != nil {
		return *m
	}
	return nil
}

// keyReads finds the members of raw — outside every list and map — that the
// store's key reads: those that, left in clear, give the key back when
// every other member kit seals is left out. It returns nil when no such
// members do.
func (e *sealedEngine[T]) keyReads(raw []byte, key string) []string {
	// Left in clear one by one until the key comes back, then each dropped
	// again when the key does not need it.
	var plain []string
	for _, c := range e.keyCandidates(raw) {
		if plain = append(plain, c); e.keepsKey(raw, key, plain) {
			break
		}
	}
	if !e.keepsKey(raw, key, plain) {
		return nil
	}
	for i := 0; i < len(plain); {
		without := slices.Delete(slices.Clone(plain), i, i+1)
		if e.keepsKey(raw, key, without) {
			plain = without
			continue
		}
		i++
	}
	return plain
}

// keyCandidates are the members of raw kit seals, outside every list and
// map and not null: those the store's key may read.
func (e *sealedEngine[T]) keyCandidates(raw []byte) []string {
	var out []string
	_, _, err := sealWalk(raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		if !at.inList && !isNull(value) {
			out = append(out, at.pointer)
		}
		return value, false, nil
	})
	if err != nil {
		return nil
	}
	return out
}

// keepsKey reports whether raw gives key back with, of the members kit
// seals, only those at pointers left.
func (e *sealedEngine[T]) keepsKey(raw []byte, key string, pointers []string) bool {
	doc, _, err := sealWalk(raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		return value, !slices.Contains(pointers, at.pointer) || at.inList, nil
	})
	return err == nil && e.s.keyOf(e.lenient(doc)) == key
}

// learn keeps pointers in clear from now on, and says it once.
func (e *sealedEngine[T]) learn(pointers []string) {
	e.clear.Update(func(cur *map[string]bool) *map[string]bool {
		var next map[string]bool
		if cur != nil {
			next = maps.Clone(*cur)
		}
		if next == nil {
			next = map[string]bool{}
		}
		for _, p := range pointers {
			next[p] = true
		}
		return &next
	})
	if e.warned.CompareAndSwap(false, true) {
		fields := strings.Join(pointers, ", ")
		if pointers == nil {
			fields = "a member inside a list or a map"
		}
		e.a.problem(e.s.id, say("seal.key-reads", "store", e.s.id, "fields", fields))
		logger.Warn(context.Background(), e.a.log, "a store's key reads a member kit seals: kept in clear at rest",
			logger.String("store", e.s.id), logger.String("fields", fields))
	}
}
