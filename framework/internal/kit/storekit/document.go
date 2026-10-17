package storekit

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The documents a store's engine works on (ADR 0179 §2): a record's JSON as
// it rests — byte for byte what kit encodes —, and the entity once decoded,
// kept beside it as an any so that a key, an index or a read does not decode
// it twice. The engine is compiled once, whatever the entity type: the SDK's
// document store is instantiated at docRecord alone, and what the product
// declares on its type reaches it as functions of a document
// (plug.StoreConfig).

// docRecord is a document as the SDK's document store holds it: its JSON as
// it rests, and — once something decoded it — the entity, shared by the
// copies the document store makes of one decoded record, so that its key
// function and its index functions decode it once.
type docRecord struct {
	raw  json.RawMessage
	open *openedDoc
}

// openedDoc is what kit learnt of one record: its entity, and its key.
type openedDoc struct {
	v any
	// ok says v is decoded; keyed that key is known.
	ok, keyed bool
	key       string
}

// MarshalJSON is the record as it rests.
func (d docRecord) MarshalJSON() ([]byte, error) { return d.raw, nil }

// UnmarshalJSON keeps the record as it rests; decoding it is the engine's.
func (d *docRecord) UnmarshalJSON(b []byte) error {
	d.raw = bytes.Clone(b)
	d.open = &openedDoc{}
	return nil
}

// recordFor is the record the document store keeps of d.
func recordFor(d plug.DocumentValue) docRecord {
	return docRecord{raw: d.JSON, open: &openedDoc{v: d.Value, ok: d.Value != nil}}
}

// rawRecord is a record as it rests, not decoded yet.
func rawRecord(raw json.RawMessage) docRecord {
	return docRecord{raw: bytes.Clone(raw), open: &openedDoc{}}
}

// value is the document r holds, its entity when it was decoded.
func (d docRecord) value() plug.DocumentValue {
	if d.open == nil || !d.open.ok {
		return plug.DocumentValue{JSON: d.raw}
	}
	return plug.DocumentValue{JSON: d.raw, Value: d.open.v}
}

// docLoad keeps the first record an engine could not decode while it keyed
// or indexed it: that refuses the engine's opening, or its refiling.
type docLoad struct {
	mu  sync.Mutex
	err error
}

// failed keeps err, when it is the first.
func (l *docLoad) failed(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		l.err = err
	}
}

// loaded is the first record that did not decode, nil when none.
func (l *docLoad) loaded() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// documents is what an engine knows of its documents: their key, the
// entity their JSON holds, and the indexes over them — the product's
// functions on its type, wrapped once at the declaration — and the first
// record it could not decode.
type documents struct {
	cfg  *plug.StoreConfig
	load docLoad
}

// decoded is r with its entity decoded, as the configuration decodes it; a
// record that does not decode keeps none, and the load keeps why.
func (c *documents) decoded(r docRecord) plug.DocumentValue {
	if r.open == nil {
		r.open = &openedDoc{}
	}
	if !r.open.ok && c.cfg.Decode != nil {
		v, err := c.cfg.Decode(r.raw)
		if err != nil {
			c.load.failed(err)
			return plug.DocumentValue{JSON: r.raw}
		}
		r.open.v, r.open.ok = v, true
	}
	return r.value()
}

// keyOf is the document store's key function: r's key, read as it rests
// when the configuration's KeyAtRest can, else from its entity, decoded
// once.
func (c *documents) keyOf(r docRecord) string {
	if r.open != nil && r.open.keyed {
		return r.open.key
	}
	key, ok := "", false
	if c.cfg.KeyAtRest != nil && (r.open == nil || !r.open.ok) {
		key, ok = c.cfg.KeyAtRest(r.raw)
	}
	if !ok {
		key = c.cfg.Key(c.decoded(r))
	}
	if r.open != nil {
		r.open.key, r.open.keyed = key, true
	}
	return key
}

// writtenKey is r's key, "" when the key function panics: the document
// store says why the write failed.
func (c *documents) writtenKey(r docRecord) (key string) {
	defer func() {
		if recover() != nil {
			key = ""
		}
	}()
	return c.keyOf(r)
}

// specs are the configuration's indexes as the document store takes them,
// over records.
func (c *documents) specs() []docstore.IndexSpec[docRecord] {
	out := make([]docstore.IndexSpec[docRecord], len(c.cfg.Indexes))
	for i, ix := range c.cfg.Indexes {
		keys := ix.Keys
		out[i] = docstore.IndexSpec[docRecord]{Name: ix.Name, Unique: ix.Unique, Keys: func(r docRecord) []string {
			return keys(c.decoded(r))
		}}
	}
	return out
}

// values are the documents rs hold, as they rest.
func values(rs []docRecord) []plug.DocumentValue {
	out := make([]plug.DocumentValue, len(rs))
	for i, r := range rs {
		out[i] = r.value()
	}
	return out
}

// filtered are the documents of all keep accepts.
func filtered(all []plug.DocumentValue, keep func(d plug.DocumentValue) bool) []plug.DocumentValue {
	out := all[:0]
	for _, d := range all {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out
}

// raws are the records of entries as they rest.
func raws(entries []plug.EntryValue) []json.RawMessage {
	out := make([]json.RawMessage, len(entries))
	for i, en := range entries {
		out[i] = en.JSON
	}
	return out
}

// pluggedEntries are the SDK's entries as an engine hands them on.
func pluggedEntries(entries []docstore.Entry) []plug.EntryValue {
	out := make([]plug.EntryValue, len(entries))
	for i, en := range entries {
		out[i] = plug.EntryValue{Key: en.Key, JSON: en.JSON}
	}
	return out
}

// stamp is what a write under ctx says about the version it makes, as the
// configuration stamps it; nothing on a store that keeps no versions.
func (c *documents) stamp(ctx context.Context) docstore.Stamp {
	if c.cfg.Versions == 0 || c.cfg.Stamp == nil {
		return docstore.Stamp{}
	}
	st := c.cfg.Stamp(ctx)
	return docstore.Stamp{Meta: st.Meta, InPlace: st.InPlace}
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

// pluggedTombstones are the SDK's tombstones as the store's engine hands
// them on.
func pluggedTombstones(stones []docstore.Tombstone) []plug.TombstoneValue {
	if stones == nil {
		return nil
	}
	out := make([]plug.TombstoneValue, len(stones))
	for i, st := range stones {
		out[i] = plug.TombstoneValue{Key: st.Key, At: st.At}
	}
	return out
}

// clockFunc is the app's clock as the SDK's document store reads it.
type clockFunc func() time.Time

// clockOf is now as the SDK's document store reads it, nil — the store's
// own — for none.
func clockOf(now func() time.Time) clock.Clock {
	if now == nil {
		return nil
	}
	return clockFunc(now)
}

// Now is the clock's instant.
func (f clockFunc) Now() time.Time { return f() }

// Since is how long ago t was, on the clock.
func (f clockFunc) Since(t time.Time) time.Duration { return f().Sub(t) }

// pluggedWeight is the SDK's weight of a store's versions as its engine
// hands it on.
func pluggedWeight(w docstore.VersionWeight, err error) (plug.VersionWeightValue, error) {
	return plug.VersionWeightValue{Former: w.Former, Trashed: w.Trashed, Bytes: w.Bytes}, said(err)
}
