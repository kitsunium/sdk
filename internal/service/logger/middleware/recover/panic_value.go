// Package recover — declares the panicValue adapter that
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
func (p panicValue) Error() string {
	//: expose only the Go type — the full Stringer rendering lives in Private.
	return fmt.Sprintf("panic of type %T", p.v)
}

// safeString renders v via fmt.Sprintf("%v", v) with an inner defer/recover
// so a Stringer that panics during rendering does NOT propagate out of the
// recover middleware's outer recover() block. A panic inside a deferred
// function would otherwise bypass the outer recover and crash the producer.
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
func safeTypeName(v any) string {
	//: reflection on dynamic type never invokes user code, cannot panic.
	return fmt.Sprintf("%T", v)
}
