package bson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// D is a document whose element order matters: the shape a decode into an
// interface produces for a document, and the one to encode when the order of
// the keys is part of the meaning — a command, a sort specification. Encode an
// M or a struct when it is not. A nil D encodes as null.
type D []E

// E is one element of a D: a key and its value. Encoded on its own, outside
// a D, it is an ordinary struct, the document {"key": …, "value": …}.
type E struct {
	// Key is the element name. It must not contain a NUL byte.
	Key string
	// Value is any value the codec encodes.
	Value any
}

// M is a document whose element order does not matter. The codec writes a
// map's keys in sorted order, so encoding the same M twice gives the same
// bytes.
type M map[string]any

// A is an array: the shape a decode into an interface produces for one.
type A []any

// Binary is a BSON binary value: the bytes and the subtype naming what they
// are. A []byte encodes as a Binary of subtype BinaryGeneric; a Binary of any
// other subtype keeps its subtype through a round trip.
type Binary struct {
	// Subtype is the BSON binary subtype, BinaryGeneric for opaque bytes.
	Subtype byte
	// Data is the payload.
	Data []byte
}

// Equal reports whether b and other hold the same subtype and bytes.
func (b Binary) Equal(other Binary) bool {
	//: both halves of the value.
	return b.Subtype == other.Subtype && bytes.Equal(b.Data, other.Data)
}

// IsZero reports whether b is the empty generic binary — what omitempty
// leaves out.
func (b Binary) IsZero() bool {
	//: the zero subtype and no bytes.
	return b.Subtype == BinaryGeneric && len(b.Data) == 0
}

// Undefined is the deprecated BSON undefined value. It decodes into an
// interface as Undefined{}, and into a typed target as that type's zero.
type Undefined struct{}

// Null is the BSON null value, for writing an explicit null into a D. A null
// decodes into an interface as nil.
type Null struct{}

// MinKey is the BSON value that sorts lower than every other.
type MinKey struct{}

// MaxKey is the BSON value that sorts higher than every other.
type MaxKey struct{}

// DateTime is a BSON datetime: milliseconds since the Unix epoch, UTC. A
// time.Time encodes as one and decodes from one; DateTime is what a decode
// into an interface produces.
type DateTime int64

// NewDateTimeFromTime returns the DateTime t falls in, truncated to the
// millisecond: the instant a time.Time is written as.
func NewDateTimeFromTime(t time.Time) DateTime {
	//: seconds scaled, then the whole milliseconds of the fraction.
	return DateTime(t.Unix()*1e3 + int64(t.Nanosecond())/1e6)
}

// Time returns the instant dt denotes, in the local time zone; call UTC on it
// for the zone BSON stores.
func (dt DateTime) Time() time.Time {
	//: Go's division truncates toward zero, so a negative remainder lands in
	//: the nanoseconds and time.Unix normalises it.
	return time.Unix(int64(dt)/1e3, int64(dt)%1e3*1e6)
}

// MarshalJSON writes dt as encoding/json writes the UTC time.Time it denotes.
func (dt DateTime) MarshalJSON() ([]byte, error) {
	//: the time package owns the textual form.
	return json.Marshal(dt.Time().UTC())
}

// UnmarshalJSON reads the textual form MarshalJSON writes. A JSON null leaves
// dt unchanged, as it leaves a time.Time.
func (dt *DateTime) UnmarshalJSON(data []byte) error {
	//: null is "no value", not an instant.
	if string(data) == "null" {
		//: nothing to set.
		return nil
	}
	var parsed time.Time
	//: the time package owns the textual form.
	if err := json.Unmarshal(data, &parsed); err != nil {
		//: not a timestamp encoding/json reads.
		return valueError(err, "DateTime.UnmarshalJSON: the input is not an RFC 3339 JSON string")
	}
	*dt = NewDateTimeFromTime(parsed)
	//: set.
	return nil
}

