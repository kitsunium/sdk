// Package cbor — the decoder's walk over input the validator has accepted.
//
// Because the item was validated first, every length, count and offset the
// walk reads is already known to fit the input and the bounds, so nothing is
// allocated on the word of an unchecked count. The walk still checks every
// read against the input: an inconsistency is an error, never a panic.
//
// An item the target cannot hold — a string for an int, an integer that
// overflows its field — does not stop the walk: the first such failure is
// kept, the item is skipped, and the rest is decoded, as fxamacker/cbor and
// encoding/json both do.
package cbor

import (
	"cmp"
	"math"
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// decodeStates recycles the state of a decode pass: the plans' functions
// take it by pointer, which would otherwise allocate it on every Unmarshal.
var decodeStates = recycler.NewPool(func() *decodeState {
	//: zeroed, as every state is before it is put back.
	return &decodeState{}
})

// decodeState is one decode pass over a validated data item.
type decodeState struct {
	// data is the validated data item.
	data []byte
	// off is the offset of the next unread head.
	off int
	// first is the first item that could not be stored; nil while none.
	first error
	// failures counts the items that could not be stored, so a caller can
	// tell whether one item it decoded did.
	failures int
	// field names the struct field being filled, for the failure message.
	field string
}

// sequence iterates the entries of an array or a map whose head was read.
type sequence struct {
	// left is how many entries a definite container still holds.
	left uint64
	// indefinite is true when a break ends the container.
	indefinite bool
}

// unmarshalItem decodes the validated data item data into v.
func unmarshalItem(data []byte, v any) error {
	target, err := pointerTarget(v)
	//: Unmarshal writes through a pointer or not at all.
	if err != nil {
		//: refused before the walk.
		return err
	}
	d := decodeStates.Get()
	d.data = data
	walkErr := d.decodeTarget(target)
	err = cmp.Or(walkErr, d.first)
	*d = decodeState{}
	decodeStates.Put(d)
	//: a walk inconsistency first, then the first item not stored.
	return err
}

// decodeTarget decodes the whole item into target, the value Unmarshal's
// pointer points to.
func (d *decodeState) decodeTarget(target reflect.Value) error {
	//: an untyped target needs no plan.
	if untyped, ok := reflect.TypeAssert[*any](target.Addr()); ok {
		x, err := d.decodeAny()
		*untyped = x
		//: built without reflection.
		return err
	}
	plan := decodePlanFor(target.Type())
	//: through the target type's plan.
	return plan.kind.decode(d, target, plan)
}

// pointerTarget returns the value v points to, refusing anything but a
// non-nil pointer.
func pointerTarget(v any) (reflect.Value, error) {
	//: a nil interface has no type to report.
	if v == nil {
		//: refused.
		return reflect.Value{}, decodeCause(nil, "Unmarshal needs a non-nil pointer, got nil")
	}
	rv := reflect.ValueOf(v)
	//: only a non-nil pointer can be written through.
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		//: refused, naming the type.
		return reflect.Value{}, decodeCause(nil, "Unmarshal needs a non-nil pointer, got "+rv.Type().String())
	}
	//: the pointed-to value, settable.
	return rv.Elem(), nil
}

// peek reads the head at the cursor without consuming it.
func (d *decodeState) peek() (itemHead, error) {
	h, ok := readHead(d.data, d.off)
	//: validated input always holds a whole head here.
	if !ok {
		//: never on validated input.
		return h, d.corrupt()
	}
	//: the next head.
	return h, nil
}

// corrupt is the error of the walk finding what validation refuses. It cannot
// happen on validated input; it exists so that a defect is an error and not
// a panic.
func (d *decodeState) corrupt() error {
	//: the offset is where the walk stopped.
	return malformed(d.off, "decoding found input that validation should have refused")
}

// note records an item that could not be stored. Only the first is kept,
// and detail is only built for it.
func (d *decodeState) note(detail func() string) {
	d.failures++
	//: the first failure is the one reported.
	if d.first == nil {
		d.first = decodeCause(nil, detail())
	}
}

// mismatch records that the item at the cursor, whose head is h, cannot be
// stored into a value of type t, and skips it.
func (d *decodeState) mismatch(h itemHead, t reflect.Type) error {
	d.noteMismatch(h, t)
	//: the walk goes on past it.
	return d.skip()
}

