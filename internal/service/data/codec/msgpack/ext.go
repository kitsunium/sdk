package msgpack

import (
	"encoding/binary"
	"math"
	"reflect"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The extension forms an Ext is written in (spec.md §ext format family): a
// fixext for the five payload sizes it carries, else ext 8, 16 or 32.
const (
	// codeFixext1 is fixext 1; fixext 2, 4, 8 and 16 follow it, 0xd5 to 0xd8,
	// one code per doubling of the payload.
	codeFixext1 byte = 0xd4
	// codeExt16 is ext 16.
	codeExt16 byte = 0xc8
	// codeExt32 is ext 32.
	codeExt32 byte = 0xc9
	// fixextSizes is how many payload sizes the fixext forms cover.
	fixextSizes int = 5
)

// Ext is a MessagePack extension value the codec has no Go type for: its type
// and its payload. Types 0 to 127 are the applications' own — the record
// types, UUIDs and decimals other MessagePack stacks write — and −128 to −2
// are reserved by the specification; −1 is the timestamp, which decodes into
// an interface as a time.Time, and into an Ext as its raw payload.
//
// Decoded into an interface, an extension of any other type is an Ext holding
// a copy of its payload; decoded into an Ext, any extension is. An Ext encodes
// in the shortest form its payload allows: fixext 1, 2, 4, 8 or 16, else
// ext 8, 16 or 32.
type Ext struct {
	// Data is the payload, opaque to the codec.
	Data []byte
	// Type is the extension type.
	Type int8
}

// extType is Ext, matched exactly before its kind is looked at.
var extType = reflect.TypeFor[Ext]()

// encodeExt writes an Ext.
func encodeExt(b []byte, v reflect.Value, _ int) ([]byte, error) {
	e, ok := interfaceOf[Ext](v)
	//: an Ext reached through an unexported embedding cannot be read.
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	//: the shortest form.
	return appendExt(b, e)
}

// appendExt appends e in the shortest extension form its payload allows.
func appendExt(b []byte, e Ext) ([]byte, error) {
	n := len(e.Data)
	typ := byte(e.Type)
	//: the five sizes a fixext carries without a length field.
	for i := range fixextSizes {
		//: 1, 2, 4, 8, 16.
		if n == 1<<i {
			//: fixext 1 to 16, then the type.
			return append(append(b, codeFixext1+byte(i), typ), e.Data...), nil
		}
	}
	//: then the narrowest length field.
	switch {
	//: ext 8.
	case n <= math.MaxUint8:
		b = append(b, codeExt8, byte(n), typ)
	//: ext 16.
	case n <= math.MaxUint16:
		b = append(binary.BigEndian.AppendUint16(append(b, codeExt16), uint16(n)), typ)
	//: ext 32.
	case uint64(n) <= math.MaxUint32:
		b = append(binary.BigEndian.AppendUint32(append(b, codeExt32), uint32(n)), typ)
	//: a payload the wire cannot carry.
	default:
		return b, marshalFault("extension payload exceeds the ext 32 length", errs.Int(fieldLen, n))
	}
	//: the payload.
	return append(b, e.Data...), nil
}

// decodeExtValue decodes into an Ext: any extension, its type and a copy of
// its payload; nil zeroes it.
func decodeExtValue(d *decodeState, v reflect.Value) error {
	at := d.off
	h, err := d.readHeader()
	//: malformed or truncated.
	if err != nil {
		//: the header's own failure.
		return err
	}
	var e Ext
	//: the forms an Ext is read from.
	switch h.fam {
	//: an extension, timestamp included.
	case famExt:
		p, terr := d.take(h.arg)
		//: truncated.
		if terr != nil {
			//: the declared length is not there.
			return terr
		}
		e = Ext{Type: h.ext, Data: cloneBytes(p)}
	//: nil is the zero Ext.
	case famNil:
	//: anything else is not an extension.
	default:
		return unmarshalFault("an Ext takes an extension", errs.Int(fieldOffset, at))
	}
	dst, ok := interfaceOf[*Ext](v.Addr())
	//: an Ext reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", errs.Int(fieldOffset, at))
	}
	*dst = e
	//: stored without boxing.
	return nil
}
