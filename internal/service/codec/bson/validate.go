// Package bson — the structural validator. Unmarshal runs it over the whole
// input before the target is touched, so a malformed document never leaves a
// half-decoded value behind, and the decoder that follows reads bytes whose
// every length, terminator, type byte and string has been checked. Every
// declared length is compared with the bytes actually remaining before
// anything is sliced or allocated from it.
package bson

import (
	"strconv"
	"unicode/utf8"
)

// validationError says where, and why, a document is malformed. It is turned
// into BSON_UNMARSHAL_FAILED or BSON_DEPTH_EXCEEDED by validateRoot; its text
// names an offset and a rule, never the bytes found there.
type validationError struct {
	// offset is where in the input the problem was found.
	offset int
	// rule is what the bytes there break.
	rule string
	// depth reports the nesting bound rather than a malformation.
	depth bool
}

// validateRoot checks that data is exactly one well-formed document.
func validateRoot(data []byte) error {
	v := validator{}
	//: the declared length must be the input's, and the document well formed.
	if problem := v.root(data); problem != nil {
		//: the nesting bound has its own code.
		if problem.depth {
			//: refused before any recursion past the bound.
			return depthError("Unmarshal")
		}
		//: every other malformation.
		return unmarshalError(nil, "malformed document at byte "+strconv.Itoa(problem.offset)+": "+problem.rule)
	}
	//: well formed.
	return nil
}

// validator walks one input, tracking the absolute offset for messages.
type validator struct{}

// root checks the top-level document, whose declared length must be exactly
// the length of the input: trailing bytes are refused.
func (v validator) root(data []byte) *validationError {
	//: the length prefix and the terminator must both be there.
	if len(data) < minDocumentSize {
		//: too short to be a document.
		return &validationError{offset: 0, rule: "shorter than the smallest document (5 bytes)"}
	}
	//: the document claims the whole input, no more and no less.
	if int64(readInt32(data)) != int64(len(data)) {
		//: a truncated document, or bytes after it.
		return &validationError{offset: 0, rule: "the declared length is not the input length"}
	}
	//: the top level is the first nesting level.
	return v.document(data, 0, 1)
}

// document checks a document or array occupying exactly doc, found at base in
// the input and nested at depth.
func (v validator) document(doc []byte, base, depth int) *validationError {
	//: the bound is checked before anything nested is read.
	if depth > maxBSONNestedLevels {
		//: too deep.
		return &validationError{offset: base, depth: true}
	}
	end := len(doc) - 1
	//: the last byte terminates the element list.
	if doc[end] != 0 {
		//: the element list leaks past its length, or the length is short.
		return &validationError{offset: base + end, rule: "a document does not end with its terminator"}
	}
	pos := lengthSize
	//: element by element, up to the terminator.
	for pos < end {
		next, problem := v.element(doc[:end], pos, base, depth)
		//: the first malformation stops the walk.
		if problem != nil {
			//: reported.
			return problem
		}
		pos = next
	}
	//: every byte accounted for.
	return nil
}

// element checks the element at body[pos:], body being a document's element
// list without its terminator, and returns where the next element starts.
func (v validator) element(body []byte, pos, base, depth int) (int, *validationError) {
	t := body[pos]
	pos++
	keyEnd := cstringEnd(body[pos:])
	//: the element name must be terminated inside the document.
	if keyEnd < 0 {
		//: the name runs into the terminator.
		return 0, &validationError{offset: base + pos, rule: "an element name is not terminated"}
	}
	//: the element name is UTF-8.
	if !utf8.Valid(body[pos : pos+keyEnd]) {
		//: not text.
		return 0, &validationError{offset: base + pos, rule: "an element name is not UTF-8"}
	}
	pos += keyEnd + 1
	size, problem := v.value(t, body[pos:], base+pos, depth)
	//: the value is malformed.
	if problem != nil {
		//: reported.
		return 0, problem
	}
	//: the next element.
	return pos + size, nil
}

// value checks the value of an element of type t at the start of b, found at
// base in the input, and returns its size.
func (v validator) value(t byte, b []byte, base, depth int) (int, *validationError) {
	//: one rule set per type.
	switch t {
	//: a string-shaped value.
	case typeString, typeJavaScript, typeSymbol:
		return v.str(b, base)
	//: a nested document.
	case typeDocument, typeArray:
		return v.nested(b, base, depth)
	//: the other variable-width types.
	case typeBinary:
		return v.binary(b, base)
	case typeRegex:
		return v.regex(b, base)
	case typeDBPointer:
		return v.dbPointer(b, base)
	case typeCodeWithScope:
		return v.codeWithScope(b, base, depth)
	case typeBoolean:
		return v.boolean(b, base)
	}
	size, fixed := fixedValueSize(t)
	//: not a BSON 1.1 element type.
	if !fixed {
		//: refused rather than skipped.
		return 0, &validationError{offset: base - 1, rule: "an element has an unknown type byte"}
	}
	//: the fixed-width payload must be there.
	if size > len(b) {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a fixed-width value is truncated"}
	}
	//: the width of the type.
	return size, nil
}