// noteMismatch records that an item already consumed, whose head was h,
// could not be stored into a value of type t.
func (d *decodeState) noteMismatch(h itemHead, t reflect.Type) {
	field := d.field
	//: the message names what was found and where it was going.
	d.note(func() string {
		//: types only, never the value.
		return "cannot store " + describeHead(h) + " into " + describeTarget(field, t)
	})
}

// describeTarget names a target type, and the struct field holding it.
func describeTarget(field string, t reflect.Type) string {
	//: a top-level or element target.
	if field == "" {
		//: the type.
		return "a Go value of type " + t.String()
	}
	//: a struct field.
	return "the field " + field + " of type " + t.String()
}

// describeHead names the kind of data item a head opens.
func describeHead(h itemHead) string {
	//: one phrase per major type, split further for major type 7.
	switch h.major {
	case majorUnsigned:
		//: 0.
		return "an unsigned integer"
	case majorNegative:
		//: 1.
		return "a negative integer"
	case majorBytes:
		//: 2.
		return "a byte string"
	case majorText:
		//: 3.
		return "a text string"
	case majorArray, majorMap:
		//: 4 and 5.
		return "an array or a map"
	case majorTag:
		//: 6.
		return "a tag"
	default:
		//: 7.
		return describeSimple(h)
	}
}

// describeSimple names a simple value or a floating-point number.
func describeSimple(h itemHead) string {
	//: booleans, null, floats, and the rest.
	switch {
	case h.info == simpleFalse || h.info == simpleTrue:
		//: true or false.
		return "a boolean"
	case h.info == simpleNull || h.info == simpleUndefined:
		//: null or undefined.
		return "null"
	case h.info >= info2Bytes:
		//: half, single or double.
		return "a floating-point number"
	default:
		//: 0..19 and 32..255, which RFC 8949 leaves unassigned.
		return "an unassigned simple value"
	}
}

// isNull reports whether h is null or undefined, which decode alike.
func isNull(h itemHead) bool {
	//: major type 7, additional information 22 or 23.
	return h.major == majorSimple && (h.info == simpleNull || h.info == simpleUndefined)
}

// take consumes the next n bytes, which alias the input.
func (d *decodeState) take(n uint64) ([]byte, error) {
	//: compared as uint64 so a length beyond int is not truncated first.
	if n > uint64(len(d.data)-d.off) {
		//: never on validated input.
		return nil, d.corrupt()
	}
	content := d.data[d.off : d.off+int(n)]
	d.off += int(n)
	//: the payload.
	return content, nil
}

// stringBytes consumes the byte or text string whose head h is at the cursor
// and returns its content. fresh is false when the content aliases the
// input, true when an indefinite string's chunks were joined into new memory.
func (d *decodeState) stringBytes(h itemHead) (content []byte, fresh bool, err error) {
	d.off += h.size
	//: a definite string is one slice of the input.
	if h.info != infoIndefinite {
		content, err = d.take(h.arg)
		//: aliased.
		return content, false, err
	}
	content, err = d.joinChunks()
	//: joined.
	return content, true, err
}

// joinChunks consumes an indefinite string's chunks and their break, and
// returns them joined.
func (d *decodeState) joinChunks() ([]byte, error) {
	total, err := d.chunksLength()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return nil, err
	}
	joined := make([]byte, 0, total)
	//: chunk after chunk until the break.
	for {
		h, err := d.peek()
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return nil, err
		}
		//: the break ends the string.
		if h.major == majorSimple && h.info == infoIndefinite {
			d.off++
			//: the whole string.
			return joined, nil
		}
		d.off += h.size
		chunk, err := d.take(h.arg)
		//: never on validated input.
		if err != nil {
			//: corrupt.
			return nil, err
		}
		joined = append(joined, chunk...)
	}
}

// chunksLength adds up the lengths of the chunks at the cursor, consuming
// nothing, so the joined string is allocated once.
func (d *decodeState) chunksLength() (int, error) {
	off, total := d.off, 0
	//: chunk after chunk until the break.
	for {
		h, ok := readHead(d.data, off)
		//: never on validated input.
		if !ok {
			//: corrupt.
			return 0, d.corrupt()
		}
		//: the break ends the string.
		if h.major == majorSimple && h.info == infoIndefinite {
			//: the joined length; validation bounded every chunk by the input.
			return total, nil
		}
		total += int(h.arg)
		off += h.size + int(h.arg)
	}
}

