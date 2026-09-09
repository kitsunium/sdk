// Package group — the panic carried off a task's goroutine and re-raised in
// the waiter.
package group

import (
	"runtime/debug"
	"strings"
)

// PanicValue carries a panic raised inside a task across the goroutine
// boundary to whoever is waiting on the group.
//
// It is deliberately NOT an error. A panic is a programming fault, and turning
// one into a value the caller may ignore is how a broken invariant becomes a
// silent wrong answer — the same position kernel/singleflight takes, and the
// reason neither type carries an error code.
//
// The stack is captured at the moment of recovery, inside the goroutine that
// ran the task. Without it a re-raised panic would point at the waiter's
// stack — code that did nothing wrong — and the frame that actually failed
// would be gone, which is the failure mode that makes a crashed worker pool so
// expensive to debug.
//
// It is a separate type from singleflight's namesake rather than a shared one:
// each names its own package in the message a crash prints, and hoisting the
// two into a fourth kernel package would add a primitive whose entire content
// is two fields.
type PanicValue struct {
	// Raised is the value the original panic carried, verbatim.
	Raised any
	// Stack is the stack of the goroutine that ran the task, captured at
	// recovery.
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
// value has one, so an uncaught re-raise shows the task's stack rather than
// only the waiter's.
func (p PanicValue) String() string {
	//: build once: the header, the original value, then the captured stack.
	var out strings.Builder
	out.WriteString("kernel/group: panic in task: ")
	//: %v-equivalent for the common string/error payloads without pulling fmt
	//: into a kernel package for a path that runs once per process crash.
	out.WriteString(renderRaised(p.Raised))
	out.WriteString("\noriginating goroutine stack:\n")
	out.Write(p.Stack)
	//: the assembled message.
	return out.String()
}

// renderRaised turns the recovered value into text, preferring the payload's
// own rendering when it has one.
func renderRaised(raised any) string {
	//: the shapes panic payloads actually take in practice.
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
