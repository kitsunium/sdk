package kit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/concur/snapshot"
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

// sealedEngine is a store's engine when it seals: an engine of records as
// they rest — whichever the app places the store on: the SDK's document
// store in memory or in the data directory, its store over SQL —, and kit's
// sealing around it. A sealed member is a string to that engine, kept as it
// is. The documents it hands its caller are the records opened; those it
// hands the engine inside are the records as they rest, beside their entity
// opened — which the engine's key and indexes read.
type sealedEngine struct {
	inner plug.StoreEngine
	s     *storeCore
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
}

// sealedOpener opens the engine of records as they rest a sealing store runs
// on, keyed and indexed as cfg says.
type sealedOpener func(cfg *engineConfig) (plug.StoreEngine, error)

// openSealedEngine opens the engine of a store that seals, with open — a
// loading engine's records opened once to key and index them, and a record
// the load could not open refusing the start.
func openSealedEngine(s *storeCore, a *App, z *sealer, specs []plug.IndexSpec, open sealedOpener) (*sealedEngine, error) {
	e := &sealedEngine{s: s, a: a, z: z}
	inner, err := open(&engineConfig{key: e.docKey, keyAtRest: e.keyAtRest, decode: e.decodeAtRest, indexes: specs})
	if err != nil {
		return nil, err
	}
	e.inner = inner
	return e, nil
}

// docKey is the engine inside's key function: the key of the record kit
// wrote, or of the record loaded.
func (e *sealedEngine) docKey(d plug.DocumentValue) string {
	if d.Value != nil {
		return e.s.keyOf(d.Value)
	}
	return e.restingKey(d.JSON)
}

// keyAtRest is the key of a record as it rests that holds a box, read from
// the members kit does not seal; false for one that holds none, which is
// decoded as it is.
func (e *sealedEngine) keyAtRest(raw json.RawMessage) (string, bool) {
	if !hasBoxes(raw) {
		return "", false
	}
	return e.s.keyOf(e.lenient(raw)), true
}

// restingKey is the key of a record as it rests: read from its clear
// members, or from the record decoded; "" when it does not decode.
func (e *sealedEngine) restingKey(raw json.RawMessage) string {
	if key, ok := e.keyAtRest(raw); ok {
		return key
	}
	v, err := e.s.decodeEntity(raw)
	if err != nil {
		return ""
	}
	return e.s.keyOf(v)
}

// decodeAtRest is the entity a record as it rests holds, opened: what the
// engine inside's key and indexes read.
func (e *sealedEngine) decodeAtRest(raw json.RawMessage) (any, error) {
	key := ""
	if hasBoxes(raw) {
		key = e.s.keyOf(e.lenient(raw))
	}
	v, _, err := e.openRecord(context.Background(), key, raw)
	return v, err
}

// lenient is the record raw holds without its sealed members: what the
// store's key reads, since it reads none of them. It is decoded as far as it
// decodes: the key reads what it can.
func (e *sealedEngine) lenient(raw []byte) any {
	doc, _, err := boxWalk(raw, nil, "", false, func(memberAt, []byte) ([]byte, bool, error) { return nil, true, nil })
	if err != nil || e.s.ent.decode == nil {
		return e.s.addressable(nil).Interface()
	}
	v, _ := e.s.ent.decode(doc)
	return v
}

// openRecord is the record under key, raw as it rests, opened and decoded,
// and its JSON opened. A member whose data key is destroyed reads as its
// zero value.
func (e *sealedEngine) openRecord(ctx context.Context, key string, raw []byte) (any, json.RawMessage, error) {
	doc, err := e.openDoc(ctx, key, raw)
	if err != nil {
		return nil, nil, err
	}
	v, err := e.s.decodeEntity(doc)
	if err != nil {
		return nil, nil, err
	}
	return v, doc, nil
}

// opened is the record d holds as it rests, opened.
func (e *sealedEngine) opened(ctx context.Context, key string, d plug.DocumentValue) (plug.DocumentValue, error) {
	v, doc, err := e.openRecord(ctx, key, d.JSON)
	if err != nil {
		return plug.DocumentValue{}, err
	}
	return plug.DocumentValue{JSON: doc, Value: v}, nil
}

// openDoc opens every box of raw, the record under key as it rests: a
// member whose data key is destroyed is left out, and decodes as its zero
// value. A box in a member no longer classified opens all the same: a field
// whose class changed reads what it held.
func (e *sealedEngine) openDoc(ctx context.Context, key string, raw []byte) ([]byte, error) {
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

// sealRecord is d, a record opened, as the store keeps it at rest: its
// members kit seals sealed under its data key — its subject's, or its own —
// bound to the store, its key and their pointers. It returns what releases
// the data key's stripe, held until the write stands: inside a transaction
// ([Transact]), until it ends (heldToTheEnd).
func (e *sealedEngine) sealRecord(ctx context.Context, d plug.DocumentValue) (plug.DocumentValue, func(), error) {
	raw := d.JSON
	if raw == nil {
		var err error
		if raw, err = json.Marshal(d.Value); err != nil {
			return plug.DocumentValue{}, func() {}, unencodable(e.s.id, err)
		}
	}
	key := e.s.keyOf(d.Value)
	out := plug.DocumentValue{JSON: raw, Value: d.Value}
	if key == "" || !e.s.plan().sealsAny() {
		return out, func() {}, nil // nothing to seal; an empty key, the document store says why
	}
	ref, err := e.refOf(ctx, key, d.Value)
	if err != nil {
		return plug.DocumentValue{}, func() {}, err
	}
	lock := e.z.locks.of(ref)
	lock.RLock()
	out.JSON, err = e.sealKeyed(ctx, raw, key, ref)
	if err != nil {
		lock.RUnlock()
		return plug.DocumentValue{}, func() {}, err
	}
	return out, heldToTheEnd(ctx, lock.RUnlock), nil
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
// a hold keeps it, its own key is the one that keeps it readable. An entry
// of the privacy journal is sealed under its month's key (journalRef): it
// is about nobody, no hold keeps it, and a key per entry would grow kit's
// store of keys with every entry (ADR 0178 §C3).
func (e *sealedEngine) refOf(ctx context.Context, key string, v any) (string, error) {
	if entry, ok := v.(journalRecord); ok {
		return journalRef(entry.At), nil
	}
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
func (e *sealedEngine) held(ctx context.Context, key string, v any) bool {
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
func (e *sealedEngine) sealKeyed(ctx context.Context, raw []byte, key, ref string) ([]byte, error) {
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
func (e *sealedEngine) seal(ctx context.Context, raw []byte, key, ref string, clear map[string]bool) ([]byte, error) {
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
func (e *sealedEngine) clearNow() map[string]bool {
	if m := e.clear.Load(); m != nil {
		return *m
	}
	return nil
}

// keyReads finds the members of raw — outside every list and map — that the
// store's key reads: those that, left in clear, give the key back when
// every other member kit seals is left out. It returns nil when no such
// members do.
func (e *sealedEngine) keyReads(raw []byte, key string) []string {
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
func (e *sealedEngine) keyCandidates(raw []byte) []string {
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
func (e *sealedEngine) keepsKey(raw []byte, key string, pointers []string) bool {
	doc, _, err := sealWalk(raw, e.s.plan().rules, "", false, func(at memberAt, value []byte) ([]byte, bool, error) {
		return value, !slices.Contains(pointers, at.pointer) || at.inList, nil
	})
	return err == nil && e.s.keyOf(e.lenient(doc)) == key
}

// learn keeps pointers in clear from now on, and says it once.
func (e *sealedEngine) learn(pointers []string) {
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
