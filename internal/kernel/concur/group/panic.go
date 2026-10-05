package group

import (
	"runtime/debug"
	"strings"
)

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
	out.WriteString("kernel/concur/group: panic in task: ")
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