// skip consumes one whole data item.
func (d *decodeState) skip() error {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	d.off += h.size
	//: what follows the head depends on the major type.
	switch h.major {
	case majorBytes, majorText:
		//: a payload, or chunks.
		return d.skipString(h)
	case majorArray, majorMap:
		//: entries.
		return d.skipEntries(h)
	case majorTag:
		//: the enclosed item.
		return d.skip()
	default:
		//: an integer or a simple value is its head.
		return nil
	}
}

// skipString consumes a string's payload, or its chunks and break.
func (d *decodeState) skipString(h itemHead) error {
	//: chunks are items, closed by a break.
	if h.info == infoIndefinite {
		//: until the break.
		return d.skipUntilBreak()
	}
	_, err := d.take(h.arg)
	//: the payload.
	return err
}

// skipEntries consumes an array's or a map's entries.
func (d *decodeState) skipEntries(h itemHead) error {
	//: an indefinite container ends at its break.
	if h.info == infoIndefinite {
		//: until the break.
		return d.skipUntilBreak()
	}
	items := h.arg
	//: a map has two items per pair; validation bounded the count.
	if h.major == majorMap {
		items *= 2
	}
	//: one item at a time.
	for range items {
		//: never fails on validated input.
		if err := d.skip(); err != nil {
			//: corrupt.
			return err
		}
	}
	//: skipped.
	return nil
}

// skipUntilBreak consumes items up to and including a break.
func (d *decodeState) skipUntilBreak() error {
	//: item after item.
	for {
		//: never on validated input.
		if d.off >= len(d.data) {
			//: corrupt.
			return d.corrupt()
		}
		//: the break closes the item.
		if d.data[d.off] == breakByte {
			d.off++
			//: done.
			return nil
		}
		//: never fails on validated input.
		if err := d.skip(); err != nil {
			//: corrupt.
			return err
		}
	}
}

// newSequence starts iterating the container whose head h was just read.
func newSequence(h itemHead) sequence {
	//: a count, or a break to look for.
	return sequence{left: h.arg, indefinite: h.info == infoIndefinite}
}

// next reports whether another entry of s follows, consuming the break that
// ends an indefinite container.
func (d *decodeState) next(s *sequence) (bool, error) {
	//: a definite container counts down.
	if !s.indefinite {
		//: exhausted.
		if s.left == 0 {
			//: no more.
			return false, nil
		}
		s.left--
		//: one more.
		return true, nil
	}
	//: never on validated input.
	if d.off >= len(d.data) {
		//: corrupt.
		return false, d.corrupt()
	}
	//: the break ends it.
	if d.data[d.off] == breakByte {
		d.off++
		//: no more.
		return false, nil
	}
	//: one more.
	return true, nil
}

// entryCount is how many entries the container whose head h was just read
// holds: its count, or for an indefinite one the entries before its break,
// counted without consuming them.
func (d *decodeState) entryCount(h itemHead) (int, error) {
	//: a definite count was bounded by validation.
	if h.info != infoIndefinite {
		//: fits an int.
		return int(h.arg), nil
	}
	saved := d.off
	count := 0
	//: skip entries up to the break, then rewind.
	for d.off < len(d.data) && d.data[d.off] != breakByte {
		//: never fails on validated input.
		if err := d.skip(); err != nil {
			//: corrupt.
			return 0, err
		}
		count++
	}
	d.off = saved
	//: a map's entries were counted one per key and one per value.
	if h.major == majorMap {
		count /= 2
	}
	//: the count.
	return count, nil
}

// floatOf returns the value of the float head h.
func floatOf(h itemHead) float64 {
	//: the width is the additional information.
	switch h.info {
	case info2Bytes:
		//: half precision, widened exactly.
		return halfToFloat64(uint16(h.arg))
	case info4Bytes:
		//: single precision, widened exactly.
		return float64(math.Float32frombits(uint32(h.arg)))
	default:
		//: double precision.
		return math.Float64frombits(h.arg)
	}
}
