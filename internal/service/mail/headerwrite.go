// Package mail — RFC 2047 encoding and RFC 5322 folding for header lines.
package mail

import (
	"io"
	"mime"
	"strings"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RFC 5322 §2.1.1's two line limits. They are not the same kind of rule: 998 is
// a MUST NOT (excluding the CRLF) and 78 is a SHOULD. So 78 is where folding
// starts and 998 is where refusal starts.
const (
	softLineOctets int = 78
	hardLineOctets int = 998
)

// crlf is the only line terminator this domain writes. RFC 5322 §2.1 makes it
// the terminator; a lone LF is what a Unix-shaped writer produces by accident
// and what an MTA silently repairs — differently from the next MTA.
const crlf string = "\r\n"

// minFoldTail is the smallest continuation a fold may leave behind: the space
// it folds at, plus at least one octet after it. A fold that left only the
// space would emit a line of whitespace, which unfolds to nothing.
const minFoldTail int = 2

// encodeUnstructured applies RFC 2047 "encoded-word" form to a header value,
// and applies it ONLY when the value needs it.
//
// mime.QEncoding.Encode is the whole implementation, and its behaviour is the
// decision: a pure-ASCII value is returned untouched, so "Invoice 4711" stays
// "Invoice 4711" in the raw message rather than becoming
// "=?utf-8?q?Invoice_4711?=". Encoding unconditionally would be simpler and
// would be worse — every log line, every mail-store search, every operator
// reading a captured message would face an encoded blob to buy nothing, since
// a receiver renders both identically.
//
// One thing this function does NOT do is protect against injection. Handed a
// CRLF, mime.QEncoding.Encode returns "=0D=0A" — a deliverable header carrying
// a value the caller never wrote, reported as success. Every value reaching
// here has already passed coremail.ValidateHeaderValue, and writeHeader checks
// again at the point of writing.
func encodeUnstructured(value string) string {
	//: Q over B: for a mostly-ASCII value Q leaves the ASCII readable, and for
	//: a fully non-ASCII one the size difference is a few percent.
	return mime.QEncoding.Encode("utf-8", value)
}

// formatAddress renders one mailbox as RFC 5322 §3.4 name-addr, or as a bare
// angle-addr when there is no display name.
//
// The display name is the interesting half and has three cases, not two:
//
//   - non-ASCII → RFC 2047 encoded-word. RFC 2047 §5 forbids an encoded-word
//     inside a quoted-string, so this case must NOT also be quoted.
//   - ASCII carrying a special (RFC 5322 §3.2.3) → quoted-string. This is the
//     case everyone forgets: an unquoted "Doe, John <j@x>" parses as TWO
//     addresses, the first of which is not an address at all, and the
//     recipient's client shows something different from the sender's.
//   - anything else → emitted bare.
func formatAddress(a coremail.AddressValue) string {
	//: no display name means nothing to encode or quote.
	if a.Name == "" {
		//: angle-addr alone.
		return "<" + a.Addr + ">"
	}
	//: RFC 2047 first: an encoded-word is already safe against every special,
	//: and quoting one would violate §5.
	if encoded := encodeUnstructured(a.Name); encoded != a.Name {
		//: encoded-word form.
		return encoded + " <" + a.Addr + ">"
	}
	//: ASCII with a special must become a quoted-string or it becomes syntax.
	if coremail.NeedsQuotedDisplayName(a.Name) {
		//: quoted-string form, with backslash and quote escaped (§3.2.4).
		return `"` + quoteEscaper.Replace(a.Name) + `" <` + a.Addr + ">"
	}
	//: plain atoms.
	return a.Name + " <" + a.Addr + ">"
}

// addressField pairs a header name with the mailbox list emitted under it, so
// the composer's header block is built from a table rather than three copies of
// the same four lines.
type addressField struct {
	name  string
	addrs []coremail.AddressValue
}

// quoteEscaper escapes the two characters RFC 5322 §3.2.4 requires escaping
// inside a quoted-string. CR, LF and NUL are not in the list because they are
// refused upstream rather than escaped.
var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// formatAddressList renders a mailbox-list (RFC 5322 §3.4). The separator is
// ", " so that folding has a space to fold at — a comma alone would make a
// hundred-recipient To field one unfoldable token.
func formatAddressList(list []coremail.AddressValue) string {
	//: exactly one join, sized by the caller's slice.
	rendered := make([]string, 0, len(list))
	//: each mailbox in the order the caller wrote it.
	for _, addr := range list {
		//: name-addr or angle-addr.
		rendered = append(rendered, formatAddress(addr))
	}
	//: the space after the comma is a fold point, not decoration.
	return strings.Join(rendered, ", ")
}

// writeHeader emits one already-encoded header field, folded to the RFC 5322
// §2.1.1 limits and terminated by CRLF.
//
// It re-runs the injection gate on both halves. That is not belt-and-braces
// theatre: this is the last function before bytes reach a buffer, some of the
// values arriving here were built by this package rather than supplied by the
// caller (a Content-Disposition assembled from a filename, an address list
// joined from mailboxes), and mime/multipart's own CreatePart writes header
// values VERBATIM — handed "a\r\nBcc: x@y" it emits exactly that, injected
// header and all. A gate at the point of writing is the only one that covers
// every path into it.
func writeHeader(dst io.Writer, name, value string) error {
	//: the name grammar, checked here too because this is the write point.
	if nameErr := coremail.ValidateHeaderName(name); nameErr != nil {
		//: HeaderInjection.
		return nameErr
	}
	//: and the value, for the same reason.
	if valueErr := coremail.ValidateHeaderValue(name, value); valueErr != nil {
		//: HeaderInjection.
		return valueErr
	}
	//: fold to the recommended width, refusing what cannot be folded.
	folded, foldErr := foldHeaderLine(name, value)
	//: HeaderTooLong.
	if foldErr != nil {
		//: refused rather than truncated.
		return foldErr
	}
	//: one write, CRLF-terminated.
	_, writeErr := io.WriteString(dst, folded+crlf)
	//: the buffer's own failure, if any.
	return writeErr
}

// foldHeaderLine wraps "name: value" onto continuation lines per RFC 5322
// §2.2.3, and returns [coremail.HeaderTooLong] when it cannot.
//
// Folding is legal ONLY at existing whitespace. §2.2.3 defines unfolding as
// "removing any CRLF that is immediately followed by WSP" — the whitespace
// stays, the CRLF goes — so a fold placed at a space reconstructs the original
// value exactly, and a fold placed anywhere else would insert a character the
// caller never wrote.
//
// That is why an over-long token is refused instead of being cut. A 1200-octet
// URL in a header has no legal fold point; breaking it anyway would deliver a
// different URL and report success, which is the failure mode this whole
// domain is organised against.
func foldHeaderLine(name, value string) (folded string, err error) {
	line := name + ": " + value
	//: the overwhelmingly common case, and it allocates nothing.
	if len(line) <= softLineOctets {
		//: already within the recommendation.
		return line, nil
	}
	lines := foldAtWhitespace(line)
	//: every emitted line must respect the MUST NOT, not just the first.
	for _, candidate := range lines {
		//: 998 excludes the CRLF (§2.1.1).
		if len(candidate) > hardLineOctets {
			//: the header NAME is safe to report; its value is not.
			return "", errs.Wrap(coremail.HeaderTooLong, errs.WrapParams{},
				errs.String("header", name), errs.Int("octets", len(candidate)))
		}
	}
	//: the continuation lines already begin with the whitespace they folded at.
	return strings.Join(lines, crlf), nil
}

// foldAtWhitespace splits one long line into a first line and continuation
// lines, each continuation beginning with the space it was folded at.
func foldAtWhitespace(line string) []string {
	var lines []string
	rest := line
	//: keep folding while anything remains over the recommended width.
	for len(rest) > softLineOctets {
		cut := foldPoint(rest)
		//: no legal fold point left — emit the remainder and let the caller's
		//: hard-limit check decide whether it is fatal.
		if cut < 0 {
			//: nothing more can be done legally.
			break
		}
		//: the space at cut stays with the CONTINUATION, so unfolding restores
		//: the original spacing byte for byte.
		lines = append(lines, rest[:cut])
		rest = rest[cut:]
	}
	//: whatever is left is the last line.
	return append(lines, rest)
}

// foldPoint returns the index of the space to fold at, or -1 when the line
// offers none that leaves a non-empty continuation.
//
// It prefers the LAST space at or before the soft limit, which keeps lines as
// full as possible; failing that it takes the first space after it, which
// produces one over-long line rather than giving up on the rest.
func foldPoint(line string) int {
	limit := min(softLineOctets, len(line)-minFoldTail)
	//: backwards from the soft limit: the fullest legal line.
	for index := limit; index >= 1; index-- {
		//: a space with something after it is a fold point.
		if line[index] == ' ' && line[index+1] != ' ' {
			//: fold here.
			return index
		}
	}
	//: forwards past the limit: an over-long first line beats an unfolded rest.
	for index := softLineOctets + 1; index < len(line)-1; index++ {
		//: same rule, later position.
		if line[index] == ' ' && line[index+1] != ' ' {
			//: fold here.
			return index
		}
	}
	//: one unbreakable token.
	return -1
}
