// Package multipart — boundary recovery and validation.
//
// The delimiter that separates the parts of a multipart body is announced in
// the message's Content-Type header, NOT in the body. codec.Codec carries no
// header, so this file is where the gap is closed: the boundary is re-emitted
// on every delimiter line, which makes a well-formed body self-describing, and
// these helpers read it back out. See CLAUDE.md §The boundary problem for what
// that buys and what it does not.
package multipart

import (
	"bytes"
	"mime"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// boundarySniffWindow caps how many leading bytes are scanned for the first
// delimiter line. RFC 2046 allows an arbitrary preamble before it; a bounded
// window keeps the scan O(1) against a hostile body that never emits one.
const boundarySniffWindow int = 4096

// maxBoundaryLen is the RFC 2046 ceiling on a boundary (70 characters).
const maxBoundaryLen int = 70

// boundaryChars is the RFC 2046 bchars set, matching what
// mime/multipart.Writer.SetBoundary accepts. SPACE is legal except as the last
// character, which validateBoundary checks separately.
const boundaryChars string = "" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"abcdefghijklmnopqrstuvwxyz" +
	"0123456789'()+_,-./:=? "

// delimiterPrefix is the two hyphens every delimiter line starts with.
const delimiterPrefix string = "--"

// Boundary recovers the RFC 2046 delimiter from a multipart body by reading it
// back off the body's first delimiter line.
//
// This is EXACT for every body this package produced and for every
// RFC 7578 form-data body in the wild, because form-data producers emit no
// preamble. It is a recovery, not an oracle: a body whose preamble contains a
// line that both starts with "--" and parses as a valid boundary would hand
// back the wrong answer. When the real Content-Type header is available, pass
// it to NewDecoderWithBoundary instead of relying on this.
func Boundary(data []byte) (boundary string, err error) {
	//: candidate is the first delimiter-looking line inside the sniff window.
	candidate, found := firstDelimiterLine(data)
	//: no delimiter at all — the input is not a multipart body we can read.
	if !found {
		//: typed refusal; nothing about the payload is echoed.
		return "", boundaryInvalid("no delimiter line found within the sniff window")
	}
	//: a well-formed body carries the closing delimiter "--<boundary>--";
	//: finding it confirms the candidate rather than assuming it.
	if validateBoundary(candidate) == nil && closesWith(data, candidate) {
		//: confirmed by the closing delimiter.
		return candidate, nil
	}
	//: a body with ZERO parts is nothing but its closing delimiter, so the
	//: first line already carries the trailing "--" — strip it and re-confirm.
	if trimmed, ok := strings.CutSuffix(candidate, delimiterPrefix); ok &&
		validateBoundary(trimmed) == nil && closesWith(data, trimmed) {
		//: confirmed after removing the closing marker.
		return trimmed, nil
	}
	//: no closing delimiter anywhere — the body is truncated. Hand the
	//: candidate over anyway when it is structurally valid so mime/multipart
	//: reports the truncation, which is the more precise diagnosis.
	if verr := validateBoundary(candidate); verr == nil {
		//: best-effort; the reader will fail with the real reason.
		return candidate, nil
	}
	//: the line looked like a delimiter but is not a usable boundary.
	return "", boundaryInvalid("first delimiter line is not a valid RFC 2046 boundary")
}

// ContentType returns the full Content-Type header value for a body this
// package produced — "multipart/form-data; boundary=…", quoted by
// mime.FormatMediaType when the boundary needs it.
//
// Marshal cannot set a header, so this is how the caller obtains the one piece
// of the message the Codec contract has nowhere to put.
func ContentType(data []byte) (value string, err error) {
	//: recover the delimiter the encoder wrote into the body.
	boundary, berr := Boundary(data)
	//: propagate the typed refusal verbatim.
	if berr != nil {
		//: caller sees BOUNDARY_INVALID with its own diagnostic.
		return "", berr
	}
	//: stdlib does the parameter quoting so the header is always well-formed.
	return mime.FormatMediaType(mimeFormData, map[string]string{"boundary": boundary}), nil
}

// firstDelimiterLine returns the text following the "--" prefix of the first
// delimiter-looking line inside the sniff window, with the trailing CR removed.
func firstDelimiterLine(data []byte) (candidate string, found bool) {
	//: the scan itself stays on bytes; exactly one conversion happens here,
	//: on the single line that matched.
	raw, ok := scanDelimiterLine(data)
	//: window exhausted without a delimiter line.
	if !ok {
		//: caller reports the structural refusal.
		return "", false
	}
	//: materialise the candidate for the validators.
	return string(raw), true
}

// scanDelimiterLine walks the bounded sniff window and returns the bytes after
// the "--" prefix of the first delimiter-looking line.
func scanDelimiterLine(data []byte) (rest []byte, found bool) {
	//: bound the scan so a hostile preamble cannot make this O(len(data)).
	window := data
	//: clip to the sniff window when the body is larger.
	if len(window) > boundarySniffWindow {
		//: only the head can legitimately carry the first delimiter.
		window = window[:boundarySniffWindow]
	}
	//: hoisted so the loop body allocates nothing.
	prefix := []byte(delimiterPrefix)
	//: walk line by line; the first "--"-prefixed line is the candidate.
	for cursor := 0; cursor < len(window); {
		//: locate the line terminator; -1 means the window's trailing line.
		line, next := nextLine(window, cursor)
		//: advance regardless of whether this line matched.
		cursor = next
		//: only "--"-prefixed lines can be delimiters.
		if !bytes.HasPrefix(line, prefix) {
			//: preamble line — keep scanning.
			continue
		}
		//: first match wins.
		return line[len(prefix):], true
	}
	//: window exhausted without a delimiter line.
	return nil, false
}

// nextLine returns the bytes of the line starting at cursor (CR stripped) and
// the offset of the following line.
func nextLine(data []byte, cursor int) (line []byte, next int) {
	//: locate the LF that ends this line.
	offset := bytes.IndexByte(data[cursor:], '\n')
	//: no LF — the remainder of the window is the final line.
	if offset < 0 {
		//: consume everything left.
		return bytes.TrimSuffix(data[cursor:], []byte{'\r'}), len(data)
	}
	//: line content excludes the LF; CRLF loses its CR too.
	return bytes.TrimSuffix(data[cursor:cursor+offset], []byte{'\r'}), cursor + offset + 1
}

// closesWith reports whether data carries the closing delimiter for boundary.
func closesWith(data []byte, boundary string) bool {
	//: "--<boundary>--" is the RFC 2046 close-delimiter form.
	return bytes.Contains(data, []byte(delimiterPrefix+boundary+delimiterPrefix))
}

// validateBoundary enforces the RFC 2046 shape mime/multipart.Writer accepts:
// 1-70 characters drawn from bchars, with SPACE barred from the last position.
// Checking here rather than deferring to SetBoundary keeps the failure a typed
// BOUNDARY_INVALID at the SDK's own edge instead of a bare stdlib error.
func validateBoundary(boundary string) error {
	//: an empty or over-long boundary is out of spec.
	if boundary == "" || len(boundary) > maxBoundaryLen {
		//: typed refusal naming the structural rule.
		return boundaryInvalid("boundary length must be 1..70 characters")
	}
	//: every character must be in the bchars set.
	if strings.ContainsFunc(boundary, func(r rune) bool {
		//: report the first character outside the allowed set.
		return !strings.ContainsRune(boundaryChars, r)
	}) {
		//: typed refusal naming the charset rule.
		return boundaryInvalid("boundary carries a character outside the RFC 2046 set")
	}
	//: SPACE is legal in a boundary but never as its final character.
	if strings.HasSuffix(boundary, " ") {
		//: typed refusal naming the trailing-space rule.
		return boundaryInvalid("boundary must not end with a space")
	}
	//: structurally usable.
	return nil
}

// boundaryInvalid builds the typed BoundaryInvalid error with a call-site
// specific Private diagnostic. The boundary itself is never echoed — it is
// attacker-controlled on the decode path.
func boundaryInvalid(detail string) error {
	//: typed sentinel so callers route on CodeMultipartBoundaryInvalid.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeMultipartBoundaryInvalid,
		Reason:  "BOUNDARY_INVALID",
		Public:  "multipart boundary is missing or invalid",
		Private: "service/codec/multipart: " + detail,
	})
}
