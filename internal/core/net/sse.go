// Package net — the Server-Sent Events frame and its wire form.
package net

import (
	"strconv"
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

// sseRetryBase is the numeric base the retry field is written in. The format
// says decimal, so this is not a choice.
const sseRetryBase int = 10

// sseCRLFWidth is how many bytes a CRLF terminator occupies. Naming it is the
// point: treating CRLF as two terminators emits a spurious empty data line
// between every pair of lines, which is a corruption nobody reads back.
const sseCRLFWidth int = 2

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
// It is separate from AppendTo so a caller can check a frame before committing
// to send it, and so AppendTo can validate everything BEFORE it appends
// anything — a half-written frame on a stream is unrecoverable, since the peer
// has already read it.
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

// AppendTo appends the frame's wire form to dst and returns the extended slice.
//
// It appends nothing when the frame is invalid: the whole frame is validated
// first, so a stream never emits a partial one. Appending rather than returning
// a fresh slice is what lets a stream reuse one buffer for its whole life.
func (e SSEEventValue) AppendTo(dst []byte) (wire []byte, err error) {
	//: validate before touching dst, so a refusal leaves the caller's buffer
	//: exactly as it was.
	if verr := e.Validate(); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	//: id first, so a client that stops reading mid-frame still has the resume
	//: token for the frame it is about to discard.
	if e.ID != "" {
		dst = appendSSEField(dst, "id", e.ID)
	}
	//: the event type, when the frame is not the default "message".
	if e.Name != "" {
		dst = appendSSEField(dst, "event", e.Name)
	}
	//: the reconnection hint, in whole milliseconds.
	if retry := e.Retry.Duration(); retry != 0 {
		dst = append(dst, "retry: "...)
		dst = strconv.AppendInt(dst, int64(retry/time.Millisecond), sseRetryBase)
		dst = append(dst, '\n')
	}
	//: one data line per line of payload; an empty payload emits none, which is
	//: how an id-only checkpoint frame is expressed.
	if e.Data != "" {
		dst = appendSSEData(dst, e.Data)
	}
	//: the blank line terminates the frame and triggers the client's dispatch.
	return append(dst, '\n'), nil
}

// AppendSSEComment appends a comment frame — a line beginning with ':' that
// every client ignores — to dst.
//
// It exists for keep-alive: an idle event stream is indistinguishable from a
// dead one to a proxy counting idle seconds, and a comment is the only traffic
// the format offers that cannot be mistaken for an event.
func AppendSSEComment(dst []byte, text string) (wire []byte, err error) {
	//: a comment is a single line, so a terminator inside it would end the
	//: comment early and turn the remainder into fields the client obeys.
	if verr := validateSSELine("comment", text); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	dst = appendSSEField(dst, "", text)
	//: the blank line keeps the comment from merging with the next frame's
	//: fields, so a comment can be written between any two events.
	return append(dst, '\n'), nil
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
	//: internal/service/proc/sdnotify.
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

// appendSSEData writes the payload as one "data:" line per line it contains.
//
// The scan is what this function costs. Both terminator cursors are found once
// over the WHOLE payload and afterwards only ever move FORWARD, so each of the
// two searches reads each stretch of the payload exactly once and the walk is
// linear whatever shape the payload has. The obvious alternative — an IndexByte
// pair per line — is quadratic on half of the possible payloads, and which half
// depends on which of the two scans is left unbounded: bound neither and a
// payload of LF-terminated lines re-reads its whole tail looking for a CR that
// is not there; bound the CR scan by the LF and the mirror payload, CR-only,
// does the same. Both are measured in BENCH.md and both are refused.
func appendSSEData(dst []byte, data string) []byte {
	lf := strings.IndexByte(data, '\n')
	cr := strings.IndexByte(data, '\r')
	at := 0
	//: walk the payload one line at a time until the last one is written.
	for {
		idx := sseFirstTerminator(lf, cr)
		//: no terminator left, so the remainder is the final line.
		if idx < 0 {
			//: every line is written.
			return appendSSEField(dst, "data", data[at:])
		}
		dst = appendSSEField(dst, "data", data[at:idx])
		at = idx + sseTerminatorWidth(data, idx)
		//: refresh only a cursor this cut consumed or overtook, resuming AT the
		//: new position rather than at the start — that is the whole linearity
		//: argument, so these are not an optimisation on top of a correct loop,
		//: they ARE the loop.
		lf = sseAdvance(data, lf, at, '\n')
		cr = sseAdvance(data, cr, at, '\r')
	}
}

// sseTerminatorWidth reports how many bytes the terminator at idx occupies.
func sseTerminatorWidth(data string, idx int) int {
	//: CRLF is one terminator, not two — treating it as two would emit a
	//: spurious empty data line between every pair of lines.
	if data[idx] == '\r' && idx+1 < len(data) && data[idx+1] == '\n' {
		//: the pair, counted once.
		return sseCRLFWidth
	}
	//: a lone LF or a lone CR.
	return 1
}

// sseAdvance returns where the next b sits at or after at, re-scanning only
// when the cut just made consumed or overtook the cursor it is given.
//
// The untouched case is not an optimisation: a cursor still ahead of the cut is
// already the answer, and re-scanning for it would read the same bytes again on
// every line, which is what makes the obvious per-line form quadratic.
func sseAdvance(data string, cursor, at int, b byte) int {
	//: absent stays absent, and a cursor beyond the cut is still correct.
	if cursor < 0 || cursor >= at {
		//: nothing to re-scan.
		return cursor
	}
	//: consumed or overtaken — look for the next one, from here forward only.
	return sseIndexFrom(data, at, b)
}

// sseFirstTerminator returns whichever of the two cursors comes first, with -1
// meaning that terminator is absent from the rest of the payload.
func sseFirstTerminator(lf, cr int) int {
	//: an absent LF leaves the CR to decide, absent or not.
	if lf < 0 {
		//: the carriage return, or -1 when there is none either.
		return cr
	}
	//: an absent CR, or one after the LF, leaves the LF.
	if cr < 0 || lf < cr {
		//: the line feed cuts.
		return lf
	}
	//: the carriage return is the earlier of the two.
	return cr
}

// sseIndexFrom returns the index of b at or after from in s, expressed in s's
// own coordinates, or -1 when the rest of s does not carry it.
func sseIndexFrom(s string, from int, b byte) int {
	idx := strings.IndexByte(s[from:], b)
	//: a miss over the remainder stays a miss over the whole string.
	if idx < 0 {
		//: absent from here on.
		return -1
	}
	//: re-base onto the whole string, which is what the cursors are kept in.
	return from + idx
}

// appendSSEField writes one "name: value" line, or ": value" when name is empty
// (a comment).
func appendSSEField(dst []byte, name, value string) []byte {
	dst = append(dst, name...)
	dst = append(dst, ':')
	//: the client strips exactly one leading space from a value, so the
	//: conventional space costs nothing — but writing it on an EMPTY value
	//: would leave trailing whitespace on the wire for no gain.
	if value != "" {
		dst = append(dst, ' ')
		dst = append(dst, value...)
	}
	//: one field, one line.
	return append(dst, '\n')
}
