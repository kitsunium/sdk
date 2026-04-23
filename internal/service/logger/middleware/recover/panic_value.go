// Package recover: panic_value.go declares the panicValue adapter that
// converts a recover() return value (any) into the error interface so
// errs.Wrap can carry it without losing the original Stringer behaviour.
package recover

import "fmt"

// panicValue adapts a recovered panic value (any) into the error interface
// so errs.Wrap can carry it without spilling the verbatim rendering into
// the Source() chain. The value's rich stringification lives in the wrapped
// error's Private field via safeString, NOT in this Error() method.
type panicValue struct {
	// v is the verbatim recover() return value, retained for introspection.
	v any
}

// Error renders a type-only description of the panic. Consumers that log
// the unwrap chain (fmt.Printf("%+v", err) at the edge) therefore see only
// "panic of type <T>" — never the original panic value, which may contain
// internal state that must not leak via the Source() channel. The rich
// %v rendering is preserved in the wrapping *errs.Error's Private field.
//
// Returns:
//   - msg: a "panic of type <T>" rendering where T is the Go type of v.
func (p panicValue) Error() (msg string) {
	//: expose only the Go type — the full Stringer rendering lives in Private.
	return fmt.Sprintf("panic of type %T", p.v)
}

// safeString renders v via fmt.Sprintf("%v", v) with an inner defer/recover
// so a Stringer that panics during rendering does NOT propagate out of the
// recover middleware's outer recover() block. A panic inside a deferred
// function would otherwise bypass the outer recover and crash the producer.
//
// Params:
//   - v: the value to render; typically a recover() return value.
//
// Returns:
//   - s: the rendered string, or "<panic in String(): ...>" on inner panic.
func safeString(v any) (s string) {
	//: inner guard so a meta-panic in v's Stringer degrades gracefully.
	defer func() {
		//: substitute a diagnostic marker when rendering itself panics.
		if r := recover(); r != nil {
			//: expose the meta-panic type only; never its verbatim value.
			s = fmt.Sprintf("<panic in String(): %T>", r)
		}
	}()
	//: happy path — honour the value's Stringer when it behaves.
	return fmt.Sprintf("%v", v)
}

// safeTypeName renders v's dynamic Go type via fmt.Sprintf("%T", v). The
// "%T" verb reads the runtime type descriptor — never calls a user method —
// so it cannot panic. The helper exists for symmetry with safeString and
// to document intent at the Wrap call site.
//
// Params:
//   - v: the value whose type name is wanted; typically a recover() value.
//
// Returns:
//   - name: the Go type name (e.g. "*errors.errorString", "main.myErr").
func safeTypeName(v any) (name string) {
	//: reflection on dynamic type never invokes user code, cannot panic.
	return fmt.Sprintf("%T", v)
}
