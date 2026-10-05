package cbor

import (
	"math"
	"math/big"
	"reflect"
	"time"
)

// The range a float of seconds must fall in to be an epoch time.
const (
	// maxEpochSeconds is 2^63: from there up, the seconds overflow an int64.
	maxEpochSeconds float64 = 0x1p63
	// nanosPerSecond scales a fraction of a second to nanoseconds.
	nanosPerSecond float64 = 1e9
)

// decode decodes true or false.
func (boolDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: only the two boolean simple values.
	if h.major != majorSimple || (h.info != simpleTrue && h.info != simpleFalse) {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	d.off += h.size
	v.SetBool(h.info == simpleTrue)
	//: stored.
	return nil
}

// decode decodes an integer into a signed integer of any width.
func (intDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	n, fits, err := d.signedValue(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: not an integer, or one this width cannot hold.
	if !fits || v.OverflowInt(n) {
		d.noteMismatch(h, p.typ)
		//: recorded; the item is consumed.
		return nil
	}
	v.SetInt(n)
	//: stored.
	return nil
}

// decode decodes a non-negative integer into an unsigned integer of any
// width.
func (uintDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	n, fits, err := d.unsignedValue(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: negative, not an integer, or too wide for this width.
	if !fits || v.OverflowUint(n) {
		d.noteMismatch(h, p.typ)
		//: recorded; the item is consumed.
		return nil
	}
	v.SetUint(n)
	//: stored.
	return nil
}

// decode decodes a float or an integer into a float32 or a float64.
func (floatDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	f, fits, err := d.floatValue(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: not a number, or beyond a float32's range.
	if !fits || v.OverflowFloat(f) {
		d.noteMismatch(h, p.typ)
		//: recorded; the item is consumed.
		return nil
	}
	v.SetFloat(f)
	//: stored.
	return nil
}

// decode decodes a text string. A byte string is refused, as
// fxamacker/cbor refused it.
func (stringDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, a BinaryUnmarshaler, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	//: only text.
	if h.major != majorText {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	content, _, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	v.SetString(string(content))
	//: stored; validation checked it is UTF-8.
	return nil
}

// signedValue consumes the item h opens and returns it as an int64. fits is
// false — and the item still consumed — when it is not an integer or does
// not fit an int64.
func (d *decodeState) signedValue(h itemHead) (n int64, fits bool, err error) {
	//: integers and bignums.
	switch h.major {
	case majorUnsigned:
		d.off += h.size
		//: up to math.MaxInt64.
		return int64(h.arg), h.arg <= math.MaxInt64, nil
	case majorNegative:
		d.off += h.size
		//: −1−n, down to math.MinInt64.
		return ^int64(h.arg), h.arg <= math.MaxInt64, nil
	case majorTag:
		wide, err := d.readBignum(h)
		//: a bignum within an int64 is an integer like any other.
		return bigInt64(wide), wide != nil && wide.IsInt64(), err
	default:
		//: not an integer.
		return 0, false, d.skip()
	}
}

// unsignedValue consumes the item h opens and returns it as a uint64. fits
// is false — and the item still consumed — when it is not a non-negative
// integer that fits a uint64.
func (d *decodeState) unsignedValue(h itemHead) (n uint64, fits bool, err error) {
	//: non-negative integers and bignums.
	switch h.major {
	case majorUnsigned:
		d.off += h.size
		//: any uint64.
		return h.arg, true, nil
	case majorTag:
		wide, err := d.readBignum(h)
		//: a bignum within a uint64 is an integer like any other.
		return bigUint64(wide), wide != nil && wide.IsUint64(), err
	default:
		//: negative, or not an integer.
		return 0, false, d.skip()
	}
}

// floatValue consumes the item h opens and returns it as a float64: a float
// as it is, an integer converted. fits is false — and the item still
// consumed — when it is not a number this codec reads as one.
func (d *decodeState) floatValue(h itemHead) (f float64, fits bool, err error) {
	//: floats, integers and bignums.
	switch {
	case isFloatHead(h):
		d.off += h.size
		//: half, single or double, widened exactly.
		return floatOf(h), true, nil
	case h.major == majorUnsigned:
		d.off += h.size
		//: converted.
		return float64(h.arg), true, nil
	case h.major == majorNegative:
		d.off += h.size
		//: converted when −1−n fits an int64, as fxamacker/cbor did.
		return float64(^int64(h.arg)), h.arg <= math.MaxInt64, nil
	case h.major == majorTag:
		//: a bignum within 64 bits.
		return d.bignumFloat(h)
	default:
		//: not a number.
		return 0, false, d.skip()
	}
}

// bignumFloat reads a bignum and converts it when it fits 64 bits — a
// uint64 for tag 2, an int64 for tag 3 — as fxamacker/cbor converted it.
func (d *decodeState) bignumFloat(h itemHead) (f float64, fits bool, err error) {
	wide, err := d.readBignum(h)
	//: not a bignum, or never on validated input.
	if err != nil || wide == nil {
		//: refused.
		return 0, false, err
	}
	//: positive bignums convert from a uint64.
	if h.arg == tagPositiveBignum {
		//: within 64 bits or refused.
		return float64(wide.Uint64()), wide.IsUint64(), nil
	}
	//: negative ones from an int64.
	return float64(wide.Int64()), wide.IsInt64(), nil
}

// readBignum consumes the item h opens. When it is a bignum (tag 2 or 3) its
// value is returned; any other tag gives nil.
func (d *decodeState) readBignum(h itemHead) (*big.Int, error) {
	//: skipTags left the cursor on a bignum tag, or on nothing else.
	if h.major != majorTag || (h.arg != tagPositiveBignum && h.arg != tagNegativeBignum) {
		//: not a bignum; consumed.
		return nil, d.skip()
	}
	d.off += h.size
	//: the byte string that follows, checked by validation.
	return d.bignumContent(h.arg)
}

// bigInt64 is n's int64 value, or 0 for no bignum.
func bigInt64(n *big.Int) int64 {
	//: no bignum.
	if n == nil {
		//: zero.
		return 0
	}
	//: the low 64 bits; fits says whether that is the value.
	return n.Int64()
}

// bigUint64 is n's uint64 value, or 0 for no bignum.
func bigUint64(n *big.Int) uint64 {
	//: no bignum.
	if n == nil {
		//: zero.
		return 0
	}
	//: the low 64 bits; fits says whether that is the value.
	return n.Uint64()
}

// decode decodes into a time.Time: an RFC 3339 text string, or Unix
// seconds as an integer or a float — tagged 0, 1 or with any other tag, or
// untagged. Null leaves the time as it is; NaN and the infinities give the
// zero time (RFC 8949 §3.4.2).
func (timeDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, err := d.skipAllTags()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: null is "no time": nothing to store.
	if isNull(h) {
		d.off += h.size
		//: unchanged.
		return nil
	}
	t, stored, err := d.timeValue(h)
	//: an item that is not a time was recorded.
	if err != nil || !stored {
		//: consumed.
		return err
	}
	setValue(v, t)
	//: stored.
	return nil
}

// timeValue consumes the item h opens and returns the time it holds. stored
// is false, the failure recorded and the item consumed, when it holds none.
func (d *decodeState) timeValue(h itemHead) (instant time.Time, stored bool, err error) {
	//: the shapes a time can take.
	switch {
	case h.major == majorText:
		//: RFC 3339.
		return d.textTime(h)
	case h.major == majorUnsigned || h.major == majorNegative:
		n, fits, signedErr := d.signedValue(h)
		//: seconds that fit an int64.
		return d.epochTime(h, time.Unix(n, 0), fits, signedErr)
	case isFloatHead(h):
		d.off += h.size
		//: seconds with a fraction.
		return d.floatTime(h, floatOf(h))
	default:
		//: a byte string, a container, a boolean.
		return time.Time{}, false, d.mismatch(h, timeType)
	}
}

// epochTime finishes an integer epoch time: t when the seconds fit an int64.
func (d *decodeState) epochTime(h itemHead, t time.Time, fits bool, err error) (time.Time, bool, error) {
	//: an overflow is recorded, a broken walk returned.
	if err != nil || !fits {
		//: recorded when it overflowed.
		if err == nil {
			d.noteMismatch(h, timeType)
		}
		//: nothing stored.
		return time.Time{}, false, err
	}
	//: the instant, in the local zone as time.Unix gives it.
	return t, true, nil
}

// textTime parses an RFC 3339 date/time text string.
func (d *decodeState) textTime(h itemHead) (time.Time, bool, error) {
	content, _, err := d.stringBytes(h)
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return time.Time{}, false, err
	}
	t, parseErr := time.Parse(time.RFC3339, string(content))
	//: a text string that is not a date/time.
	if parseErr != nil {
		d.note(func() string {
			//: never the text itself.
			return "a text string that is not an RFC 3339 date/time"
		})
		//: nothing stored.
		return time.Time{}, false, nil
	}
	//: the instant, in the offset the text gave.
	return t, true, nil
}

// floatTime converts float seconds to a time: NaN and the infinities to the
// zero time, an out-of-range value to a recorded failure.
func (d *decodeState) floatTime(h itemHead, seconds float64) (time.Time, bool, error) {
	//: RFC 8949 §3.4.2: no meaningful instant.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		//: the zero time.
		return time.Time{}, true, nil
	}
	whole, fraction := math.Modf(seconds)
	//: beyond an int64 of seconds.
	if whole >= maxEpochSeconds || whole < -maxEpochSeconds {
		d.noteMismatch(h, timeType)
		//: nothing stored.
		return time.Time{}, false, nil
	}
	//: the instant, in the local zone as time.Unix gives it.
	return time.Unix(int64(whole), int64(fraction*nanosPerSecond)), true, nil
}

// skipAllTags consumes every tag head at the cursor and returns the head of
// the enclosed item.
func (d *decodeState) skipAllTags() (itemHead, error) {
	//: a time target reads its value whatever tags enclose it.
	for {
		h, err := d.peek()
		//: the first head that is not a tag.
		if err != nil || h.major != majorTag {
			//: the item.
			return h, err
		}
		d.off += h.size
	}
}

// decode decodes an integer or a bignum into a big.Int.
func (bigIntDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, done, err := d.begin(v, p)
	//: null, or a broken walk.
	if done || err != nil {
		//: handled.
		return err
	}
	n, err := d.bigValue(h)
	//: not an integer, recorded; or a broken walk.
	if err != nil || n == nil {
		//: consumed.
		return err
	}
	setValue(v, *n)
	//: stored.
	return nil
}

// bigValue consumes the item h opens and returns it as a big.Int, or nil —
// the failure recorded — when it is not an integer.
func (d *decodeState) bigValue(h itemHead) (*big.Int, error) {
	//: integers of either sign, and bignums.
	switch h.major {
	case majorUnsigned:
		d.off += h.size
		//: n.
		return new(big.Int).SetUint64(h.arg), nil
	case majorNegative:
		d.off += h.size
		//: −1−n.
		return new(big.Int).Not(new(big.Int).SetUint64(h.arg)), nil
	case majorTag:
		n, err := d.readBignum(h)
		//: another tag is not an integer.
		if err == nil && n == nil {
			d.noteMismatch(h, bigIntType)
		}
		//: the bignum, or nothing.
		return n, err
	default:
		//: recorded and skipped.
		return nil, d.mismatch(h, bigIntType)
	}
}

// setValue stores x into v, through its address when it has one, so a
// struct-sized value is copied once and never boxed.
func setValue[T any](v reflect.Value, x T) {
	//: an addressable value is written in place.
	if v.CanAddr() {
		//: the address has the exact type.
		if ptr, ok := reflect.TypeAssert[*T](v.Addr()); ok {
			*ptr = x
			//: stored.
			return
		}
	}
	//: through reflection otherwise.
	v.Set(reflect.ValueOf(x))
}
