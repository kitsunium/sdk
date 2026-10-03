// Package sse — an event's wire form (WHATWG HTML §9.2, "Server-sent
// events"): the encoder that turns a corenet.SSEEventValue into the bytes a
// stream writes, and a comment into the one frame no client dispatches.
//
// The value and the rules it must satisfy are the core's
// (corenet.SSEEventValue.Validate); writing it is a mechanism, and it moved
// here from internal/core/net (ADR 0160 §4).
package sse

import (
	"strconv"
	"strings"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// retryBase is the numeric base the retry field is written in. The format
// says decimal, so this is not a choice.
const retryBase int = 10

// crlfWidth is how many bytes a CRLF terminator occupies. Naming it is the
// point: treating CRLF as two terminators emits a spurious empty data line
// between every pair of lines, which is a corruption nobody reads back.
const crlfWidth int = 2

// AppendEvent appends e's wire form to dst and returns the extended slice.
//
// It appends nothing when the frame is invalid: the whole frame is validated
// first (corenet.SSEEventValue.Validate), so a stream never emits a partial
// one. Appending rather than returning a fresh slice is what lets a stream
// reuse one buffer for its whole life.
func AppendEvent(dst []byte, e corenet.SSEEventValue) (wire []byte, err error) {
	//: validate before touching dst, so a refusal leaves the caller's buffer
	//: exactly as it was.
	if verr := e.Validate(); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	//: id first, so a client that stops reading mid-frame still has the resume
	//: token for the frame it is about to discard.
	if e.ID != "" {
		dst = appendField(dst, "id", e.ID)
	}
	//: the event type, when the frame is not the default "message".
	if e.Name != "" {
		dst = appendField(dst, "event", e.Name)
	}
	//: the reconnection hint, in whole milliseconds.
	if retry := e.Retry.Duration(); retry != 0 {
		dst = append(dst, "retry: "...)
		dst = strconv.AppendInt(dst, int64(retry/time.Millisecond), retryBase)
		dst = append(dst, '\n')
	}
	//: one data line per line of payload; an empty payload emits none, which is
	//: how an id-only checkpoint frame is expressed.
	if e.Data != "" {
		dst = appendData(dst, e.Data)
	}
	//: the blank line terminates the frame and triggers the client's dispatch.
	return append(dst, '\n'), nil
}

// AppendComment appends a comment frame — a line beginning with ':' that
// every client ignores — to dst.
//
// It exists for keep-alive: an idle event stream is indistinguishable from a
// dead one to a proxy counting idle seconds, and a comment is the only traffic
// the format offers that cannot be mistaken for an event.
func AppendComment(dst []byte, text string) (wire []byte, err error) {
	//: a comment is a single line, so a terminator inside it would end the
	//: comment early and turn the remainder into fields the client obeys.
	if verr := validateComment(text); verr != nil {
		//: hand back the untouched buffer with the reason.
		return dst, verr
	}
	dst = appendField(dst, "", text)
	//: the blank line keeps the comment from merging with the next frame's
	//: fields, so a comment can be written between any two events.
	return append(dst, '\n'), nil
}

// validateComment refuses a line terminator in a comment: a comment is a single
// line, so a terminator inside one would end it early and turn the remainder
// into fields the client obeys. It is the rule corenet.SSEEventValue.Validate
// applies to an id and an event name, applied to the one line the value does
// not carry, and it is spelled the same way: two assembly-backed byte searches
// behind an emptiness test, measured in internal/core/net/BENCH.md.
func validateComment(text string) error {
	//: LF and CR both terminate a line in this format, so both are fatal here.
	if text != "" && (strings.IndexByte(text, '\n') >= 0 || strings.IndexByte(text, '\r') >= 0) {
		//: refuse rather than truncate.
		return errs.Wrap(corenet.SSEFieldInvalid, errs.WrapParams{},
			errs.String("field", "comment"),
			errs.String("why", "the value carries a line terminator, which this field cannot express"))
	}
	//: representable.
	return nil
}

// appendData writes the payload as one "data:" line per line it contains.
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
func appendData(dst []byte, data string) []byte {
	lf := strings.IndexByte(data, '\n')
	cr := strings.IndexByte(data, '\r')
	at := 0
	//: walk the payload one line at a time until the last one is written.
	for {
		idx := firstTerminator(lf, cr)
		//: no terminator left, so the remainder is the final line.
		if idx < 0 {
			//: every line is written.
			return appendField(dst, "data", data[at:])
		}
		dst = appendField(dst, "data", data[at:idx])
		at = idx + terminatorWidth(data, idx)
		//: refresh only a cursor this cut consumed or overtook, resuming AT the
		//: new position rather than at the start — that is the whole linearity
		//: argument, so these are not an optimisation on top of a correct loop,
		//: they ARE the loop.
		lf = advance(data, lf, at, '\n')
		cr = advance(data, cr, at, '\r')
	}
}

// terminatorWidth reports how many bytes the terminator at idx occupies.
func terminatorWidth(data string, idx int) int {
	//: CRLF is one terminator, not two — treating it as two would emit a
	//: spurious empty data line between every pair of lines.
	if data[idx] == '\r' && idx+1 < len(data) && data[idx+1] == '\n' {
		//: the pair, counted once.
		return crlfWidth
	}
	//: a lone LF or a lone CR.
	return 1
}

// advance returns where the next b sits at or after at, re-scanning only
// when the cut just made consumed or overtook the cursor it is given.
//
// The untouched case is not an optimisation: a cursor still ahead of the cut is
// already the answer, and re-scanning for it would read the same bytes again on
// every line, which is what makes the obvious per-line form quadratic.
func advance(data string, cursor, at int, b byte) int {
	//: absent stays absent, and a cursor beyond the cut is still correct.
	if cursor < 0 || cursor >= at {
		//: nothing to re-scan.
		return cursor
	}
	//: consumed or overtaken — look for the next one, from here forward only.
	return indexFrom(data, at, b)
}

// firstTerminator returns whichever of the two cursors comes first, with -1
// meaning that terminator is absent from the rest of the payload.
func firstTerminator(lf, cr int) int {
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

// indexFrom returns the index of b at or after from in s, expressed in s's
// own coordinates, or -1 when the rest of s does not carry it.
func indexFrom(s string, from int, b byte) int {
	idx := strings.IndexByte(s[from:], b)
	//: a miss over the remainder stays a miss over the whole string.
	if idx < 0 {
		//: absent from here on.
		return -1
	}
	//: re-base onto the whole string, which is what the cursors are kept in.
	return from + idx
}

// appendField writes one "name: value" line, or ": value" when name is empty
// (a comment).
func appendField(dst []byte, name, value string) []byte {
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
