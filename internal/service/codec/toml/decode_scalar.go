// Package toml — scalars into Go values: strings, integers, floats, booleans
// and the four date and time kinds, each into the types that can hold it
// without losing it, or a refusal.
package toml

import (
	"math"
	"reflect"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The zone a local value takes when the target is a time.Time.
var localZone = time.Local

// scalarAny returns the untyped Go value of a scalar node.
func (d *decoder) scalarAny(node *node) any {
	switch node.kind {
	//: int64.
	case kindInteger:
		return int64(node.num)
	//: float64.
	case kindFloat:
		return math.Float64frombits(node.num)
	//: bool.
	case kindBool:
		return node.num == 1
	//: time.Time, in the offset the document wrote.
	case kindDateTime:
		return instantOf(&d.p.times[node.num])
	//: the three local types.
	default:
		return d.localAny(node)
	}
}

// localAny returns the untyped Go value of a local date, time or date-time.
func (d *decoder) localAny(node *node) any {
	dt := &d.p.times[node.num]
	switch node.kind {
	//: a calendar day.
	case kindLocalDate:
		return localDateOf(dt)
	//: a time of day.
	case kindLocalTime:
		return localTimeOf(dt)
	//: a date and a time of day.
	default:
		return LocalDateTime{LocalDate: localDateOf(dt), LocalTime: localTimeOf(dt)}
	}
}

// instantOf returns the time.Time of an offset date-time: in UTC for a zero
// offset, in a fixed zone otherwise.
func instantOf(dt *datetime) time.Time {
	zone := time.UTC
	//: a non-zero offset gets an unnamed fixed zone.
	if dt.offset != 0 {
		zone = time.FixedZone("", dt.offset)
	}
	//: the instant.
	return time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.nanosecond, zone)
}

// scalar decodes the scalar node n into v: into a kind that holds it, else
// through the type's UnmarshalText, else refused — the precedence the replaced
// library had, so a string-kinded type with an UnmarshalText is still set
// directly from a TOML string.
func (d *decoder) scalar(n int32, v reflect.Value) error {
	node := &d.p.nodes[n]
	info := infoOf(v.Type())
	//: a date or a time goes into time.Time or a local type, and nothing else.
	if node.kind >= kindDateTime {
		//: assigned, or refused.
		return d.temporal(n, v)
	}
	//: a kind that holds the value: assigned, or refused for its range.
	if held, err := d.native(n, v); held {
		//: nil, or the range refusal.
		return err
	}
	//: a type that reads its own text reads the string, or the token.
	if info.is(typeTextUnmarshaler) {
		//: through UnmarshalText.
		return d.unmarshalText(n, v, d.p.textBytes(node))
	}
	//: nothing else holds it.
	return d.fail(n, problemMismatch, v.Type())
}

// native assigns a string, an integer, a float or a boolean to v when v's
// kind holds it, and reports whether it did: a value out of the kind's range
// is refused there; any other kind is left to the caller.
func (d *decoder) native(n int32, v reflect.Value) (held bool, err error) {
	node := &d.p.nodes[n]
	switch node.kind {
	//: a string.
	case kindString:
		return d.setString(n, v), nil
	//: an integer.
	case kindInteger:
		return d.setInteger(n, v)
	//: a float.
	case kindFloat:
		return d.setFloat(n, v, math.Float64frombits(node.num))
	//: a boolean.
	case kindBool:
		return d.setBool(n, v), nil
	//: a date or a time: the special types, handled by the caller.
	default:
		return false, nil
	}
}

// setString assigns a string to a string kind, and reports whether v was one.
func (d *decoder) setString(n int32, v reflect.Value) bool {
	//: only a string holds a string.
	if v.Kind() != reflect.String {
		//: not this kind.
		return false
	}
	v.SetString(string(d.p.textBytes(&d.p.nodes[n])))
	//: assigned.
	return true
}

