package bson

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// objectIDHexLen is the length of an ObjectID's hexadecimal form.
const objectIDHexLen int = 2 * objectIDSize

// NilObjectID is the zero ObjectID.
var NilObjectID ObjectID

// ObjectIDFromHex parses the 24-digit hexadecimal form of an ObjectID, in
// either case. Anything else is refused with BSON_VALUE_INVALID.
func ObjectIDFromHex(s string) (ObjectID, error) {
	var id ObjectID
	//: the length is checked first, so Decode cannot write past id.
	if len(s) != objectIDHexLen {
		//: not the 24-digit form.
		return NilObjectID, valueError(nil, "ObjectIDFromHex: the input is not 24 hexadecimal digits")
	}
	//: decode straight into the array.
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		//: a non-hexadecimal digit.
		return NilObjectID, valueError(err, "ObjectIDFromHex: the input is not 24 hexadecimal digits")
	}
	//: parsed.
	return id, nil
}

// hex is ObjectID.Hex's body: decl_gen.go writes ObjectID.Hex, from the
// design, as one call of it.
func (id ObjectID) hex() string {
	var buf [objectIDHexLen]byte
	hex.Encode(buf[:], id[:])
	//: one allocation, the string itself.
	return string(buf[:])
}

// String renders id as ObjectID("…") for a log line or a test failure.
func (id ObjectID) String() string {
	//: the hexadecimal form, quoted inside the constructor-like wrapper.
	return `ObjectID("` + id.Hex() + `")`
}

// isZero is ObjectID.IsZero's body: decl_gen.go writes ObjectID.IsZero, from the
// design, as one call of it.
func (id ObjectID) isZero() bool {
	//: arrays compare by value.
	return id == NilObjectID
}

// Timestamp returns the creation time id carries, to the second, in UTC.
func (id ObjectID) Timestamp() time.Time {
	//: the first four bytes, big-endian seconds.
	return time.Unix(int64(binary.BigEndian.Uint32(id[0:4])), 0).UTC()
}

// MarshalText writes the hexadecimal form, so an ObjectID can key a map that
// encoding/json writes.
func (id ObjectID) MarshalText() ([]byte, error) {
	var buf [objectIDHexLen]byte
	hex.Encode(buf[:], id[:])
	//: a slice the caller owns.
	return slices.Clone(buf[:]), nil
}

// UnmarshalText reads the hexadecimal form MarshalText writes.
func (id *ObjectID) UnmarshalText(text []byte) error {
	parsed, err := ObjectIDFromHex(string(text))
	//: the parser's refusal, unchanged.
	if err != nil {
		//: id is left as it was.
		return err
	}
	*id = parsed
	//: set.
	return nil
}

// MarshalJSON writes the hexadecimal form as a JSON string.
func (id ObjectID) MarshalJSON() ([]byte, error) {
	var buf [objectIDHexLen + 2]byte
	buf[0] = '"'
	hex.Encode(buf[1:objectIDHexLen+1], id[:])
	buf[objectIDHexLen+1] = '"'
	//: a slice the caller owns.
	return slices.Clone(buf[:]), nil
}

// UnmarshalJSON reads a JSON string holding the hexadecimal form, or the
// extended-JSON object {"$oid": "…"}. The empty string reads as NilObjectID,
// and a JSON null leaves id unchanged.
func (id *ObjectID) UnmarshalJSON(data []byte) error {
	//: null is "no value".
	if string(data) == "null" {
		//: nothing to set.
		return nil
	}
	text, err := objectIDJSONText(data)
	//: neither accepted shape.
	if err != nil {
		//: id is left as it was.
		return err
	}
	//: the empty string is the nil identifier.
	if text == "" {
		*id = NilObjectID
		//: set.
		return nil
	}
	//: the 24-digit form.
	return id.UnmarshalText([]byte(text))
}

// objectIDJSONText extracts the hexadecimal text from a JSON string or an
// extended-JSON {"$oid": "…"} object.
func objectIDJSONText(data []byte) (string, error) {
	var text string
	//: the plain string form.
	if err := json.Unmarshal(data, &text); err == nil {
		//: found.
		return text, nil
	}
	var wrapped struct {
		// OID is the extended-JSON member.
		OID *string `json:"$oid"`
	}
	//: the extended-JSON form.
	if err := json.Unmarshal(data, &wrapped); err != nil || wrapped.OID == nil {
		//: neither shape.
		return "", valueError(err, `ObjectID.UnmarshalJSON: the input is neither a JSON string nor {"$oid": string}`)
	}
	//: found.
	return *wrapped.OID, nil
}
