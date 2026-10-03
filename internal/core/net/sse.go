// Package net — the Server-Sent Events frame: the value a stream sends and the
// rules a frame must satisfy to be carried at all. Writing it — the encoder
// that splits Data into data lines, and the comment frame — is
// internal/service/net/sse's (ADR 0160 §4).
package net

import (
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// SSEContentType is the media type an event stream must be served under. A
// client that receives anything else does not start an EventSource at all.
const SSEContentType string = "text/event-stream"

// SSEMinRetry is the smallest reconnection hint the wire can carry. The retry
// field is an integer MILLISECOND count, so anything shorter rounds to zero —
// and zero does not mean "a very short delay", it means "reconnect at once".
const SSEMinRetry time.Duration = time.Millisecond

// SSELastEventIDHeader is the request header a reconnecting client sends,
// carrying the id of the last event it processed.
const SSELastEventIDHeader string = "Last-Event-ID"

// SSEEventValue is one Server-Sent Events frame.
//
// The format has no escape mechanism. A line terminator inside a value is not
// quoted, it SPLITS the value: that is exactly what makes a multi-line payload
// expressible — Data is emitted as one "data:" line per line and the client
// rejoins them with "\n" — and exactly what makes a line terminator inside ID
// or Name unrepresentable. Those are refused rather than truncated, because a
// silently shortened id is a resume token pointing at the wrong place.
type SSEEventValue struct {
	// ID is the frame's "id" field. A client stores the last non-empty id it
	// saw and sends it back as Last-Event-ID when it reconnects, so this is the
	// resume token — and it is the CALLER's to mint, because only the caller
	// knows what it means. Empty emits no id field, which leaves the client's
	// stored id untouched; that is the format's own behaviour, not an omission.
	ID string `json:"id"`
	// Name is the frame's "event" field — the event type the client dispatches
	// under. Empty means the default type, "message".
	Name string `json:"event"`
	// Data is the payload. Every line terminator it carries (LF, CR or CRLF)
	// becomes a separate "data:" line, which the client rejoins with "\n". A
	// payload that mixed terminators therefore arrives normalised to LF: the
	// value is preserved, its byte spelling is not.
	Data string `json:"data"`
	// Retry is the reconnection delay hint, emitted as an integer millisecond
	// count. Zero emits no retry field.
	Retry DurationValue `json:"retry"`
}

// IsZero reports whether the frame carries no field at all.
func (e SSEEventValue) IsZero() bool {
	//: every field empty means there is nothing to serialise.
	return e.ID == "" && e.Name == "" && e.Data == "" && e.Retry == 0
}

// Validate reports whether the frame can be carried by the wire format.
//
// It is separate from the encoder (internal/service/net/sse.AppendEvent) so a
// caller can check a frame before committing to send it, and so the encoder
// can validate everything BEFORE it appends anything — a half-written frame on
// a stream is unrecoverable, since the peer has already read it.
func (e SSEEventValue) Validate() error {
	//: a frame with no field at all serialises to a bare blank line, which the
	//: client reads as an empty dispatch. That is never what a caller meant.
	if e.IsZero() {
		//: refuse the empty frame rather than emit a no-op.
		return errs.Wrap(SSEFieldInvalid, errs.WrapParams{},
			errs.String("why", "the event carries no field at all"))
	}
	//: id and event are single-line fields; a terminator inside either would
	//: split it into a different value plus a stray field.
	if err := validateSSELine("id", e.ID); err != nil {
		//: the error already names the offending field.
		return err
	}
	//: the same rule, for the event type.
	if err := validateSSELine("event", e.Name); err != nil {
		//: the error already names the offending field.
		return err
	}
	//: an event type with no data dispatches NOTHING: the client discards a
	//: frame whose data buffer is empty, and discards the event type with it.
	//: A caller who names an event expects it to arrive, so this is refused
	//: rather than silently dropped on the far side. Use a comment for a
	//: payload-free keep-alive.
	if e.Name != "" && e.Data == "" {
		//: refuse the frame the client would ignore.
		return errs.Wrap(SSEFieldInvalid, errs.WrapParams{},
			errs.String("field", "event"),
			errs.String("why", "an event name without data dispatches nothing"))
	}
	//: the retry hint is the last thing to check.
	return validateSSERetry(e.Retry.Duration())
}

// validateSSELine refuses a line terminator in a field the format cannot split.
func validateSSELine(field, value string) error {
	//: LF and CR both terminate a line in this format, so both are fatal here.
	//: Two byte searches rather than ContainsAny: both terminators are ASCII,
	//: so a UTF-8 continuation byte is never one of them and the two forms
	//: agree on every input — while ContainsAny either builds a 32-byte ASCII
	//: set per call or, for a value of eight bytes or fewer, decodes a rune per
	//: byte. IndexByte is the assembly-backed primitive. Measured in BENCH.md;
	//: the same substitution is made for the same reason in
	//: internal/service/proc/systemd/notify, and for a comment's single line
	//: in internal/service/net/sse.
	//: The empty test is not redundant with them, it is the row that made the
	//: substitution a win instead of a wash: Validate calls this for id AND
	//: event whether the frame carries them or not, and on the empty string
	//: ContainsAny returns without looking at anything while two IndexByte
	//: calls still happen. Guarding it took a data-only Validate from 26.2 ns
	//: back to 12.4 against ContainsAny's 20.9, and costs nothing measurable
	//: where the value IS present.
	if value != "" && (strings.IndexByte(value, '\n') >= 0 || strings.IndexByte(value, '\r') >= 0) {
		//: refuse rather than truncate — see the type's doc comment.
		return errs.Wrap(SSEFieldInvalid, errs.WrapParams{},
			errs.String("field", field),
			errs.String("why", "the value carries a line terminator, which this field cannot express"))
	}
	//: representable.
	return nil
}

// validateSSERetry refuses a reconnection hint the millisecond field cannot
// carry honestly.
func validateSSERetry(retry time.Duration) error {
	//: a negative delay has no wire form and no meaning.
	if retry < 0 {
		//: refuse rather than take its absolute value.
		return errs.Wrap(SSEFieldInvalid, errs.WrapParams{},
			errs.String("field", "retry"),
			errs.String("why", "the reconnection delay is negative"))
	}
	//: a sub-millisecond delay would serialise to "retry: 0", which does not
	//: mean "almost no delay" — it tells the client to reconnect immediately,
	//: turning a small backoff into a hot loop against a server that is
	//: probably already struggling.
	if retry > 0 && retry < SSEMinRetry {
		//: refuse rather than round a backoff down to none.
		return errs.Wrap(SSEFieldInvalid, errs.WrapParams{},
			errs.String("field", "retry"),
			errs.Int64("nanoseconds", int64(retry)),
			errs.String("why", "the wire field is an integer millisecond count"))
	}
	//: representable.
	return nil
}
