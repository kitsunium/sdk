// Package singleflight — the panic carried out of the shared call into every
// waiter.
package singleflight

import (
	"runtime/debug"
	"strings"
)

// PanicValue carries a panic raised inside a deduplicated call across the
// goroutine boundary to every caller waiting on it.
//
// It is deliberately NOT an error. A panic is a programming fault, and
// converting one into a value the caller may ignore is how a broken invariant
// becomes a silent wrong answer; it is also why this type carries no error
// code (the kernel's cache and clock primitives take the same position — a
// programmer error is not a runtime condition with a dotted quad).
//
// The stack is captured at the moment of recovery, inside the goroutine that
// ran fn. Without it a re-raised panic would point at the waiter's stack —
// code that did nothing wrong — and the frame that actually failed would be
// gone.
type PanicValue struct {
	// Raised is the value the original panic carried, verbatim.
	Raised any
	// Stack is the stack of the goroutine that ran fn, captured at recovery.
	Stack []byte
}

// newPanicValue captures raised together with the stack of the goroutine that
// is unwinding.
func newPanicValue(raised any) *PanicValue {
	//: debug.Stack must be called here, while the failing goroutine is still
	//: the current one — a stack taken later belongs to the wrong frame.
	return &PanicValue{Raised: raised, Stack: debug.Stack()}
}

// String renders the original panic followed by the stack of the goroutine
// that raised it. The Go runtime prints a panic value through String when the
// value has one, so an uncaught re-raise shows fn's stack rather than only the
// waiter's.
func (p PanicValue) String() string {
	//: build once: the header, the original value, then the captured stack.
	var out strings.Builder
	out.WriteString("singleflight: panic in deduplicated call: ")
	//: %v-equivalent for the common string/error payloads without pulling fmt
	//: into a hot kernel package for a path that runs once per process crash.
	out.WriteString(renderRaised(p.Raised))
	out.WriteString("\noriginating goroutine stack:\n")
	out.Write(p.Stack)
	//: the assembled message.
	return out.String()
}

// renderRaised turns the recovered value into text, preferring the payload's
// own rendering when it has one.
func renderRaised(raised any) string {
	//: the two shapes panic payloads actually take in practice.
	switch typed := raised.(type) {
	//: the most common payload — panic("some message").
	case string:
		//: already text.
		return typed
	//: the second most common — panic(err) from a re-raised failure.
	case error:
		//: an error knows how to say what it is.
		return typed.Error()
	//: anything that can render itself, including this package's own value.
	case interface{ String() string }:
		//: so does a Stringer.
		return typed.String()
	}
	//: anything else is described by its shape rather than guessed at; the
	//: caller still has PanicValue.Raised for the exact value.
	return "non-textual panic value (see PanicValue.Raised)"
}
