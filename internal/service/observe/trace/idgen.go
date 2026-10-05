package trace

import (
	"crypto/rand"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// NewTraceID draws a fresh 16-byte trace identifier.
//
// Every byte is random. The W3C specification says a trace-id "SHOULD be
// globally unique" and that "the ID SHOULD be generated with a random
// distribution", and it warns against schemes that put structure in the id: a
// backend may sample on the id's bits, so an id with a constant prefix samples
// its whole prefix in or out together.
//
// It RETRIES on the astronomically unlikely all-zero draw rather than returning
// it, because the all-zero id is the specification's invalid value: emitting one
// would produce a traceparent every conforming receiver is required to ignore,
// which is a trace that silently does not exist.
//
// The error is always nil. The bytes come from crypto/rand.Read, which since
// Go 1.24 never returns an error and always fills its buffer — a failing source
// crashes the program instead — and the signature keeps its error because it
// is published (pkg/v1/observe/trace), as is EntropyFailed, which nothing
// returns any more.
func NewTraceID() (id coretrace.TraceID, err error) {
	//: the draw cannot fail; the error is the published signature's.
	return drawTraceID(), nil
}

// drawTraceID is NewTraceID without the error it never returns — what the
// tracer mints a root span's trace id with.
func drawTraceID() coretrace.TraceID {
	//: draw, and redraw on the one value that is not a usable identifier.
	for {
		//: fill the whole array from the CSPRNG, which cannot fail.
		var drawn coretrace.TraceID
		_, _ = rand.Read(drawn[:])
		//: 2^-128 says this loop runs once.
		if drawn.IsValid() {
			//: a usable trace identifier.
			return drawn
		}
	}
}

// NewSpanID draws a fresh 8-byte span identifier, redrawing the all-zero value
// for the reason NewTraceID does — the all-zero span-id additionally spells
// "no parent", so emitting one would turn a child into a root. Its error is
// always nil, for the reason NewTraceID's is.
func NewSpanID() (id coretrace.SpanID, err error) {
	//: the draw cannot fail; the error is the published signature's.
	return drawSpanID(), nil
}

// drawSpanID is NewSpanID without the error it never returns — what the tracer
// mints every span's id with.
func drawSpanID() coretrace.SpanID {
	//: draw, and redraw on the one value that is not a usable identifier.
	for {
		//: fill the whole array from the CSPRNG, which cannot fail.
		var drawn coretrace.SpanID
		_, _ = rand.Read(drawn[:])
		//: 2^-64 says this loop runs once.
		if drawn.IsValid() {
			//: a usable span identifier.
			return drawn
		}
	}
}
