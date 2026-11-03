package cbor

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The IEEE 754 single-precision layout a float is narrowed through.
const (
	// singleExponentBias is the bias of a single's eight exponent bits.
	singleExponentBias int = 127
	// singleMantissaBits is the width of a single's mantissa.
	singleMantissaBits uint = 23
	// singleExponentMask selects the exponent once shifted down.
	singleExponentMask uint32 = 0xff
	// singleMantissaMask selects a single's mantissa.
	singleMantissaMask uint32 = 0x7fffff
	// singleHiddenBit is the leading 1 of a normal single's significand.
	singleHiddenBit uint32 = 1 << singleMantissaBits
	// singleMagnitudeMask clears a single's sign bit.
	singleMagnitudeMask uint32 = 0x7fffffff
	// singleToHalfDrop is how many low mantissa bits a half has not.
	singleToHalfDrop uint = singleMantissaBits - halfMantissaBits
	// singleSignShift moves a single's sign bit to a half's.
	singleSignShift uint = 16
	// halfMinNormalExponent and halfMaxExponent bound a normal half.
	halfMinNormalExponent int = -14
	// halfMaxExponent is the largest exponent of a finite half.
	halfMaxExponent int = 15
)

// deterministicCodec is NewDeterministic's instance: the registered codec,
// writing the shortest floats and reading only core deterministic encoding.
type deterministicCodec struct {
	cborCodec
}

// NewDeterministic returns a fresh CBOR codec, not registered, for bytes that
// are signed or hashed: it writes RFC 8949 §4.2.1's core deterministic
// encoding — the registered codec's, every float in the shortest of half,
// single and double that holds it exactly — and its Unmarshal and Decode
// refuse with NOT_DETERMINISTIC a document not in that encoding: a head or a
// float longer than it needs, an indefinite length, a NaN other than
// 0xf97e00, or map keys out of the bytewise order of their encodings. Into
// an interface it decodes every tag but a bignum as a Tag, so a document
// decodes and encodes back to its own bytes.
func NewDeterministic() codec.Codec {
	//: fresh, never registered: the registered format is unchanged.
	return &deterministicCodec{}
}

// Marshal encodes v in core deterministic encoding.
func (*deterministicCodec) Marshal(v any) (encoded []byte, err error) {
	//: a channel, a function, an unsafe pointer: no wire format holds one (#1101).
	if verr := codec.CheckValue(v); verr != nil {
		return nil, verr
	}
	buf := scratch.AcquireBuffer()
	b, err := appendValue(buf.AvailableBuffer(), v, deterministicWalk())
	//: the caller owns its copy; the scratch buffer goes back.
	if err == nil {
		encoded = bytes.Clone(b)
	}
	keepScratch(buf, b)
	//: the bytes, or MARSHAL_FAILED.
	return encoded, err
}

// Append appends the deterministic encoding of v to dst.
func (*deterministicCodec) Append(dst []byte, v any) (appended []byte, err error) {
	out, err := appendValue(dst, v, deterministicWalk())
	//: nothing appended on failure.
	if err != nil {
		//: dst as it was.
		return dst, err
	}
	//: appended.
	return out, nil
}