// Regex is a BSON regular expression: a pattern and its options. Neither may
// contain a NUL byte. The codec writes the options in alphabetical order, as
// the specification requires, whatever order they were given in.
type Regex struct {
	// Pattern is the expression.
	Pattern string
	// Options are the single-letter flags, such as "i" or "imx".
	Options string
}

// String renders r for a log line or a test failure.
func (r Regex) String() string {
	//: a fixed, JSON-looking shape.
	return fmt.Sprintf(`{"pattern": "%s", "options": "%s"}`, r.Pattern, r.Options)
}

// Equal reports whether r and other have the same pattern and options.
func (r Regex) Equal(other Regex) bool {
	//: both strings, byte for byte.
	return r.Pattern == other.Pattern && r.Options == other.Options
}

// IsZero reports whether r is the empty regex — what omitempty leaves out.
func (r Regex) IsZero() bool {
	//: no pattern and no options.
	return r.Pattern == "" && r.Options == ""
}

// DBPointer is the deprecated BSON DBPointer: a namespace and an ObjectID.
// It is read and written so a document carrying one survives a round trip.
type DBPointer struct {
	// DB is the namespace the pointer names.
	DB string
	// Pointer is the identifier within it.
	Pointer ObjectID
}

// String renders p for a log line or a test failure.
func (p DBPointer) String() string {
	//: a fixed, JSON-looking shape.
	return fmt.Sprintf(`{"db": "%s", "pointer": "%s"}`, p.DB, p.Pointer)
}

// Equal reports whether p and other name the same namespace and identifier.
func (p DBPointer) Equal(other DBPointer) bool {
	//: both fields.
	return p == other
}

// IsZero reports whether p is the empty pointer — what omitempty leaves out.
func (p DBPointer) IsZero() bool {
	//: no namespace and the nil identifier.
	return p.DB == "" && p.Pointer.IsZero()
}

// JavaScript is BSON JavaScript code.
type JavaScript string

// Symbol is the deprecated BSON symbol, read and written as a distinct type so
// a round trip does not turn it into a string.
type Symbol string

// CodeWithScope is the deprecated BSON JavaScript-with-scope: code and the
// document its free variables are bound in. Scope must encode as a document —
// a struct, a map or a D; a decode always produces a D.
type CodeWithScope struct {
	// Code is the JavaScript.
	Code JavaScript
	// Scope is the document.
	Scope any
}

// String renders c for a log line or a test failure.
func (c CodeWithScope) String() string {
	//: a fixed, JSON-looking shape.
	return fmt.Sprintf(`{"code": "%s", "scope": %v}`, c.Code, c.Scope)
}

// Timestamp is the BSON replication timestamp: seconds since the epoch and an
// ordinal within the second. It is not a date; encode a time.Time for one.
type Timestamp struct {
	// T is the seconds since the Unix epoch.
	T uint32
	// I is the increment, ordering operations within the same second.
	I uint32
}

// After reports whether t is later than other.
func (t Timestamp) After(other Timestamp) bool {
	//: seconds first, the increment breaking a tie.
	return t.T > other.T || (t.T == other.T && t.I > other.I)
}

// Before reports whether t is earlier than other.
func (t Timestamp) Before(other Timestamp) bool {
	//: seconds first, the increment breaking a tie.
	return t.T < other.T || (t.T == other.T && t.I < other.I)
}

// Equal reports whether t and other are the same timestamp.
func (t Timestamp) Equal(other Timestamp) bool {
	//: both halves.
	return t == other
}

// IsZero reports whether t is the zero timestamp — what omitempty leaves out.
func (t Timestamp) IsZero() bool {
	//: both halves zero.
	return t.T == 0 && t.I == 0
}

// Compare returns -1 when t is before other, +1 when it is after, 0 when equal.
func (t Timestamp) Compare(other Timestamp) int {
	//: three outcomes.
	switch {
	//: earlier.
	case t.Before(other):
		return -1
	//: later.
	case t.After(other):
		return 1
	}
	//: the same instant and increment.
	return 0
}
