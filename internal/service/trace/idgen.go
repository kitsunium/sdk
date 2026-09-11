// Package trace — minting the two identifiers.
package trace

import (
	"crypto/rand"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
func NewTraceID() (id coretrace.TraceID, err error) {
	//: draw, and redraw on the one value that is not a usable identifier.
	for {
		//: fill the whole array from the CSPRNG.
		var drawn coretrace.TraceID
		//: a CSPRNG fault is reported typed, never silently retried.
		if readErr := readRandom(drawn[:]); readErr != nil {
			//: surface the wrapped entropy failure.
			return coretrace.TraceID{}, readErr
		}
		//: 2^-128 says this loop runs once.
		if drawn.IsValid() {
			//: a usable trace identifier.
			return drawn, nil
		}
	}
}

// NewSpanID draws a fresh 8-byte span identifier, redrawing the all-zero value
// for the reason NewTraceID does — the all-zero span-id additionally spells
// "no parent", so emitting one would turn a child into a root.
func NewSpanID() (id coretrace.SpanID, err error) {
	//: draw, and redraw on the one value that is not a usable identifier.
	for {
		//: fill the whole array from the CSPRNG.
		var drawn coretrace.SpanID
		//: a CSPRNG fault is reported typed, never silently retried.
		if readErr := readRandom(drawn[:]); readErr != nil {
			//: surface the wrapped entropy failure.
			return coretrace.SpanID{}, readErr
		}
		//: 2^-64 says this loop runs once.
		if drawn.IsValid() {
			//: a usable span identifier.
			return drawn, nil
		}
	}
}

// readRandom fills b with cryptographically-secure random bytes, wrapping any
// CSPRNG fault in the EntropyFailed sentinel.
func readRandom(b []byte) error {
	//: crypto/rand.Read fills b fully or returns an error (never short).
	_, err := rand.Read(b)
	//: success fast-path.
	if err == nil {
		//: b now holds len(b) secure random bytes.
		return nil
	}
	//: wrap the CSPRNG fault with the dotted-quad entropy code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    CodeEntropyFailed,
		Reason:  "ENTROPY_FAILED",
		Public:  "Trace identifier generation failed to read secure random bytes",
		Private: "service/trace: crypto/rand.Read returned an error",
	})
}
