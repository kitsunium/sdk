package msgpack

import (
	"encoding"
	"math"
	"reflect"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// decodeFunc decodes the next value of d into v, which is settable.
type decodeFunc func(d *decodeState, v reflect.Value) error

// unmarshalMsgpacker is the method a type implements to read its own
// MessagePack. It receives a copy of exactly one value's bytes.
type unmarshalMsgpacker interface {
	// UnmarshalMsgpack decodes the value's MessagePack encoding.
	UnmarshalMsgpack(data []byte) error
}

// listDecoder decodes a slice or an array element by element.
type listDecoder struct {
	// elem decodes one element.
	elem decodeFunc
	// firstCap is how many elements a declared count may reserve up front.
	firstCap uint64
}

// decodedError is what a string decodes to when the target is the error
// interface: the message, and nothing else.
type decodedError struct {
	// msg is the decoded text.
	msg string
}

// Plan cache and the interfaces the builders look for.
var (
	// decoders caches decodeFunc per reflect.Type.
	decoders sync.Map
	// unmarshalMsgpackerType is the UnmarshalMsgpack method.
	unmarshalMsgpackerType = reflect.TypeFor[unmarshalMsgpacker]()
	// binaryUnmarshalerType is encoding.BinaryUnmarshaler.
	binaryUnmarshalerType = reflect.TypeFor[encoding.BinaryUnmarshaler]()
	// textUnmarshalerType is encoding.TextUnmarshaler.
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	// stringType, float64Type and boolType name the targets of the direct
	// map decoders in their failures.
	stringType = reflect.TypeFor[string]()
	// float64Type is float64.
	float64Type = reflect.TypeFor[float64]()
	// boolType is bool.
	boolType = reflect.TypeFor[bool]()
)

// Error implements error.
func (e *decodedError) Error() string {
	//: the decoded text, verbatim.
	return e.msg
}

// decoderFor returns the cached decoder of t, building it on first use.
func decoderFor(t reflect.Type) decodeFunc {
	//: fast path: built before.
	if cached, ok := decoders.Load(t); ok {
		//: the cache only ever holds decodeFunc.
		if f, isFunc := cached.(decodeFunc); isFunc {
			//: hit.
			return f
		}
	}
	//: a recursive type reaches itself while being built: cache a
	//: placeholder that waits for the real decoder, then build.
	var (
		wg    sync.WaitGroup
		built decodeFunc
	)
	wg.Add(1)
	placeholder := decodeFunc(func(d *decodeState, v reflect.Value) error {
		wg.Wait()
		//: forward to the finished decoder.
		return built(d, v)
	})
	//: another goroutine may have started first; use its entry.
	if actual, loaded := decoders.LoadOrStore(t, placeholder); loaded {
		//: the cache only ever holds decodeFunc.
		if f, isFunc := actual.(decodeFunc); isFunc {
			//: theirs.
			return f
		}
	}
	built = buildDecoder(t)
	wg.Done()
	decoders.Store(t, built)
	//: the finished decoder.
	return built
}

// buildDecoder chooses the decoder of t: a self-decoding type first, then its
// kind.
func buildDecoder(t reflect.Type) decodeFunc {
	//: a type that reads itself wins over its kind.
	if f := hookDecoder(t); f != nil {
		//: time, UnmarshalMsgpack, BinaryUnmarshaler or TextUnmarshaler.
		return f
	}
	//: scalars need no plan.
	if f := scalarDecoder(t.Kind()); f != nil {
		//: bool, number or string.
		return f
	}
	//: containers, pointers, interfaces, or an unsupported kind.
	return compositeDecoder(t)
}

// hookDecoder returns the decoder of a type that decodes itself, or nil.
func hookDecoder(t reflect.Type) decodeFunc {
	//: a pointer or interface defers to what it holds.
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface {
		//: decided on the element.
		return nil
	}
	pt := reflect.PointerTo(t)
	//: first match wins, in the vendor's order.
	switch {
	//: the timestamp extension.
	case t == timeType:
		return decodeTimeValue
	//: the MessagePack-specific method.
	case pt.Implements(unmarshalMsgpackerType):
		return decodeSelfMsgpack
	//: binary form.
	case pt.Implements(binaryUnmarshalerType):
		return decodeSelfBinary
	//: text form.
	case pt.Implements(textUnmarshalerType):
		return decodeSelfText
	//: an ordinary value.
	default:
		return nil
	}
}

// scalarDecoder returns the decoder of a bool, number or string kind, or nil.
func scalarDecoder(k reflect.Kind) decodeFunc {
	//: one decoder per kind family.
	switch k {
	//: true, false or nil.
	case reflect.Bool:
		return decodeBoolValue
	//: every signed width, range-checked.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decodeIntValue
	//: every unsigned width, range-checked.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return decodeUintValue
	//: both float widths.
	case reflect.Float32, reflect.Float64:
		return decodeFloatValue
	//: str or bin.
	case reflect.String:
		return decodeStringValue
	//: not a scalar.
	default:
		return nil
	}
}

// compositeDecoder returns the decoder of a container, pointer or interface
// kind, and a refusal for the kinds MessagePack cannot fill.
func compositeDecoder(t reflect.Type) decodeFunc {
	//: one builder per kind.
	switch t.Kind() {
	//: []byte from bin or str; any other slice from an array.
	case reflect.Slice:
		return sliceDecoder(t)
	//: [N]byte from bin or str; any other array from an array.
	case reflect.Array:
		return arrayDecoder(t)
	//: a map, keys and values by their own decoders.
	case reflect.Map:
		return newMapDecoder(t)
	//: a struct, through its cached layout.
	case reflect.Struct:
		return newStructDecoder(t)
	//: nil, or allocate and decode the element.
	case reflect.Pointer:
		return pointerDecoder(t)
	//: the untyped value, an error message, or into a held pointer.
	case reflect.Interface:
		return interfaceDecoder(t)
	//: chan, func, complex, unsafe.Pointer.
	default:
		return failingDecoder(unmarshalFault("cannot decode into a value of this Go type", typeField(t)))
	}
}

// failingDecoder returns a decoder that always fails with err.
func failingDecoder(err error) decodeFunc {
	//: the failure is decided once, when the plan is built.
	return func(*decodeState, reflect.Value) error {
		//: same failure every time.
		return err
	}
}

// mismatch is the failure for a value of the wrong family for the target.
func (d *decodeState) mismatch(h header, t reflect.Type) error {
	//: name what arrived, what was expected, and where.
	return unmarshalFault("cannot decode the MessagePack value into this Go type",
		errs.String(fieldWire, h.fam.String()), typeField(t), errs.Int(fieldOffset, d.off))
}

// zeroValue sets v to its zero value.
func zeroValue(v reflect.Value) error {
	//: a value reached through an unexported embedding cannot be set.
	if !v.CanSet() {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", typeField(v.Type()))
	}
	v.SetZero()
	//: zeroed.
	return nil
}

// decodeBoolValue decodes into a bool kind.
func decodeBoolValue(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	b, ok := boolArg(h)
	//: anything but a boolean or nil does not fit a bool.
	if !ok {
		//: wrong family.
		return d.mismatch(h, v.Type())
	}
	v.SetBool(b)
	//: stored.
	return nil
}

// boolArg reads a boolean header; nil is false.
func boolArg(h header) (value, ok bool) {
	//: only booleans and nil.
	switch h.fam {
	//: true or false.
	case famBool:
		return h.arg == 1, true
	//: nil is false, as the vendor decoded it.
	case famNil:
		return false, true
	//: not a boolean.
	default:
		return false, false
	}
}

// decodeIntValue decodes into a signed kind, refusing a value it cannot hold.
func decodeIntValue(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	n, ok := signedArg(h)
	//: not an integer, or not one this kind can hold.
	if !ok || v.OverflowInt(n) {
		//: refuse instead of wrapping.
		return d.rangeOrMismatch(h, v.Type(), isInteger(h.fam))
	}
	v.SetInt(n)
	//: stored.
	return nil
}

// decodeUintValue decodes into an unsigned kind, refusing a value it cannot
// hold.
func decodeUintValue(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	n, ok := unsignedArg(h)
	//: not an integer, negative, or too large for this kind.
	if !ok || v.OverflowUint(n) {
		//: refuse instead of wrapping.
		return d.rangeOrMismatch(h, v.Type(), isInteger(h.fam))
	}
	v.SetUint(n)
	//: stored.
	return nil
}

// decodeFloatValue decodes into a float kind; integers are accepted.
func decodeFloatValue(d *decodeState, v reflect.Value) error {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	f, ok := floatArg(h)
	//: not a number, or beyond float32's range.
	if !ok || v.OverflowFloat(f) {
		//: refuse instead of producing ±Inf.
		return d.rangeOrMismatch(h, v.Type(), ok)
	}
	v.SetFloat(f)
	//: stored.
	return nil
}

// rangeOrMismatch is the failure for a number that does not fit (numeric is
// set) or a value that is not a number of an acceptable family.
func (d *decodeState) rangeOrMismatch(h header, t reflect.Type, numeric bool) error {
	//: a family that could fit, with a value that does not.
	if numeric {
		//: name the target type, never the value.
		return unmarshalFault("number does not fit the Go type", typeField(t), errs.Int(fieldOffset, d.off))
	}
	//: the wrong family altogether.
	return d.mismatch(h, t)
}

// isInteger reports whether a family is one of the integer families, so a
// refusal can say "does not fit" rather than "wrong type".
func isInteger(f family) bool {
	//: signed or unsigned.
	return f == famInt || f == famUint
}

// signedArg reads an integer header as an int64; nil is 0.
func signedArg(h header) (int64, bool) {
	//: integer families and nil.
	switch h.fam {
	//: already two's complement.
	case famInt:
		return int64(h.arg), true
	//: unsigned, if it fits.
	case famUint:
		return int64(h.arg), h.arg <= math.MaxInt64
	//: nil is zero, as the vendor decoded it.
	case famNil:
		return 0, true
	//: not an integer.
	default:
		return 0, false
	}
}

// unsignedArg reads an integer header as a uint64; nil is 0, a negative value
// does not fit.
func unsignedArg(h header) (uint64, bool) {
	//: integer families and nil.
	switch h.fam {
	//: already unsigned.
	case famUint:
		return h.arg, true
	//: signed, if it is not negative.
	case famInt:
		return h.arg, int64(h.arg) >= 0
	//: nil is zero.
	case famNil:
		return 0, true
	//: not an integer.
	default:
		return 0, false
	}
}

// floatArg reads a numeric header as a float64; nil is 0.
func floatArg(h header) (float64, bool) {
	//: floats, integers and nil.
	switch h.fam {
	//: widened exactly.
	case famFloat32:
		return float64(math.Float32frombits(uint32(h.arg))), true
	//: as encoded.
	case famFloat64:
		return math.Float64frombits(h.arg), true
	//: signed integer.
	case famInt:
		return float64(int64(h.arg)), true
	//: unsigned integer.
	case famUint:
		return float64(h.arg), true
	//: nil is zero.
	case famNil:
		return 0, true
	//: not a number.
	default:
		return 0, false
	}
}

// decodeStringValue decodes into a string kind from str or bin; nil is "".
func decodeStringValue(d *decodeState, v reflect.Value) error {
	p, err := d.readBytesOrNil(v.Type())
	if err != nil {
		//: wrong family, malformed or truncated.
		return err
	}
	v.SetString(string(p))
	//: the string owns a copy of the bytes.
	return nil
}

// readBytesOrNil reads a str or bin payload, or nil as no bytes; any other
// family is a mismatch for target type t. The slice aliases the input.
func (d *decodeState) readBytesOrNil(t reflect.Type) ([]byte, error) {
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return nil, err
	}
	//: the byte-carrying families.
	switch h.fam {
	//: str and bin are interchangeable here.
	case famStr, famBin:
		return d.take(h.arg)
	//: nil carries no bytes.
	case famNil:
		return nil, nil
	//: anything else.
	default:
		return nil, d.mismatch(h, t)
	}
}