// str checks an int32-length-prefixed, NUL-terminated UTF-8 string.
func (v validator) str(b []byte, base int) (int, *validationError) {
	//: the prefix must be there.
	if len(b) < lengthSize {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a string length is truncated"}
	}
	declared := readInt32(b)
	//: the length counts the NUL, so it is at least one, and fits what is left.
	if declared < 1 || int64(declared) > int64(len(b)-lengthSize) {
		//: a negative, zero or overlong length.
		return 0, &validationError{offset: base, rule: "a string length is out of range"}
	}
	end := lengthSize + int(declared) - 1
	//: the last byte the length covers is the NUL.
	if b[end] != 0 {
		//: not terminated where the length says.
		return 0, &validationError{offset: base + end, rule: "a string is not terminated where its length ends"}
	}
	//: the text is UTF-8; an embedded NUL is allowed, as the specification
	//: allows it.
	if !utf8.Valid(b[lengthSize:end]) {
		//: not text.
		return 0, &validationError{offset: base + lengthSize, rule: "a string is not UTF-8"}
	}
	//: the prefix, the text and the NUL.
	return end + 1, nil
}

// nested checks an embedded document or array, which the element types
// distinguish but whose layout is the same: an array's keys are not checked
// to be "0", "1", …, as the specification asks readers to accept either.
func (v validator) nested(b []byte, base, depth int) (int, *validationError) {
	//: the prefix must be there.
	if len(b) < lengthSize {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a document length is truncated"}
	}
	declared := readInt32(b)
	//: at least the smallest document, at most what is left.
	if declared < int32(minDocumentSize) || int64(declared) > int64(len(b)) {
		//: out of range.
		return 0, &validationError{offset: base, rule: "a document length is out of range"}
	}
	//: one level deeper.
	if problem := v.document(b[:declared], base, depth+1); problem != nil {
		//: reported.
		return 0, problem
	}
	//: the whole document.
	return int(declared), nil
}

// binary checks a binary: a non-negative int32 length, a subtype byte, and the
// bytes; the deprecated subtype 0x02 must carry its own inner length, equal
// to the outer one minus four.
func (v validator) binary(b []byte, base int) (int, *validationError) {
	//: the prefix and the subtype must be there.
	if len(b) < binaryHeaderSize {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a binary header is truncated"}
	}
	declared := readInt32(b)
	//: non-negative, and within what is left after the header.
	if declared < 0 || int64(declared) > int64(len(b)-binaryHeaderSize) {
		//: out of range.
		return 0, &validationError{offset: base, rule: "a binary length is out of range"}
	}
	//: the old generic subtype nests a second length.
	if b[lengthSize] == BinaryOld {
		//: the inner length must be there and agree with the outer one.
		if declared < int32(lengthSize) || readInt32(b[binaryHeaderSize:]) != declared-int32(lengthSize) {
			//: inconsistent.
			return 0, &validationError{offset: base + binaryHeaderSize, rule: "a subtype 0x02 binary's inner length disagrees with its length"}
		}
	}
	//: the header and the bytes.
	return binaryHeaderSize + int(declared), nil
}

// regex checks two NUL-terminated UTF-8 cstrings: pattern, then options.
func (v validator) regex(b []byte, base int) (int, *validationError) {
	size := 0
	//: the pattern, then the options.
	for range 2 {
		n := cstringEnd(b[size:])
		//: terminated inside the document.
		if n < 0 {
			//: runs into the terminator.
			return 0, &validationError{offset: base + size, rule: "a regex is not terminated"}
		}
		//: text.
		if !utf8.Valid(b[size : size+n]) {
			//: not text.
			return 0, &validationError{offset: base + size, rule: "a regex is not UTF-8"}
		}
		size += n + 1
	}
	//: both cstrings.
	return size, nil
}

// dbPointer checks a DBPointer: a string, then twelve bytes.
func (v validator) dbPointer(b []byte, base int) (int, *validationError) {
	size, problem := v.str(b, base)
	//: the namespace.
	if problem != nil {
		//: reported.
		return 0, problem
	}
	//: the ObjectID after it.
	if len(b)-size < objectIDSize {
		//: truncated.
		return 0, &validationError{offset: base + size, rule: "a DBPointer's ObjectID is truncated"}
	}
	//: both parts.
	return size + objectIDSize, nil
}

// codeWithScope checks a code-with-scope: an int32 total length that the code
// string and the scope document fill exactly.
func (v validator) codeWithScope(b []byte, base, depth int) (int, *validationError) {
	//: the prefix must be there.
	if len(b) < lengthSize {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a code-with-scope length is truncated"}
	}
	declared := readInt32(b)
	//: at least the smallest code and scope, at most what is left.
	if declared < int32(minCodeWithScopeSize) || int64(declared) > int64(len(b)) {
		//: out of range.
		return 0, &validationError{offset: base, rule: "a code-with-scope length is out of range"}
	}
	value := b[:declared]
	code, problem := v.str(value[lengthSize:], base+lengthSize)
	//: the code string, which must leave room for the scope.
	if problem != nil {
		//: reported.
		return 0, problem
	}
	scopeAt := lengthSize + code
	scope, problem := v.nested(value[scopeAt:], base+scopeAt, depth)
	//: the scope document.
	if problem != nil {
		//: reported.
		return 0, problem
	}
	//: the two parts fill the declared length exactly.
	if scopeAt+scope != int(declared) {
		//: bytes left over inside the value.
		return 0, &validationError{offset: base, rule: "a code-with-scope length disagrees with its parts"}
	}
	//: the whole value.
	return int(declared), nil
}

// boolean checks a boolean: one byte, 0x00 or 0x01.
func (v validator) boolean(b []byte, base int) (int, *validationError) {
	//: the byte must be there.
	if len(b) < 1 {
		//: truncated.
		return 0, &validationError{offset: base, rule: "a boolean is truncated"}
	}
	//: any other byte is not a boolean.
	if b[0] > 1 {
		//: refused.
		return 0, &validationError{offset: base, rule: "a boolean is neither 0x00 nor 0x01"}
	}
	//: one byte.
	return 1, nil
}
