package bson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Equal reports whether b and other hold the same subtype and bytes.
func (b Binary) Equal(other Binary) bool {
	//: both halves of the value.
	return b.Subtype == other.Subtype && bytes.Equal(b.Data, other.Data)
}

// isZero is Binary.IsZero's body: decl_gen.go writes Binary.IsZero, from the
// design, as one call of it.
func (b Binary) isZero() bool {
	//: the zero subtype and no bytes.
	return b.Subtype == BinaryGeneric && len(b.Data) == 0
}

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

// isZero is Regex.IsZero's body: decl_gen.go writes Regex.IsZero, from the
// design, as one call of it.
func (r Regex) isZero() bool {
	//: no pattern and no options.
	return r.Pattern == "" && r.Options == ""
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

// String renders c for a log line or a test failure.
func (c CodeWithScope) String() string {
	//: a fixed, JSON-looking shape.
	return fmt.Sprintf(`{"code": "%s", "scope": %v}`, c.Code, c.Scope)
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

// equal is Timestamp.Equal's body: decl_gen.go writes Timestamp.Equal, from the
// design, as one call of it.
func (t Timestamp) equal(other Timestamp) bool {
	//: both halves.
	return t == other
}

// isZero is Timestamp.IsZero's body: decl_gen.go writes Timestamp.IsZero, from the
// design, as one call of it.
func (t Timestamp) isZero() bool {
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
