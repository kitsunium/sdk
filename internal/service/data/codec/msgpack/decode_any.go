package msgpack

import (
	"bytes"
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// anyPreallocElems is how many []any slots a declared count may reserve
// before its elements are read (preallocBytes / 16-byte interface).
const anyPreallocElems uint64 = uint64(preallocBytes) / anySlotBytes

// anySlotBytes is the size of one interface value.
const anySlotBytes uint64 = 16

// decodeAny reads one value into the Go type an empty interface receives.
func (d *decodeState) decodeAny() (any, error) {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return nil, err
	}
	//: containers and payload families read further; scalars are complete.
	switch h.fam {
	//: an array of untyped values.
	case famArray:
		return d.decodeArrayAny(h)
	//: a string-keyed map of untyped values.
	case famMap:
		return d.decodeMapAny(h)
	//: str, bin, ext carry a payload.
	case famStr, famBin, famExt:
		return d.decodePayloadAny(h)
	//: nil, booleans and numbers.
	default:
		return scalarAny(h), nil
	}
}

// scalarAny converts a scalar header into the Go value an interface receives.
func scalarAny(h header) any {
	//: the family and the field width pick the Go type.
	switch h.fam {
	//: a boolean.
	case famBool:
		return h.arg == 1
	//: a signed integer of the field's width.
	case famInt:
		return signedAny(int64(h.arg), h.width)
	//: an unsigned integer of the field's width.
	case famUint:
		return unsignedAny(h.arg, h.width)
	//: a single-precision float.
	case famFloat32:
		return math.Float32frombits(uint32(h.arg))
	//: a double-precision float.
	case famFloat64:
		return math.Float64frombits(h.arg)
	//: nil.
	default:
		return nil
	}
}

// signedAny returns n as the signed Go type of the field it was read from; a
// fixint (width 0) and int 8 are both int8.
func signedAny(n int64, width uint8) any {
	//: one Go type per width.
	switch width {
	//: int 16.
	case width16:
		return int16(n)
	//: int 32.
	case width32:
		return int32(n)
	//: int 64.
	case width64:
		return n
	//: fixint and int 8.
	default:
		return int8(n)
	}
}

// unsignedAny returns n as the unsigned Go type of the field it was read from.
func unsignedAny(n uint64, width uint8) any {
	//: one Go type per width.
	switch width {
	//: uint 16.
	case width16:
		return uint16(n)
	//: uint 32.
	case width32:
		return uint32(n)
	//: uint 64.
	case width64:
		return n
	//: uint 8.
	default:
		return uint8(n)
	}
}

// decodePayloadAny reads a str, bin or ext payload into its untyped Go value.
func (d *decodeState) decodePayloadAny(h header) (any, error) {
	p, err := d.take(h.arg)
	if err != nil {
		//: the payload is not there.
		return nil, err
	}
	//: the family decides the Go type.
	switch h.fam {
	//: a string owns a copy of the bytes.
	case famStr:
		return string(p), nil
	//: a []byte owns a copy too: the input may be a pooled buffer.
	case famBin:
		return cloneBytes(p), nil
	//: only the timestamp extension has a Go type.
	default:
		return extAny(h.ext, p)
	}
}

// extAny decodes an extension payload: the timestamp becomes a time.Time,
// every other type is refused by name.
func extAny(ext int8, p []byte) (any, error) {
	//: −1 is the only type the specification defines.
	if ext != extTimestamp {
		//: an application extension has no Go type here.
		return nil, unmarshalFault("unsupported extension type", errs.Int(fieldWire, int(ext)))
	}
	//: decode the instant.
	return decodeTimestamp(p)
}

// decodeArrayAny reads an array into a []any.
func (d *decodeState) decodeArrayAny(h header) (any, error) {
	//: a count the input cannot hold is refused before the slice exists.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return nil, err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return nil, err
	}
	//: reserve what a declared count may buy, then grow as elements arrive.
	out := make([]any, 0, min(h.arg, anyPreallocElems))
	//: one element per declared slot.
	for range h.arg {
		v, err := d.decodeAny()
		if err != nil {
			//: the first failure wins.
			return nil, err
		}
		out = append(out, v)
	}
	d.leave()
	//: the populated slice.
	return out, nil
}

// decodeMapAny reads a map into a map[string]any.
func (d *decodeState) decodeMapAny(h header) (any, error) {
	//: a count the input cannot hold is refused before the map exists.
	if err := d.checkCount(h); err != nil {
		//: implausible count.
		return nil, err
	}
	//: one level deeper.
	if err := d.enter(); err != nil {
		//: nested too deep.
		return nil, err
	}
	out := make(map[string]any, min(h.arg, anyPreallocElems))
	//: fill it.
	if err := d.fillMapAny(out, h.arg); err != nil {
		//: the first failure wins.
		return nil, err
	}
	d.leave()
	//: the populated map.
	return out, nil
}

// fillMapAny reads n string-keyed pairs into m.
func (d *decodeState) fillMapAny(m map[string]any, n uint64) error {
	//: one pair per declared count.
	for range n {
		key, err := d.readKeyString()
		if err != nil {
			//: not a string key, or truncated.
			return err
		}
		v, err := d.decodeAny()
		if err != nil {
			//: the first failure wins.
			return err
		}
		//: a repeated key keeps its last value.
		m[key] = v
	}
	//: every pair read.
	return nil
}

// readKeyString reads a map key that must be a string, as a Go string.
func (d *decodeState) readKeyString() (string, error) {
	key, err := d.readKey()
	//: the map owns a copy of the key.
	return string(key), err
}

// readKey reads a map key that must be a string: a str or a bin, or nil for
// the empty string, as the vendor-backed codec accepted. The slice aliases
// the input.
func (d *decodeState) readKey() ([]byte, error) {
	at := d.off
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return nil, err
	}
	//: the key families.
	switch h.fam {
	//: the usual key.
	case famStr, famBin:
		return d.take(h.arg)
	//: nil reads as "".
	case famNil:
		return nil, nil
	//: an integer, a float, a container… cannot key a string map.
	default:
		return nil, unmarshalFault("map key is not a string",
			errs.Int(fieldOffset, at), errs.String(fieldWire, h.fam.String()))
	}
}

// cloneBytes copies p into a fresh slice the caller owns. p is a payload of
// the input and never nil, so an empty bin decodes to an empty slice rather
// than to nil.
func cloneBytes(p []byte) []byte {
	//: never aliases the input, even when empty.
	return bytes.Clone(p)
}
