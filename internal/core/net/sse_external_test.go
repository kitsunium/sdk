// Package net_test — the Server-Sent Events frame as a value: what the format
// can carry, and when a frame carries nothing. Its wire form is
// internal/service/net/sse's, and so are the encoder's tests (ADR 0160 §4).
package net_test

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_SSEEventValue_Validate pins what the format cannot carry.
//
// Every case here is something an encoder could plausibly "fix" — truncate the
// id at the newline, take the absolute value of a negative retry, round a
// sub-millisecond delay down to zero — and every one of those fixes produces a
// frame the peer accepts and misreads. A truncated id resumes from the wrong
// place; "retry: 0" tells the client to reconnect immediately, which turns a
// small backoff into a hot loop against a server that is probably already
// struggling. Refusal is the only honest answer. That the encoder then leaves
// its buffer untouched is internal/service/net/sse's to prove (ADR 0160 §4).
func Test_SSEEventValue_Validate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		event corenet.SSEEventValue
	}
	tests := []tc{
		{name: "a newline in the id", event: corenet.SSEEventValue{ID: "4\n2", Data: "x"}},
		{name: "a carriage return in the id", event: corenet.SSEEventValue{ID: "4\r2", Data: "x"}},
		{name: "a newline in the event name", event: corenet.SSEEventValue{Name: "ti\nck", Data: "x"}},
		{name: "a carriage return in the event name", event: corenet.SSEEventValue{Name: "ti\rck", Data: "x"}},
		{name: "a negative retry", event: corenet.SSEEventValue{Data: "x", Retry: corenet.DurationValue(-time.Second)}},
		{name: "a retry under one millisecond", event: corenet.SSEEventValue{Data: "x", Retry: corenet.DurationValue(500 * time.Microsecond)}},
		{name: "an event name with no data dispatches nothing", event: corenet.SSEEventValue{Name: "tick"}},
		{name: "a frame with no field at all", event: corenet.SSEEventValue{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the refusal must be typed, so a handler can tell a malformed event
		//: from a dead peer without string matching.
		if err := c.event.Validate(); !errs.HasCode(err, corenet.CodeSSEFieldInvalid) {
			t.Fatalf("Validate() = %v, want SSE_FIELD_INVALID", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_SSEEventValue_ValidateAccepts pins the other half: every frame the
// format CAN carry passes, including the two that carry no data at all — an
// id-only frame advances the client's cursor and a retry-only frame sets its
// reconnection policy, and refusing either would take a capability away.
func Test_SSEEventValue_ValidateAccepts(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		event corenet.SSEEventValue
	}
	tests := []tc{
		{name: "data alone", event: corenet.SSEEventValue{Data: "x"}},
		{name: "a multi-line payload, which splits rather than fails", event: corenet.SSEEventValue{Data: "one\r\ntwo\nthree\r"}},
		{name: "every field", event: corenet.SSEEventValue{ID: "42", Name: "tick", Data: "x", Retry: corenet.DurationValue(time.Second)}},
		{name: "an id alone moves the cursor", event: corenet.SSEEventValue{ID: "cursor-9"}},
		{name: "a retry alone is the reconnection policy", event: corenet.SSEEventValue{Retry: corenet.DurationValue(corenet.SSEMinRetry)}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if err := c.event.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_SSEEventValue_IsZero pins the emptiness test the refusal above rests on.
func Test_SSEEventValue_IsZero(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		event corenet.SSEEventValue
		want  bool
	}
	tests := []tc{
		{name: "the zero value", event: corenet.SSEEventValue{}, want: true},
		{name: "an id alone is a frame", event: corenet.SSEEventValue{ID: "1"}},
		{name: "a name alone is a frame", event: corenet.SSEEventValue{Name: "tick"}},
		{name: "data alone is a frame", event: corenet.SSEEventValue{Data: "x"}},
		{name: "a retry alone is a frame", event: corenet.SSEEventValue{Retry: corenet.DurationValue(time.Second)}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.event.IsZero(); got != c.want {
			t.Fatalf("IsZero() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
