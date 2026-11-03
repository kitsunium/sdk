package jsonschema

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math/big"
)

// kind is a JSON value's kind.
type kind uint8

// The six JSON kinds.
const (
	// kindNull is null.
	kindNull kind = iota
	// kindBool is true or false.
	kindBool
	// kindNumber is a number, held exactly.
	kindNumber
	// kindString is a string.
	kindString
	// kindArray is an array.
	kindArray
	// kindObject is an object.
	kindObject
)

// value is a JSON value read exactly: numbers as rationals, so 1 and 1.0 are
// one number and multipleOf 0.01 is exact, objects with their member order.
type value struct {
	// kind is which of the fields below holds the value.
	kind kind
	// b is a boolean's value.
	b bool
	// n is a number's value.
	n *big.Rat
	// s is a string's value.
	s string
	// arr is an array's elements.
	arr []*value
	// keys are an object's member names, in document order.
	keys []string
	// obj is an object's members by name.
	obj map[string]*value
}

// tokenReader is the part of jsontext.Decoder parseValue reads with.
type tokenReader interface {
	// PeekKind is the kind of the next token, without reading it.
	PeekKind() jsontext.Kind
	// ReadToken reads the next token.
	ReadToken() (jsontext.Token, error)
	// ReadValue reads the next value whole.
	ReadValue() (jsontext.Value, error)
}

// parse reads exactly one JSON value from data, strictly: jsontext's defaults
// refuse duplicate names, invalid UTF-8 and malformed input.
func parse(data []byte) (*value, error) {
	//: one decoder for the document.
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	//: nothing but whitespace after it: jsontext's own error, or a token.
	if _, err = dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, notOneValue(err)
	}
	return v, nil
}

// parseValue reads the next value of dec.
func parseValue(dec tokenReader) (*value, error) {
	//: by kind.
	switch dec.PeekKind() {
	//: members in order.
	case '{':
		return parseObject(dec)
	//: elements in order.
	case '[':
		return parseArray(dec)
	//: the literal, exactly.
	case '0':
		raw, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		n, ok := new(big.Rat).SetString(string(raw))
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		return &value{kind: kindNumber, n: n}, nil
	//: a string or a literal.
	default:
		return parseScalar(dec)
	}
}

// parseScalar reads a string, true, false or null.
func parseScalar(dec tokenReader) (*value, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	//: by kind.
	switch tok.Kind() {
	//: the unescaped string.
	case '"':
		return &value{kind: kindString, s: tok.String()}, nil
	//: true.
	case 't':
		return &value{kind: kindBool, b: true}, nil
	//: false.
	case 'f':
		return &value{kind: kindBool}, nil
	//: null.
	case 'n':
		return &value{kind: kindNull}, nil
	//: a delimiter where a value was expected.
	default:
		return nil, io.ErrUnexpectedEOF
	}
}

// parseArray reads an array.
func parseArray(dec tokenReader) (*value, error) {
	//: the opening bracket.
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	v := &value{kind: kindArray}
	for dec.PeekKind() != ']' {
		elem, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		v.arr = append(v.arr, elem)
	}
	//: the closing bracket.
	_, err := dec.ReadToken()
	return v, err
}

// parseObject reads an object.
func parseObject(dec tokenReader) (*value, error) {
	//: the opening brace.
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	v := &value{kind: kindObject, obj: map[string]*value{}}
	for dec.PeekKind() != '}' {
		//: the name, copied before the next read voids the token.
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		name := tok.String()
		member, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		v.keys = append(v.keys, name)
		v.obj[name] = member
	}
	//: the closing brace.
	_, err := dec.ReadToken()
	return v, err
}

// equal is JSON equality: numbers by value, arrays element by element,
// objects member by member whatever their order.
func equal(a, b *value) bool {
	//: different kinds are never equal.
	if a.kind != b.kind {
		return false
	}
	//: by kind.
	switch a.kind {
	//: null equals null.
	case kindNull:
		return true
	//: same truth.
	case kindBool:
		return a.b == b.b
	//: same number.
	case kindNumber:
		return a.n.Cmp(b.n) == 0
	//: same characters.
	case kindString:
		return a.s == b.s
	//: same elements in order.
	case kindArray:
		return equalArrays(a.arr, b.arr)
	//: same members.
	default:
		return equalObjects(a, b)
	}
}

// equalArrays compares two arrays element by element.
func equalArrays(a, b []*value) bool {
	//: same length, then each element.
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// equalObjects compares two objects member by member.
func equalObjects(a, b *value) bool {
	//: same member count, then each member of a in b.
	if len(a.obj) != len(b.obj) {
		return false
	}
	for name, av := range a.obj {
		bv, ok := b.obj[name]
		if !ok || !equal(av, bv) {
			return false
		}
	}
	return true
}

// isInteger reports whether v is a number with no fractional part: 1.0 is.
func (v *value) isInteger() bool {
	//: a number whose denominator is one.
	return v.kind == kindNumber && v.n.IsInt()
}

// notOneValue is the cause of a document with something after its value:
// the reader's own error, or io.ErrUnexpectedEOF for a second value.
func notOneValue(err error) error {
	//: the reader's error says where.
	if err != nil {
		return err
	}
	//: a second value read cleanly.
	return io.ErrUnexpectedEOF
}
