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

// scalar decodes the scalar node n into v.
func (d *decoder) scalar(n int32, v reflect.Value) error {
	node := &d.p.nodes[n]
	info := infoOf(v.Type())
	//: time.Time and the local types take a date or time as it is.
	if info.is(typeSpecial) && node.kind >= kindDateTime {
		//: assigned, or refused.
		return d.temporal(n, v)
	}
	//: a type that reads its own text reads the value's.
	if info.is(typeTextUnmarshaler) && v.Kind() != reflect.Interface {
		//: the string's value, or the token as written.
		return d.unmarshalText(n, v, d.p.textBytes(node))
	}
	switch node.kind {
	//: a string.
	case kindString:
		return d.setString(n, v)
	//: an integer.
	case kindInteger:
		return d.setInteger(n, v)
	//: a float.
	case kindFloat:
		return d.setFloat(n, v, math.Float64frombits(node.num))
	//: a boolean.
	case kindBool:
		return d.setBool(n, v)
	//: a date or a time into anything but the special types.
	default:
		return d.fail(n, problemMismatch, v.Type())
	}
}

// setString decodes a string into a string kind.
func (d *decoder) setString(n int32, v reflect.Value) error {
	//: only a string holds a string.
	if v.Kind() != reflect.String {
		//: refused.
		return d.fail(n, problemMismatch, v.Type())
	}
	v.SetString(string(d.p.textBytes(&d.p.nodes[n])))
	//: decoded.
	return nil
}

// setInteger decodes an integer into a signed, unsigned or float kind that
// holds its value.
func (d *decoder) setInteger(n int32, v reflect.Value) error {
	i := int64(d.p.nodes[n].num)
	switch v.Kind() {
	//: a signed integer of any width.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: wider than the field.
		if v.OverflowInt(i) {
			//: refused.
			return d.fail(n, problemOverflow, v.Type())
		}
		v.SetInt(i)
	//: an unsigned integer of any width.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: negative, or wider than the field.
		if i < 0 || v.OverflowUint(uint64(i)) {
			//: refused.
			return d.fail(n, problemOverflow, v.Type())
		}
		v.SetUint(uint64(i))
	//: a float holds an integer, as the previous library allowed.
	case reflect.Float32, reflect.Float64:
		return d.setFloat(n, v, float64(i))
	//: nothing else.
	default:
		return d.fail(n, problemMismatch, v.Type())
	}
	//: decoded.
	return nil
}

// setFloat decodes f into a float kind, refusing a finite value float32
// cannot hold.
func (d *decoder) setFloat(n int32, v reflect.Value, f float64) error {
	//: only a float holds a float.
	if v.Kind() != reflect.Float32 && v.Kind() != reflect.Float64 {
		//: refused.
		return d.fail(n, problemMismatch, v.Type())
	}
	//: a finite value beyond float32's range would become an infinity.
	if v.Kind() == reflect.Float32 && !math.IsInf(f, 0) && math.Abs(f) > math.MaxFloat32 {
		//: refused.
		return d.fail(n, problemOverflow, v.Type())
	}
	v.SetFloat(f)
	//: decoded.
	return nil
}

// setBool decodes a boolean into a bool kind.
func (d *decoder) setBool(n int32, v reflect.Value) error {
	//: only a bool holds a boolean.
	if v.Kind() != reflect.Bool {
		//: refused.
		return d.fail(n, problemMismatch, v.Type())
	}
	v.SetBool(d.p.nodes[n].num == 1)
	//: decoded.
	return nil
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
	//: an offset date-time into a local type, or a mismatched local type.
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
