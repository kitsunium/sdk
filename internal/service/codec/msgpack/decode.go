// Package msgpack — the decode cursor. A decodeState walks one in-memory
// input and never reads past it: every length the input declares is checked
// against the bytes that remain BEFORE anything is allocated for it, every
// element of an array or map needs at least one byte so a count is checked the
// same way, and containers nest at most maxDepth deep. Hostile input therefore
// fails with UNMARSHAL_FAILED, never with a panic, an unbounded allocation or
// a stack overflow.
package msgpack

import (
	"encoding/binary"
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Pair arithmetic of the map family.
const (
	// mapEntryMinBytes is the fewest bytes one map pair can occupy: a
	// one-byte key and a one-byte value.
	mapEntryMinBytes uint64 = 2
	// valuesPerPair is how many values one map pair holds: a key and a value.
	valuesPerPair uint64 = 2
)

// decodeState is the cursor over one input.
type decodeState struct {
	// data is the whole input.
	data []byte
	// off is the next unread byte.
	off int
	// depth counts the containers currently open.
	depth int
}

// truncated is the failure for an input that ends inside a value.
func (d *decodeState) truncated() error {
	//: name where the input ran out.
	return unmarshalFault("input ends inside a value", errs.Int(fieldOffset, d.off))
}

// remaining is how many bytes are left to read.
func (d *decodeState) remaining() uint64 {
	//: never negative: off only advances within data.
	return uint64(len(d.data) - d.off)
}

// readHeader reads one header: the format byte and, when the family has one,
// its length or value field (and an extension's type byte).
func (d *decodeState) readHeader() (header, error) {
	//: an exhausted input cannot start a value.
	if d.off >= len(d.data) {
		//: the value the caller expects is missing.
		return header{}, d.truncated()
	}
	//: one indexed load classifies the byte.
	info := &headerTable[d.data[d.off]]
	d.off++
	h := header{fam: info.fam, width: info.width, arg: info.inline}
	//: 0xc1 is assigned to nothing.
	if info.fam == famInvalid {
		//: name the offset of the offending byte.
		return h, unmarshalFault("header byte 0xc1 is never used", errs.Int(fieldOffset, d.off-1))
	}
	//: a family with a separate field reads it now.
	if info.width != 0 {
		//: the field is big-endian, 1–8 bytes.
		raw, err := d.readField(info.width)
		if err != nil {
			//: the input ended inside the header.
			return h, err
		}
		h.arg = widen(raw, info)
	}
	//: an extension's type byte follows its length.
	if info.fam == famExt {
		//: read the type.
		return d.readExtType(h)
	}
	//: complete header.
	return h, nil
}

// readField reads a big-endian field of width bytes.
func (d *decodeState) readField(width uint8) (uint64, error) {
	n := int(width)
	//: the whole field must be present.
	if len(d.data)-d.off < n {
		//: the input ended inside the header.
		return 0, d.truncated()
	}
	p := d.data[d.off : d.off+n]
	d.off += n
	//: big-endian, one load.
	return beUint(p), nil
}

// beUint reads a big-endian unsigned field of 1, 2, 4 or 8 bytes.
func beUint(p []byte) uint64 {
	//: one load per width.
	switch len(p) {
	//: one byte.
	case int(width8):
		return uint64(p[0])
	//: two bytes.
	case int(width16):
		return uint64(binary.BigEndian.Uint16(p))
	//: four bytes.
	case int(width32):
		return uint64(binary.BigEndian.Uint32(p))
	//: eight bytes.
	default:
		return binary.BigEndian.Uint64(p)
	}
}

// widen turns a field's raw bits into the header's argument: a signed field is
// sign-extended to 64 bits, every other field is already its value.
func widen(raw uint64, info *headerInfo) uint64 {
	//: only int 8–64 carry a sign.
	if info.fam != famInt {
		//: lengths, counts, unsigned values and float bits are as read.
		return raw
	}
	//: sign-extend from the field's width.
	switch info.width {
	//: int 8.
	case width8:
		return uint64(int64(int8(raw)))
	//: int 16.
	case width16:
		return uint64(int64(int16(raw)))
	//: int 32.
	case width32:
		return uint64(int64(int32(raw)))
	//: int 64 is already 64 bits.
	default:
		return raw
	}
}

// readExtType completes an extension header with its type byte.
func (d *decodeState) readExtType(h header) (header, error) {
	//: the type byte must be present.
	if d.off >= len(d.data) {
		//: the input ended inside the header.
		return h, d.truncated()
	}
	h.ext = int8(d.data[d.off])
	d.off++
	//: the payload is read by the caller.
	return h, nil
}

// take returns the next n bytes, refusing a length the input cannot satisfy.
// The slice aliases the input; a caller that keeps it copies it.
func (d *decodeState) take(n uint64) ([]byte, error) {
	//: a declared length longer than what is left is a lie or a truncation.
	if n > d.remaining() {
		//: refuse before anything is allocated for it.
		return nil, unmarshalFault("declared length exceeds the remaining input",
			errs.Int(fieldOffset, d.off), errs.Int64(fieldLen, int64(min(n, math.MaxInt64))))
	}
	p := d.data[d.off : d.off+int(n)]
	d.off += int(n)
	//: the payload, aliasing the input.
	return p, nil
}

// checkCount refuses an array or map count the remaining input cannot hold,
// before anything is allocated for it: an element needs at least one byte, a
// map pair at least two.
func (d *decodeState) checkCount(h header) error {
	need := h.arg
	//: a map's count is in pairs.
	if h.fam == famMap {
		//: two bytes per pair at least; a count is at most 2³²−1, so no overflow.
		need *= mapEntryMinBytes
	}
	//: a count the input cannot hold is refused now, not after allocating.
	if need > d.remaining() {
		//: name the offset and the declared count.
		return unmarshalFault("declared element count exceeds the remaining input",
			errs.Int(fieldOffset, d.off), errs.Int64(fieldLen, int64(h.arg)))
	}
	//: plausible.
	return nil
}

// enter opens one container level, refusing input nested past maxDepth.
func (d *decodeState) enter() error {
	d.depth++
	//: refuse before the recursion grows the stack further.
	if d.depth > maxDepth {
		//: hostile or broken input.
		return depthExceeded(false)
	}
	//: within bounds.
	return nil
}

// leave closes one container level.
func (d *decodeState) leave() {
	//: paired with every successful enter.
	d.depth--
}

// peekNil reports whether the next value is nil, without consuming it.
func (d *decodeState) peekNil() bool {
	//: an exhausted input has no next value.
	return d.off < len(d.data) && d.data[d.off] == codeNil
}

// skip consumes one complete value without materialising it. It is
// iterative — a count of values still owed, not recursion — so even the
// deepest input costs no stack, and every count is checked against what
// remains so the tally stays bounded by the input's length.
func (d *decodeState) skip() error {
	//: one value is owed.
	for owed := uint64(1); owed > 0; owed-- {
		h, err := d.readHeader()
		if err != nil {
			//: malformed or truncated.
			return err
		}
		//: containers owe their elements; payloads are stepped over.
		owed, err = d.skipBody(h, owed)
		if err != nil {
			//: the body is not there.
			return err
		}
	}
	//: the value ended where the cursor is.
	return nil
}

// skipBody steps over what follows a header and returns the updated count of
// values still owed.
func (d *decodeState) skipBody(h header, owed uint64) (uint64, error) {
	//: only payload families and containers have a body.
	switch h.fam {
	//: str, bin and ext: step over the payload.
	case famStr, famBin, famExt:
		_, err := d.take(h.arg)
		return owed, err
	//: arrays and maps owe their elements.
	case famArray, famMap:
		//: a count the input cannot hold is refused now.
		if err := d.checkCount(h); err != nil {
			//: implausible count.
			return owed, err
		}
		//: an array owes its elements, a map a key and a value per pair.
		if h.fam == famMap {
			//: both halves of each pair.
			return owed + h.arg*valuesPerPair, nil
		}
		return owed + h.arg, nil
	//: nil, booleans and numbers are complete after their header.
	default:
		return owed, nil
	}
}
