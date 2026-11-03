package bson

import "reflect"

// zeroTest returns omitzero's test for a field of type t — encoding/json's
// since Go 1.24 (#1103): the type's own IsZero when it declares one, reached
// through the value's address when only the pointer does, and reflect's zero
// value test otherwise. A nil pointer or interface is zero without asking it.
func zeroTest(t reflect.Type) func(reflect.Value) bool {
	//: who declares IsZero decides how it is reached.
	switch {
	//: a pointer or an interface declaring it: nil is zero.
	case (t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface) && t.Implements(typeOfZeroer):
		return zeroThroughReference
	//: on the value.
	case t.Implements(typeOfZeroer):
		return zeroByMethod
	//: on the value's address.
	case reflect.PointerTo(t).Implements(typeOfZeroer):
		return zeroByAddress
	//: every field, element or bit at its zero value.
	default:
		return reflect.Value.IsZero
	}
}

// zeroThroughReference is omitzero for a pointer or interface type declaring
// IsZero: nil, or an interface holding a nil pointer, is zero; otherwise the
// method decides.
func zeroThroughReference(v reflect.Value) bool {
	//: nothing to ask.
	if v.IsNil() {
		//: zero.
		return true
	}
	//: an interface holding a nil pointer cannot be asked either.
	if v.Kind() == reflect.Interface {
		//: the dynamic value.
		if elem := v.Elem(); elem.Kind() == reflect.Pointer && elem.IsNil() {
			//: zero.
			return true
		}
	}
	//: the method's answer.
	return zeroByMethod(v)
}

// zeroByMethod calls the value's own IsZero. A value reflection may not hand
// out — reached through an unexported embedding — is judged by its bits.
func zeroByMethod(v reflect.Value) bool {
	//: reflection refuses to hand the value out.
	if !v.CanInterface() {
		//: its bits.
		return v.IsZero()
	}
	z, ok := reflect.TypeAssert[interface{ IsZero() bool }](v)
	//: the type's answer, or its bits when it has no method after all.
	return (ok && z.IsZero()) || (!ok && v.IsZero())
}

// zeroByAddress calls IsZero through the value's address, or through an
// addressable copy when the value has none, as encoding/json does.
func zeroByAddress(v reflect.Value) bool {
	//: reflection refuses to hand the value out.
	if !v.CanInterface() {
		//: its bits.
		return v.IsZero()
	}
	//: a copy that has an address.
	if !v.CanAddr() {
		boxed := reflect.New(v.Type()).Elem()
		boxed.Set(v)
		v = boxed
	}
	//: through the pointer.
	return zeroByMethod(v.Addr())
}
