// Package plugin answers one question a process-wide registry must ask before
// it publishes anything: is this value usable as an entry at all?
//
// A registry stores values behind an interface and hands them back to every
// caller for the life of the process. Two shapes satisfy such an interface at
// compile time and cannot serve:
//
//   - a TYPED nil — (*gzipCompressor)(nil) is not == nil, so the guard every
//     registrar writes lets it through, it is stored, and every lookup returns
//     it. The failure surfaces at the first dispatch, arbitrarily far from the
//     registration that caused it;
//   - a value whose dynamic type is NOT COMPARABLE — a struct holding a slice,
//     a map or a function. Registries compare entries to tell an idempotent
//     re-registration from a conflict, and == on such a value is a runtime
//     panic naming Go's comparison rather than the duplicate plug-in.
//
// Both are programming errors at import time, which is why the answer is a
// reason string rather than an error: the registrar panics with its OWN
// dotted-quad code and reason, and only borrows the sentence that says why.
package plugin

import "reflect"

// Unusable reports why v cannot be published into a registry, or the empty
// string when it can. The reason is a fragment meant to follow a colon in the
// caller's own message, e.g. "nil *gzip.compressor".
//
// It is deliberately not a predicate returning bool: a registrar that refuses
// must say which of the two shapes it refused, because "nil Compressor" and
// "Compressor is not comparable" are found by different searches and fixed in
// different places.
//
// The parameter is a type PARAMETER rather than an `any`, which costs the
// caller nothing — every registrar passes its port and T is inferred — and
// keeps the signature from claiming to accept anything in particular. What is
// judged is the dynamic value either way: reflect sees through the boxing.
func Unusable[T any](v T) string {
	//: rv is the concrete value behind the interface; t names its type in
	//: every reason, because the registrar's message names only the port.
	rv := reflect.ValueOf(v)
	//: an untyped nil never carried a plug-in to begin with — reflect reports
	//: it as the invalid Value, which no other input produces.
	if !rv.IsValid() {
		//: nothing to name.
		return "nil"
	}
	t := rv.Type()
	//: only these kinds can be nil while their interface is not.
	switch rv.Kind() {
	//: the nilable kinds, in one arm because the answer is the same for all.
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer,
		reflect.Slice, reflect.UnsafePointer, reflect.Interface:
		//: a typed nil satisfies the port and panics at the first dispatch.
		if rv.IsNil() {
			//: named, so the offending import is identifiable.
			return "nil " + t.String()
		}
	//: every other kind is a value that cannot be nil at all.
	default:
		//: nothing to check here; comparability is asked of every kind below.
	}
	//: comparability is the registry's own mechanism, not a taste: the
	//: duplicate check is ==, and it panics rather than reporting the conflict.
	if !t.Comparable() {
		//: named for the same reason.
		return t.String() + " is not comparable"
	}
	//: usable.
	return ""
}
