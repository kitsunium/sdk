// Package net_test — the Server-Sent Events frame and its wire form.
package net_test

import (
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_SSEEventValue_AppendTo pins the wire form field by field, and above all
// pins the one property that makes the format survivable: a newline inside a
// value is not escaped, it SPLITS the value into another data line. A client
// rejoins those lines with "\n", so the round trip is exact — but only if the
// encoder splits on every terminator the format recognises, LF, CR and CRLF
// alike. Treating CRLF as two terminators emits a spurious empty line between
// every pair, which is the bug this table exists to catch.
func Test_SSEEventValue_AppendTo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		event corenet.SSEEventValue
		want  string
	}
	tests := []tc{
		{
			name:  "data only takes the default event type",
			event: corenet.SSEEventValue{Data: "hello"},
			want:  "data: hello\n\n",
		},
		{
			name:  "a named event carries id, name and data in that order",
			event: corenet.SSEEventValue{ID: "42", Name: "tick", Data: "hello"},
			want:  "id: 42\nevent: tick\ndata: hello\n\n",
		},
		{
			name:  "a multi-line payload becomes one data line per line",
			event: corenet.SSEEventValue{Data: "one\ntwo\nthree"},
			want:  "data: one\ndata: two\ndata: three\n\n",
		},
		{
			name:  "CRLF is one terminator, not two",
			event: corenet.SSEEventValue{Data: "one\r\ntwo"},
			want:  "data: one\ndata: two\n\n",
		},
		{
			name:  "a lone CR terminates a line too",
			event: corenet.SSEEventValue{Data: "one\rtwo"},
			want:  "data: one\ndata: two\n\n",
		},
		{
			name:  "an embedded blank line survives as an empty data line",
			event: corenet.SSEEventValue{Data: "one\n\ntwo"},
			want:  "data: one\ndata:\ndata: two\n\n",
		},
		{
			name:  "a trailing newline yields a trailing empty data line",
			event: corenet.SSEEventValue{Data: "one\n"},
			want:  "data: one\ndata:\n\n",
		},
		{
			name:  "the retry hint is emitted in whole milliseconds",
			event: corenet.SSEEventValue{Data: "x", Retry: corenet.DurationValue(2500 * time.Millisecond)},
			want:  "retry: 2500\ndata: x\n\n",
		},
		{
			name:  "a sub-millisecond remainder truncates rather than rounding up",
			event: corenet.SSEEventValue{Data: "x", Retry: corenet.DurationValue(1500 * time.Microsecond)},
			want:  "retry: 1\ndata: x\n\n",
		},
		{
			name:  "an id-only frame carries no data line, so it dispatches nothing and only moves the cursor",
			event: corenet.SSEEventValue{ID: "cursor-9"},
			want:  "id: cursor-9\n\n",
		},
		{
			name:  "a retry-only frame is the reconnection policy on its own",
			event: corenet.SSEEventValue{Retry: corenet.DurationValue(5 * time.Second)},
			want:  "retry: 5000\n\n",
		},
		{
			name:  "a value that starts with a space keeps it, because exactly one is stripped",
			event: corenet.SSEEventValue{Data: "  padded"},
			want:  "data:   padded\n\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.event.AppendTo(nil)
		if err != nil {
			t.Fatalf("AppendTo() = %v, want nil", err)
		}
		if string(got) != c.want {
			t.Fatalf("AppendTo() wrote %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_SSEEventValue_AppendToRefusals pins what the format cannot carry.
//
// Every case here is something an encoder could plausibly "fix" — truncate the
// id at the newline, take the absolute value of a negative retry, round a
// sub-millisecond delay down to zero — and every one of those fixes produces a
// frame the peer accepts and misreads. A truncated id resumes from the wrong
// place; "retry: 0" tells the client to reconnect immediately, which turns a
// small backoff into a hot loop against a server that is probably already
// struggling. Refusal is the only honest answer, and dst must come back
// untouched so a partial frame never reaches a stream.
func Test_SSEEventValue_AppendToRefusals(t *testing.T) {
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
		dst := []byte("PRIOR")
		got, err := c.event.AppendTo(dst)
		//: the refusal must be typed, so a handler can tell a malformed event
		//: from a dead peer without string matching.
		if !errs.HasCode(err, corenet.CodeSSEFieldInvalid) {
			t.Fatalf("AppendTo() = %v, want SSE_FIELD_INVALID", err)
		}
		//: a partial frame on a stream is unrecoverable — the peer has already
		//: read it — so the buffer must come back exactly as it went in.
		if string(got) != "PRIOR" {
			t.Fatalf("AppendTo() wrote %q into the buffer, want it untouched", got)
		}
		if verr := c.event.Validate(); !errs.HasCode(verr, corenet.CodeSSEFieldInvalid) {
			t.Fatalf("Validate() = %v, want the same refusal AppendTo gave", verr)
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

// TestAppendSSEComment pins the keep-alive primitive: a comment is ignored by
// every client, which is exactly why it is the only traffic that can prove a
// stream is alive without being mistaken for an event. A terminator inside one
// would end the comment early and turn the remainder into fields the client
// obeys, so it is refused on the same terms as an id.
func TestAppendSSEComment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		text    string
		want    string
		wantErr bool
	}
	tests := []tc{
		{name: "a plain comment", text: "keep-alive", want: ": keep-alive\n\n"},
		{name: "an empty comment carries no trailing space", text: "", want: ":\n\n"},
		{name: "a newline would smuggle fields past the comment", text: "ok\ndata: injected", wantErr: true},
		{name: "a carriage return does the same", text: "ok\rdata: injected", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := corenet.AppendSSEComment(nil, c.text)
		if c.wantErr {
			//: the refusal must be typed for the same reason an event's is.
			if !errs.HasCode(err, corenet.CodeSSEFieldInvalid) {
				t.Fatalf("AppendSSEComment(%q) = %v, want SSE_FIELD_INVALID", c.text, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("AppendSSEComment(%q) = %v, want nil", c.text, err)
		}
		if string(got) != c.want {
			t.Fatalf("AppendSSEComment(%q) wrote %q, want %q", c.text, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSSEFrameSurvivesAClientSideReassembly is the round-trip the format
// promises: whatever a caller puts in Data, a client that splits on the
// terminators and joins the data lines with "\n" gets it back — with mixed
// terminators normalised to LF, which is the format's own doing and not a loss
// of the value.
func TestSSEFrameSurvivesAClientSideReassembly(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
		want string
	}
	tests := []tc{
		{name: "a single line", data: "hello", want: "hello"},
		{name: "two lines", data: "one\ntwo", want: "one\ntwo"},
		{name: "an embedded blank line", data: "one\n\ntwo", want: "one\n\ntwo"},
		{name: "CRLF normalises to LF", data: "one\r\ntwo", want: "one\ntwo"},
		{name: "a lone CR normalises to LF", data: "one\rtwo", want: "one\ntwo"},
		{name: "a trailing newline", data: "one\n", want: "one\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		wire, err := corenet.SSEEventValue{Data: c.data}.AppendTo(nil)
		if err != nil {
			t.Fatalf("AppendTo() = %v, want nil", err)
		}
		if got := reassemble(string(wire)); got != c.want {
			t.Fatalf("a client reassembling %q got %q, want %q", wire, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// reassemble is a minimal client-side data reassembler: it collects the data
// lines of one frame and joins them with "\n", exactly as the format specifies.
func reassemble(frame string) string {
	var values []string
	for line := range strings.SplitSeq(frame, "\n") {
		//: only data lines contribute to the value; everything else is
		//: metadata, a comment, or the blank line ending the frame.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		//: the client strips exactly one leading space after the colon.
		values = append(values, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
	return strings.Join(values, "\n")
}