// decodeTimeValue decodes into a time.Time from the timestamp extension, an
// RFC 3339 string, or nil (the zero time).
func decodeTimeValue(d *decodeState, v reflect.Value) error {
	at := d.off
	h, err := d.readHeader()
	if err != nil {
		//: malformed or truncated.
		return err
	}
	t, err := d.timeOf(h, v.Type())
	if err != nil {
		//: not a time.
		return err
	}
	dst, ok := interfaceOf[*time.Time](v.Addr())
	//: a time reached through an unexported embedding.
	if !ok {
		//: refuse rather than panic.
		return unmarshalFault("cannot set a value reached through an unexported field", errs.Int(fieldOffset, at))
	}
	*dst = t
	//: stored without boxing.
	return nil
}

// timeOf reads the time a header announces.
func (d *decodeState) timeOf(h header, t reflect.Type) (time.Time, error) {
	//: the forms a time is accepted in.
	switch h.fam {
	//: the timestamp extension.
	case famExt:
		return d.timestampPayload(h)
	//: an RFC 3339 string, as other encoders write a time.
	case famStr:
		return d.timeFromText(h)
	//: nil is the zero time.
	case famNil:
		return time.Time{}, nil
	//: anything else.
	default:
		return time.Time{}, d.mismatch(h, t)
	}
}

// timestampPayload reads the payload of an extension that must be the
// timestamp.
func (d *decodeState) timestampPayload(h header) (time.Time, error) {
	//: an application extension is not a time.
	if h.ext != extTimestamp {
		//: name the type found.
		return time.Time{}, unmarshalFault("extension is not a timestamp", errs.Int(fieldWire, int(h.ext)))
	}
	p, err := d.take(h.arg)
	if err != nil {
		//: truncated.
		return time.Time{}, err
	}
	//: one of the three forms.
	return decodeTimestamp(p)
}

// timeFromText parses an RFC 3339 string as a UTC time.
func (d *decodeState) timeFromText(h header) (time.Time, error) {
	p, err := d.take(h.arg)
	if err != nil {
		//: truncated.
		return time.Time{}, err
	}
	t, perr := time.Parse(time.RFC3339Nano, string(p))
	//: not an RFC 3339 time.
	if perr != nil {
		//: the parser's error quotes the text, so it is not kept as a cause.
		return time.Time{}, unmarshalFault("string is not an RFC 3339 time", errs.Int(fieldOffset, d.off))
	}
	//: the instant, in UTC like a decoded timestamp.
	return t.UTC(), nil
}