// setInteger assigns an integer to a signed, unsigned or float kind, and
// reports whether v was one; a value out of the kind's range is refused.
func (d *decoder) setInteger(n int32, v reflect.Value) (held bool, err error) {
	i := int64(d.p.nodes[n].num)
	switch v.Kind() {
	//: a signed integer of any width.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: wider than the field.
		if v.OverflowInt(i) {
			//: refused.
			return true, d.fail(n, problemOverflow, v.Type())
		}
		v.SetInt(i)
	//: an unsigned integer of any width.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: negative, or wider than the field.
		if i < 0 || v.OverflowUint(uint64(i)) {
			//: refused.
			return true, d.fail(n, problemOverflow, v.Type())
		}
		v.SetUint(uint64(i))
	//: a float holds an integer, as the previous library allowed.
	case reflect.Float32, reflect.Float64:
		return d.setFloat(n, v, float64(i))
	//: not an integer kind.
	default:
		return false, nil
	}
	//: assigned.
	return true, nil
}

// setFloat assigns f to a float kind, and reports whether v was one; a finite
// value float32 cannot hold is refused.
func (d *decoder) setFloat(n int32, v reflect.Value, f float64) (held bool, err error) {
	//: only a float holds a float.
	if v.Kind() != reflect.Float32 && v.Kind() != reflect.Float64 {
		//: not this kind.
		return false, nil
	}
	//: a finite value beyond float32's range would become an infinity.
	if v.Kind() == reflect.Float32 && !math.IsInf(f, 0) && math.Abs(f) > math.MaxFloat32 {
		//: refused.
		return true, d.fail(n, problemOverflow, v.Type())
	}
	v.SetFloat(f)
	//: assigned.
	return true, nil
}

// setBool assigns a boolean to a bool kind, and reports whether v was one.
func (d *decoder) setBool(n int32, v reflect.Value) bool {
	//: only a bool holds a boolean.
	if v.Kind() != reflect.Bool {
		//: not this kind.
		return false
	}
	v.SetBool(d.p.nodes[n].num == 1)
	//: assigned.
	return true
}

// temporal decodes a date or time node into time.Time or one of the local
// types. A local value goes into a time.Time in time.Local; an offset
// date-time goes into time.Time only, since a local type would drop its
// offset.
func (d *decoder) temporal(n int32, v reflect.Value) error {
	node := &d.p.nodes[n]
	dt := &d.p.times[node.num]
	var out any
	switch t := v.Type(); {
	//: an instant takes any of the four.
	case t == timeType:
		out = d.asTime(node.kind, dt)
	//: a local type takes its own kind only.
	case node.kind != kindDateTime && t == localTypeOf(node.kind):
		out = d.localAny(node)
	//: an offset date-time into a local type, a mismatched local type, or any
	//: other type — not even through UnmarshalText, which the replaced
	//: library never offered a date.
	default:
		return d.fail(n, problemMismatch, t)
	}
	v.Set(reflect.ValueOf(out))
	//: decoded.
	return nil
}

// asTime returns a date or time of kind k as a time.Time: an offset date-time
// at its offset, a local one in time.Local, a local time on January 1st of
// year 0.
func (d *decoder) asTime(k kind, dt *datetime) time.Time {
	switch k {
	//: an instant.
	case kindDateTime:
		return instantOf(dt)
	//: a local time has no date.
	case kindLocalTime:
		return time.Date(0, time.January, 1, dt.hour, dt.minute, dt.second, dt.nanosecond, localZone)
	//: a local date or date-time, in time.Local.
	default:
		return time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.nanosecond, localZone)
	}
}

// localTypeOf returns the local type a kind decodes to.
func localTypeOf(k kind) reflect.Type {
	switch k {
	//: a calendar day.
	case kindLocalDate:
		return localDateType
	//: a time of day.
	case kindLocalTime:
		return localTimeType
	//: a date and a time of day.
	default:
		return localDateTimeType
	}
}

// wrapFail returns UNMARSHAL_FAILED for node n with the error a target's own
// UnmarshalText returned underneath.
func (d *decoder) wrapFail(n int32, cause error, target reflect.Type) error {
	line, _ := position(d.p.data, int(d.p.nodes[n].at))
	//: the sentinel's code and messages over the cause.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeTOMLUnmarshalFailed,
		Reason:  UnmarshalFailed.Reason(),
		Public:  UnmarshalFailed.Public(),
		Private: privateTextRefused,
	}, errs.String(fieldProblem, problemText), errs.String(fieldKey, d.keyPath(n)),
		errs.Int(fieldLine, line), errs.String(fieldType, target.String()))
}
