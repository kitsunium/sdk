// Package entitlement - the Roughtime tagged-message encoding.
//
// Split from roughtime.go because it is pure parsing of untrusted bytes and
// nothing else: every function here reads a length or an offset that a hostile
// server chose, so every one of them bounds-checks before it indexes. Keeping
// that apart from the signature checks makes it possible to read either half
// without holding the other in your head.
package entitlement

import (
	"encoding/binary"
	"fmt"
)

// roughtimeMaxTags caps how many fields one message may declare.
//
// The count is the FIRST four bytes a hostile server controls, and it sizes
// every allocation below it. Roughtime messages carry a handful of tags; this
// is far above any real one and far below a number worth allocating for.
const roughtimeMaxTags int = 32

// roughtimeTagSize is the width of a tag, an offset and a count alike.
const roughtimeTagSize int = 4

// roughtimeTag renders a four-character tag the way the wire format compares
// them: as a little-endian uint32 over the ASCII bytes.
//
// Short names are NUL-padded on the right, which is how "VER" and "SIG" travel.
// Tags longer than four characters are a programming error rather than input,
// so they truncate rather than grow a return path nobody can act on.
func roughtimeTag(name string) uint32 {
	var raw [roughtimeTagSize]byte
	copy(raw[:], name)
	//: Little-endian over the ASCII bytes IS the ordering the format sorts by.
	return binary.LittleEndian.Uint32(raw[:])
}

// decodeRoughtimeMessage splits a tagged message into its fields.
//
// Every length and offset in here was chosen by the server, so each one is
// checked against the buffer that actually arrived before it is used to slice
// it. The format makes that easy to get wrong: the offset table describes
// values 1..N-1 and the first value's offset is implicit, so an off-by-one
// reads a field's worth of somebody else's memory.
func decodeRoughtimeMessage(raw []byte) (fields map[uint32][]byte, err error) {
	count, header, countErr := roughtimeHeaderSize(raw)
	//: A header we cannot trust sizes nothing.
	if countErr != nil {
		//: Propagate the malformed message.
		return nil, countErr
	}

	values := raw[header:]
	decoded := make(map[uint32][]byte, count)
	//: Walk the tags in order, deriving each value's extent from the NEXT
	//: offset — the last one runs to the end of the buffer.
	for index := range count {
		start, end, boundsErr := roughtimeValueBounds(raw, count, index, len(values))
		//: An offset table that does not describe this buffer is unusable.
		if boundsErr != nil {
			//: Propagate the malformed message.
			return nil, boundsErr
		}
		tagAt := roughtimeTagSize*count - roughtimeTagSize + roughtimeTagSize*index
		tag := binary.LittleEndian.Uint32(raw[roughtimeTagSize+tagAt:])
		decoded[tag] = values[start:end]
	}
	//: A decoded message, still entirely untrusted.
	return decoded, nil
}

// roughtimeHeaderSize validates the tag count and returns it with the byte
// length of the header that precedes the values.
func roughtimeHeaderSize(raw []byte) (count, header int, err error) {
	//: Without the count there is no message at all.
	if len(raw) < roughtimeTagSize {
		//: Report the malformed message.
		return 0, 0, fmt.Errorf("%w: roughtime message shorter than its own header", ErrCIUnverifiable)
	}
	count = int(binary.LittleEndian.Uint32(raw[:roughtimeTagSize]))
	//: Zero tags is not a message, and a large count is an allocation the
	//: server would be choosing for us.
	if count <= 0 || count > roughtimeMaxTags {
		//: Report the malformed message.
		return 0, 0, fmt.Errorf("%w: roughtime message declares %d tags", ErrCIUnverifiable, count)
	}

	//: count-1 offsets and count tags follow the count itself.
	header = roughtimeTagSize + roughtimeTagSize*(count-1) + roughtimeTagSize*count
	//: A header longer than the packet describes nothing.
	if len(raw) < header {
		//: Report the malformed message.
		return 0, 0, fmt.Errorf("%w: roughtime header needs %d bytes, got %d", ErrCIUnverifiable, header, len(raw))
	}
	//: A usable frame.
	return count, header, nil
}

// roughtimeValueBounds resolves one field's extent inside the values section.
//
// The first value always starts at zero — its offset is implicit and absent
// from the table — and every other start is read from the entry BEFORE it.
// Both ends are checked against the section and against each other, so a
// descending or out-of-range table is refused rather than sliced with.
func roughtimeValueBounds(raw []byte, count, index, valuesLen int) (start, end int, err error) {
	start = 0
	//: Only the first value has an implicit offset.
	if index > 0 {
		start = int(binary.LittleEndian.Uint32(raw[roughtimeTagSize*index:]))
	}
	end = valuesLen
	//: Every value but the last ends where the next one begins.
	if index+1 < count {
		end = int(binary.LittleEndian.Uint32(raw[roughtimeTagSize*(index+1):]))
	}
	//: Descending, negative or past-the-end offsets describe no value.
	if start < 0 || end < start || end > valuesLen {
		//: Report the malformed message.
		return 0, 0, fmt.Errorf("%w: roughtime offsets [%d,%d) do not fit %d bytes", ErrCIUnverifiable, start, end, valuesLen)
	}
	//: A field that lies inside the buffer it came in.
	return start, end, nil
}

// roughtimeField is one tag and its value, for encoding.
type roughtimeField struct {
	// tag is the little-endian rendering of the four-character name.
	tag uint32
	// value is the field's bytes, which the format requires to be a multiple
	// of four bytes long.
	value []byte
}

// encodeRoughtimeMessage builds a tagged message from fields already in
// ascending tag order.
//
// It does not sort: the caller builds the one request this package ever sends,
// so an unsorted list is a programming error that a test catches, not input to
// be tolerated at runtime.
func encodeRoughtimeMessage(fields []roughtimeField) []byte {
	count := len(fields)
	out := binary.LittleEndian.AppendUint32(nil, uint32(count))

	offset := uint32(0)
	//: The table describes values 1..N-1; the first value's offset is
	//: implicit, which is why this stops one short.
	for _, field := range fields[:max(count-1, 0)] {
		offset += uint32(len(field.value))
		out = binary.LittleEndian.AppendUint32(out, offset)
	}
	//: Every tag, then every value, in the same order.
	for _, field := range fields {
		out = binary.LittleEndian.AppendUint32(out, field.tag)
	}
	for _, field := range fields {
		out = append(out, field.value...)
	}
	//: A message the framing layer can wrap.
	return out
}

// roughtimeUint64 reads a little-endian uint64 field, refusing a short one.
func roughtimeUint64(value []byte, what string) (parsed uint64, err error) {
	//: A field too short to hold the number it claims to be is not one.
	if len(value) < 8 {
		//: Report the malformed field.
		return 0, fmt.Errorf("%w: roughtime %s is %d bytes, want 8", ErrCIUnverifiable, what, len(value))
	}
	//: The value, still unjudged.
	return binary.LittleEndian.Uint64(value[:8]), nil
}

// roughtimeUint32 reads a little-endian uint32 field, refusing a short one.
func roughtimeUint32(value []byte, what string) (parsed uint32, err error) {
	//: A field too short to hold the number it claims to be is not one.
	if len(value) < roughtimeTagSize {
		//: Report the malformed field.
		return 0, fmt.Errorf("%w: roughtime %s is %d bytes, want 4", ErrCIUnverifiable, what, len(value))
	}
	//: The value, still unjudged.
	return binary.LittleEndian.Uint32(value[:roughtimeTagSize]), nil
}
