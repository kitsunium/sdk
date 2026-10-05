package cbor

import (
	"encoding"
	"reflect"
	"time"
)

// timeEncoder encodes a time.Time as Unix seconds, or null when zero.
type timeEncoder struct{}

// cborMarshalerEncoder writes what a type's MarshalCBOR returns.
type cborMarshalerEncoder struct{}

// binaryMarshalerEncoder writes what MarshalBinary returns as a byte string.
type binaryMarshalerEncoder struct{}

// encode appends Unix seconds, or null for the zero time.
func (timeEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	instant, _ := reflect.TypeAssert[time.Time](v)
	//: through the shared appender.
	return appendTime(b, instant), nil
}

// empty is never true: the zero time is written as null, not omitted, as before.
func (timeEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: always written.
	return false, nil
}

// encode appends what MarshalCBOR returns, once it is checked to be exactly
// one well-formed, valid data item that fits the nesting left.
func (cborMarshalerEncoder) encode(b []byte, v reflect.Value, p *encodePlan, at walkDepth) ([]byte, error) {
	m, _ := asMethods[interface{ MarshalCBOR() ([]byte, error) }](v)
	data, err := m.MarshalCBOR()
	//: the type's own failure, kept as the cause.
	if err != nil {
		//: MARSHAL_FAILED, or the cause's own code when it is an SDK error.
		return b, encodeCause(err, "MarshalCBOR failed for "+p.typ.String())
	}
	n, err := validateItem(data, maxCBORNestedLevels-at.nest)
	//: the bytes are written verbatim, so they are checked as input would be.
	if err != nil || n != len(data) {
		//: refused: the codec never writes what it would not read back.
		return b, encodeFailure("MarshalCBOR of " + p.typ.String() + " returned something other than one valid CBOR data item")
	}
	//: verbatim.
	return append(b, data...), nil
}

// empty is never true: the type writes its own item.
func (cborMarshalerEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: always written.
	return false, nil
}

// encode appends what MarshalBinary returns as a byte string.
func (binaryMarshalerEncoder) encode(b []byte, v reflect.Value, p *encodePlan, _ walkDepth) ([]byte, error) {
	data, err := marshalBinary(v, p)
	//: the type's own failure.
	if err != nil {
		//: already wrapped.
		return b, err
	}
	b = appendHead(b, majorBytes, uint64(len(data)))
	//: the bytes as returned.
	return append(b, data...), nil
}

// empty is true when MarshalBinary returns no byte.
func (binaryMarshalerEncoder) empty(v reflect.Value, p *encodePlan) (bool, error) {
	data, err := marshalBinary(v, p)
	//: no byte, or the question failed.
	return len(data) == 0 && err == nil, err
}

// marshalBinary calls v's MarshalBinary, through its address when the method
// has a pointer receiver, and wraps its failure.
func marshalBinary(v reflect.Value, p *encodePlan) ([]byte, error) {
	m, _ := asMethods[encoding.BinaryMarshaler](v)
	data, err := m.MarshalBinary()
	//: the type's own failure, kept as the cause.
	if err != nil {
		//: MARSHAL_FAILED, or the cause's own code when it is an SDK error.
		return nil, encodeCause(err, "MarshalBinary failed for "+p.typ.String())
	}
	//: the bytes.
	return data, nil
}
