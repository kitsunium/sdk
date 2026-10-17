package kit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The documents a store's engine works on (ADR 0179 §2): a record's JSON as
// it rests — byte for byte what kit encodes —, and the entity once decoded,
// kept beside it as an any so that a key, an index or a read does not decode
// it twice. The engine is compiled once, whatever the entity type: the SDK's
// document store is instantiated at docRecord alone, and what the product
// declares on its type reaches it as functions of a document
// (engineConfig).

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

// value is the document r holds, its entity when it was decoded.
func (d docRecord) value() plug.DocumentValue {
	if d.open == nil || !d.open.ok {
		return plug.DocumentValue{JSON: d.raw}
	}
	return plug.DocumentValue{JSON: d.raw, Value: d.open.v}
}

// engineConfig is what an engine knows of its documents: their key, the
// entity their JSON holds, and the indexes over them — the product's
// functions on its type, wrapped once at the declaration.
type engineConfig struct {
	// key is a document's key: it reads the document's Value, which the
	// engine decodes first unless keyAtRest gives the key.
	key func(d plug.DocumentValue) string
	// keyAtRest, when set, gives the key of a record as it rests, without
	// decoding it; false when it cannot.
	keyAtRest func(raw json.RawMessage) (string, bool)
	// decode is the entity a document's JSON holds.
	decode func(raw json.RawMessage) (any, error)
	// indexes are the secondary indexes, over documents.
	indexes []plug.IndexSpec
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

// decoded is r with its entity decoded, as cfg decodes it; a record that
// does not decode keeps none, and load keeps why.
func (c *engineConfig) decoded(r docRecord, load *docLoad) plug.DocumentValue {
	if r.open == nil {
		r.open = &openedDoc{}
	}
	if !r.open.ok && c.decode != nil {
		v, err := c.decode(r.raw)
		if err != nil {
			load.failed(err)
			return plug.DocumentValue{JSON: r.raw}
		}
		r.open.v, r.open.ok = v, true
	}
	return r.value()
}

// keyOf is the document store's key function: r's key, read as it rests
// when keyAtRest can, else from its entity, decoded once.
func (c *engineConfig) keyOf(r docRecord, load *docLoad) string {
	if r.open != nil && r.open.keyed {
		return r.open.key
	}
	key, ok := "", false
	if c.keyAtRest != nil && (r.open == nil || !r.open.ok) {
		key, ok = c.keyAtRest(r.raw)
	}
	if !ok {
		key = c.key(c.decoded(r, load))
	}
	if r.open != nil {
		r.open.key, r.open.keyed = key, true
	}
	return key
}

// specs are cfg's indexes as the document store takes them, over records.
func (c *engineConfig) specs(load *docLoad) []docstore.IndexSpec[docRecord] {
	out := make([]docstore.IndexSpec[docRecord], len(c.indexes))
	for i, ix := range c.indexes {
		keys := ix.Keys
		out[i] = docstore.IndexSpec[docRecord]{Name: ix.Name, Unique: ix.Unique, Keys: func(r docRecord) []string {
			return keys(c.decoded(r, load))
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

// undecodable is a record that does not decode into its store's type, as
// the SDK's document store says it: DocumentUndecodable, naming the store
// and what failed, never a byte of the record.
func undecodable(store string, err error) error {
	return errs.Wrap(docstore.DocumentUndecodable, errs.WrapParams{}, errs.String("store", store), errs.String("cause", jsonCause(err)))
}

// unencodable is an entity that does not encode, as the SDK's document
// store says it: DocumentUnencodable, naming the store and what failed,
// never what the entity held.
func unencodable(store string, err error) error {
	return errs.Wrap(docstore.DocumentUnencodable, errs.WrapParams{}, errs.String("store", store), errs.String("cause", encodeCause(err)))
}

// jsonCause describes a decoding failure without a byte of the record:
// encoding/json quotes the offending character of a syntax error.
func jsonCause(err error) string {
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		typeName := "an unknown type"
		if typeErr.Type != nil {
			typeName = typeErr.Type.String()
		}
		return "the stored value of field " + typeErr.Field + " does not fit " + typeName
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return "malformed JSON at offset " + strconv.FormatInt(syntaxErr.Offset, 10)
	}
	return fmt.Sprintf("%T", err)
}

// encodeCause describes an encoding failure without the value that failed.
func encodeCause(err error) string {
	if unsupported, ok := errors.AsType[*json.UnsupportedTypeError](err); ok {
		return "unsupported type " + unsupported.Type.String()
	}
	if marshalerErr, ok := errors.AsType[*json.MarshalerError](err); ok {
		return "the marshaler of " + marshalerErr.Type.String() + " failed"
	}
	return fmt.Sprintf("%T", err)
}