// Unmarshal decodes one data item in core deterministic encoding into v.
func (c *deterministicCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: no bytes is no document in this format (#1101).
	if len(data) == 0 {
		return codec.EmptyInput
	}
	//: the registered codec refuses what is not one valid item, with its code.
	n, err := validateItem(data, maxCBORNestedLevels)
	//: malformed, or followed by bytes.
	if err != nil || n != len(data) {
		//: UNMARSHAL_FAILED, as the registered codec reports it.
		return c.cborCodec.Unmarshal(data, v)
	}
	//: the encoding is checked whole, then the tags are kept.
	return unmarshalItemMode(data, v, decodeMode{deterministic: true})
}

// NewEncoder writes one deterministic data item per Encode.
func (*deterministicCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the shortest floats.
	return &cborEncoder{w: w, start: deterministicWalk()}
}

// NewDecoder reads one data item per Decode, each in core deterministic
// encoding.
func (*deterministicCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: each item checked as Unmarshal checks one.
	return &cborDecoder{r: r, walk: validator{limit: maxCBORNestedLevels}, mode: decodeMode{deterministic: true}}
}

// appendShortestFloat appends f in the shortest of half, single and double
// that holds it exactly — RFC 8949 §4.1's preferred serialization, which
// §4.2.1 requires; NaN is 0xf97e00 and the infinities halves, as always.
func appendShortestFloat(b []byte, f float64) []byte {
	//: NaN and the infinities.
	if special, ok := specialFloat(f); ok {
		//: a half.
		return appendHalf(b, special)
	}
	single := float32(f)
	//: a double that is not a single is a double.
	if float64(single) != f {
		//: eight bytes.
		return binary.BigEndian.AppendUint64(append(b, float64Head), math.Float64bits(f))
	}
	//: a single that is also a half is a half.
	if half, ok := halfOf(single); ok {
		//: two bytes.
		return appendHalf(b, half)
	}
	//: four bytes.
	return binary.BigEndian.AppendUint32(append(b, float32Head), math.Float32bits(single))
}

// halfOf returns the half-precision bits of the finite single f when a half
// holds it exactly — a normal half, a subnormal one, or a signed zero.
func halfOf(f float32) (half uint16, ok bool) {
	bits := math.Float32bits(f)
	sign := uint16(bits>>singleSignShift) & halfSignBit
	exponent := int(bits>>singleMantissaBits&singleExponentMask) - singleExponentBias
	mantissa := bits & singleMantissaMask
	//: the three ranges a half covers.
	switch {
	case bits&singleMagnitudeMask == 0:
		//: ±0.
		return sign, true
	case exponent >= halfMinNormalExponent && exponent <= halfMaxExponent:
		//: a normal half, when the bits it drops are zero.
		return normalHalf(sign, exponent, mantissa)
	case exponent >= halfSubnormalExponent && exponent < halfMinNormalExponent:
		//: a subnormal half, when the bits it drops are zero.
		return subnormalHalf(sign, exponent, mantissa)
	default:
		//: too large or too small for a half.
		return 0, false
	}
}

// normalHalf narrows a single of a normal half's exponent: exact when the
// thirteen low mantissa bits are zero.
func normalHalf(sign uint16, exponent int, mantissa uint32) (uint16, bool) {
	//: a bit a half cannot hold.
	if mantissa&(1<<singleToHalfDrop-1) != 0 {
		//: not exact.
		return 0, false
	}
	biased := uint16(exponent + int(halfExponentBias))
	//: sign, exponent, the top ten mantissa bits.
	return sign | biased<<halfMantissaBits | uint16(mantissa>>singleToHalfDrop), true
}

// subnormalHalf narrows a single below a half's smallest normal: the
// significand, hidden bit included, counted in units of 2^-24.
func subnormalHalf(sign uint16, exponent int, mantissa uint32) (uint16, bool) {
	significand := singleHiddenBit | mantissa
	shift := uint(-exponent - 1)
	//: a bit below 2^-24.
	if significand&(1<<shift-1) != 0 {
		//: not exact.
		return 0, false
	}
	//: sign and the subnormal's units.
	return sign | uint16(significand>>shift), true
}

// checkDeterministic walks the validated item at the cursor and refuses the
// first departure from RFC 8949 §4.2.1's core deterministic encoding.
func (d *decodeState) checkDeterministic() error {
	at := d.off
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: §4.2.1: no indefinite length.
	if h.info == infoIndefinite {
		//: refused.
		return notDeterministic(at, "an indefinite-length item")
	}
	//: floats and simple values have their own rule.
	if h.major == majorSimple {
		d.off += h.size
		//: the shortest float, the one NaN.
		return checkSimple(h, at)
	}
	//: §4.2.1: an argument in its shortest head.
	if !shortestHead(h) {
		//: refused.
		return notDeterministic(at, "an integer, a length or a tag number in a longer head than it needs")
	}
	d.off += h.size
	//: what the head opens.
	return d.checkEnclosed(h)
}

// checkEnclosed checks what the head h, already consumed, opens: a string's
// bytes, an array's elements, a map's pairs, a tag's content.
func (d *decodeState) checkEnclosed(h itemHead) error {
	//: by major type.
	switch h.major {
	case majorBytes, majorText:
		_, err := d.take(h.arg)
		//: the payload.
		return err
	case majorArray:
		//: each element.
		for range h.arg {
			//: the first departure.
			if err := d.checkDeterministic(); err != nil {
				//: refused.
				return err
			}
		}
		//: checked.
		return nil
	case majorMap:
		//: each pair, the keys in order.
		return d.checkPairs(h.arg)
	case majorTag:
		//: the content.
		return d.checkDeterministic()
	default:
		//: an integer: its head was all of it.
		return nil
	}
}

// checkPairs checks a map's pairs: each key and value deterministic, and
// every key's encoding bytewise greater than the one before — which also
// refuses a repeated key.
func (d *decodeState) checkPairs(pairs uint64) error {
	var previous []byte
	//: each pair in turn.
	for range pairs {
		start := d.off
		//: the key.
		if err := d.checkDeterministic(); err != nil {
			//: refused.
			return err
		}
		key := d.data[start:d.off]
		//: §4.2.1: sorted by the bytes of the encoded keys.
		if previous != nil && bytes.Compare(previous, key) >= 0 {
			//: out of order, or repeated.
			return notDeterministic(start, "map keys out of the bytewise order of their encodings")
		}
		previous = key
		//: the value.
		if err := d.checkDeterministic(); err != nil {
			//: refused.
			return err
		}
	}
	//: checked.
	return nil
}

// shortestHead reports whether h's argument takes no more bytes than it
// needs.
func shortestHead(h itemHead) bool {
	//: by the width the head used.
	switch h.info {
	case info1Byte:
		//: an argument the initial byte could have held.
		return h.arg > uint64(infoDirectMax)
	case info2Bytes:
		//: one byte would have done.
		return h.arg > math.MaxUint8
	case info4Bytes:
		//: two bytes would have done.
		return h.arg > math.MaxUint16
	case info8Bytes:
		//: four bytes would have done.
		return h.arg > math.MaxUint32
	default:
		//: in the initial byte.
		return true
	}
}

// checkSimple checks a float or simple-value item: a float in the shortest
// form that holds it, NaN only as 0xf97e00; simple values have one encoding
// already.
func checkSimple(h itemHead, at int) error {
	//: booleans, null, undefined and the simple values.
	if !isFloatHead(h) {
		//: validation refused their long forms.
		return nil
	}
	f := floatOf(h)
	//: the one NaN.
	if math.IsNaN(f) {
		//: 0xf97e00 and nothing else.
		if h.info != info2Bytes || uint16(h.arg) != halfNaN {
			//: refused.
			return notDeterministic(at, "a NaN other than 0xf97e00")
		}
		//: the canonical NaN.
		return nil
	}
	//: the width the shortest form takes.
	if shortestFloatWidth(f) != h.size {
		//: refused.
		return notDeterministic(at, "a float in a longer form than holds it exactly")
	}
	//: shortest.
	return nil
}

// shortestFloatWidth is the head size appendShortestFloat gives the finite
// or infinite f: 3, 5 or 9 bytes.
func shortestFloatWidth(f float64) int {
	var scratchHead [maxHeadBytes]byte
	//: the encoder decides, so the two cannot disagree.
	return len(appendShortestFloat(scratchHead[:0], f))
}

// notDeterministic is NewDeterministic's refusal of a document not in core
// deterministic encoding, at offset at.
func notDeterministic(at int, detail string) error {
	//: the offset of the departure; never the bytes.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecbor.CodeCBORNotDeterministic,
		Reason:  "NOT_DETERMINISTIC",
		Public:  "CBOR document is not in deterministic encoding",
		Private: "service/data/codec/cbor.NewDeterministic: " + detail,
	}, errs.Int(codec.FieldOffset, at))
}

// NewBoundedDecoder implements codec.BoundedStreamingCodec (#891): the
// deterministic stream decoder under a byte ceiling per item.
func (*deterministicCodec) NewBoundedDecoder(r io.Reader, maxBytes int64) (codec.Decoder, error) {
	//: every item checked as NewDeterministic's Unmarshal checks one.
	return newBoundedDecoder(r, maxBytes, decodeMode{deterministic: true})
}
