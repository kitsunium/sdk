// Package profiling — hosts the protocol-buffer wire reader the profile
// decoder stands on: varints, length-delimited fields, and the skip of a field
// the decoder does not read. Written from the encoding's specification so the
// SDK carries no protobuf dependency (the same choice as OTLP/JSON, ADR 0048).
package profiling

import "github.com/kitsunium/sdk/internal/kernel/errs"

// Wire types of the protocol-buffer encoding.
const (
	wireVarint  uint64 = 0
	wireFixed64 uint64 = 1
	wireBytes   uint64 = 2
	wireFixed32 uint64 = 5
)

// Sizes the reader skips over for the fixed-width wire types, and the most
// bytes one varint may take.
const (
	fixed64Size int = 8
	fixed32Size int = 4
	maxVarint   int = 10
)

// varintPayload and varintMore split a varint byte: seven bits of value, and
// the bit that says another byte follows.
const (
	varintPayload byte   = 0x7f
	varintMore    byte   = 0x80
	varintShift   uint   = 7
	fieldShift    uint   = 3
	wireMask      uint64 = 7
)

// wire reads one message's bytes, field by field. Every read is bounds-checked:
// a truncated or overlong varint, or a length past the end, is
// [ProfileMalformed] — never a panic and never a read past the buffer.
type wire struct {
	buf []byte
}

// more reports whether fields remain.
func (w *wire) more() bool {
	//: an empty rest is the end of the message.
	return len(w.buf) > 0
}

// varint reads one base-128 varint.
func (w *wire) varint() (uint64, error) {
	var value uint64
	//: at most ten bytes carry the 64 bits.
	for i := range min(len(w.buf), maxVarint) {
		b := w.buf[i]
		value |= uint64(b&varintPayload) << (varintShift * uint(i))
		//: the last byte of the varint.
		if b&varintMore == 0 {
			w.buf = w.buf[i+1:]
			//: complete.
			return value, nil
		}
	}
	//: the buffer ended mid-varint, or the varint ran past ten bytes.
	return 0, malformed("varint")
}

// key reads a field's key: its number and its wire type.
func (w *wire) key() (field, kind uint64, err error) {
	k, err := w.varint()
	//: a key that is not a varint ends the message.
	if err != nil {
		//: already malformed.
		return 0, 0, err
	}
	//: the low three bits are the wire type.
	return k >> fieldShift, k & wireMask, nil
}

// bytes reads a length-delimited field's payload.
func (w *wire) bytes() ([]byte, error) {
	n, err := w.varint()
	//: no length, no payload.
	if err != nil {
		//: already malformed.
		return nil, err
	}
	//: a length past the end of the message is a lie about the input.
	if n > uint64(len(w.buf)) {
		//: never read past the buffer.
		return nil, malformed("length")
	}
	out := w.buf[:n]
	w.buf = w.buf[n:]
	//: a view of the input, not a copy.
	return out, nil
}

// skip passes over one field's payload of wire type kind.
func (w *wire) skip(kind uint64) error {
	//: one rule per wire type the encoding still uses.
	switch kind {
	//: a varint is read and dropped.
	case wireVarint:
		_, err := w.varint()
		//: malformed or not, as the varint says.
		return err
	//: a length-delimited payload is sliced over.
	case wireBytes:
		_, err := w.bytes()
		//: malformed or not, as the length says.
		return err
	//: eight bytes.
	case wireFixed64:
		//: the fixed width, bounds-checked.
		return w.advance(fixed64Size)
	//: four bytes.
	case wireFixed32:
		//: the fixed width, bounds-checked.
		return w.advance(fixed32Size)
	}
	//: groups (3, 4) are long deprecated, and 6, 7 do not exist.
	return malformed("wire type")
}

// advance drops n bytes.
func (w *wire) advance(n int) error {
	//: a fixed-width field past the end.
	if n > len(w.buf) {
		//: never read past the buffer.
		return malformed("fixed")
	}
	w.buf = w.buf[n:]
	//: skipped.
	return nil
}

// varints appends to dst a repeated varint field's values, packed or not: a
// packed field is one length-delimited run of varints, an unpacked one a
// single varint per occurrence. runtime/pprof writes both, depending on the
// count.
func (w *wire) varints(dst []uint64, kind uint64) ([]uint64, error) {
	//: one occurrence, one value.
	if kind == wireVarint {
		v, err := w.varint()
		//: the value, or the malformation.
		return append(dst, v), err
	}
	//: anything but a packed run is not a repeated varint.
	if kind != wireBytes {
		//: the field cannot be read as declared.
		return dst, malformed("repeated")
	}
	run, err := w.bytes()
	//: no run to read.
	if err != nil {
		//: already malformed.
		return dst, err
	}
	packed := wire{buf: run}
	//: every varint of the run.
	for packed.more() {
		v, err := packed.varint()
		//: a run that ends mid-varint.
		if err != nil {
			//: already malformed.
			return dst, err
		}
		dst = append(dst, v)
	}
	//: the whole run.
	return dst, nil
}

// malformed builds [ProfileMalformed] naming what could not be read.
func malformed(what string) error {
	//: the part named as a field; the input is never quoted.
	return errs.Wrap(ProfileMalformed, errs.WrapParams{}, errs.String("reading", what))
}
