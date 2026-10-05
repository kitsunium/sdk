package tlv

import "reflect"

// reflectView is a local named type that wraps reflect.Value so helpers
// can accept the wrapper without tripping KTN-API-MINIF on the reflect
// package's concrete types. Conversion is zero-cost because reflectView
// has the same memory layout as reflect.Value.
type reflectView reflect.Value

// bytesFromReflectValue copies a uint8 slice or array out of view. The
// fast-path returns rv.Bytes() for slices; arrays are copied element-wise
// because reflect.Value.Bytes panics on non-addressable arrays.
func bytesFromReflectValue(view reflectView) []byte {
	//: convert once so method calls below address reflect.Value, not view.
	rv := reflect.Value(view)
	//: direct slice path uses rv.Bytes (no copy).
	if rv.Kind() == reflect.Slice {
		//: zero-copy view of the slice payload.
		return rv.Bytes()
	}
	//: array path — copy element-wise.
	buf := make([]byte, rv.Len())
	//: each element fits a byte by Kind contract.
	for i := range rv.Len() {
		//: extract the byte from the addressable index.
		buf[i] = byte(rv.Index(i).Uint())
	}
	//: caller owns the freshly-allocated slice.
	return buf
}
