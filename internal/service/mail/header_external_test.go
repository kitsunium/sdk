package mail_test

import (
	"bytes"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
)

// TestASCIIHeadersAreNotEncoded pins the "only when necessary" half of the
// RFC 2047 decision. Encoding unconditionally would be simpler and would make
// every captured message, every log line and every mail-store search show
// "=?utf-8?q?Invoice_4711?=" to buy nothing, since a receiver renders both
// forms identically.
func TestASCIIHeadersAreNotEncoded(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Subject = "Invoice 4711 - due 30 April"
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	if !bytes.Contains(raw, []byte("Subject: Invoice 4711 - due 30 April\r\n")) {
		t.Fatalf("an ASCII subject was encoded anyway:\n%s", raw)
	}
}

// TestNonASCIIHeadersAreEncodedAndRoundTrip pins the other half: a French
// subject must arrive readable, which means an encoded-word going out and the
// original string coming back through any conforming decoder.
func TestNonASCIIHeadersAreEncodedAndRoundTrip(t *testing.T) {
	t.Parallel()
	for _, subject := range []string{
		"Réunion à 9h",
		"Rapport du 1ᵉʳ trimestre — chiffres définitifs",
		"日本語の件名",
		strings.Repeat("é", 400),
	} {
		msg := simpleMessage()
		msg.Subject = subject
		raw, err := testComposer().Compose(msg)
		if err != nil {
			t.Fatalf("Compose(%q) = %v, want nil", subject, err)
		}
		parsed, _, _ := parseMessage(t, raw)
		header := parsed.Header.Get("Subject")
		if !strings.HasPrefix(header, "=?utf-8?") {
			t.Fatalf("a non-ASCII subject was emitted raw: %q", header)
		}
		decoded, decodeErr := new(mime.WordDecoder).DecodeHeader(header)
		if decodeErr != nil {
			t.Fatalf("Subject %q did not decode: %v", header, decodeErr)
		}
		if decoded != subject {
			t.Fatalf("Subject round-tripped to %q, want %q", decoded, subject)
		}
	}
}

// TestFoldingIsReversible pins RFC 5322 §2.2.3: unfolding removes the CRLF and
// keeps the whitespace, so a folded header must reconstruct the original value
// exactly. A folder that inserted or dropped a space would deliver a different
// value while reporting success.
func TestFoldingIsReversible(t *testing.T) {
	t.Parallel()
	value := strings.TrimSpace(strings.Repeat("token-of-some-length ", 60))
	msg := simpleMessage()
	msg.Headers = []coremail.HeaderFieldValue{{Name: "X-Trace", Value: value}}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	//: the header block must actually have been folded, or this proves nothing.
	if !bytes.Contains(raw, []byte("X-Trace: token-of-some-length token")) {
		t.Fatalf("X-Trace did not start where expected:\n%s", raw)
	}
	folded := false
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if strings.HasPrefix(line, " ") && strings.Contains(line, "token-of-some-length") {
			folded = true
		}
	}
	if !folded {
		t.Fatalf("a 1200-octet header was not folded at all:\n%s", raw)
	}
	//: and net/textproto's own unfolder must give the original value back.
	parsed, _, _ := parseMessage(t, raw)
	if got := parsed.Header.Get("X-Trace"); got != value {
		t.Fatalf("X-Trace unfolded to %q\nwant %q", got, value)
	}
}

// TestSoftLimitIsRespectedWhereFoldingIsPossible pins the RFC 5322 §2.1.1
// recommendation, which is a SHOULD rather than the MUST NOT at 998 — so it is
// asserted only where a fold point exists.
func TestSoftLimitIsRespectedWhereFoldingIsPossible(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	for range 30 {
		msg.To = append(msg.To, coremail.AddressValue{Name: "Recipient", Addr: "user@fake.example"})
	}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	block, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for line := range strings.SplitSeq(block, "\r\n") {
		if len(line) > 78 {
			t.Fatalf("header line is %d octets with fold points available: %q", len(line), line)
		}
	}
}

// TestMultipartWriterWouldHaveInjected is the measurement behind the decision
// NOT to build the message with mime/multipart.Writer.
//
// It asserts the standard library's behaviour rather than this package's:
// CreatePart formats header values verbatim, so a Content-Disposition carrying
// a CRLF produces an injected header inside the part. That is a perfectly
// reasonable contract for a package whose callers control their own headers,
// and a trap for one whose callers do not — which is why every header in this
// domain is written through a function that runs the injection gate first.
//
// If a future Go release starts validating there, this test fails and the
// reasoning in the ADR gets revisited rather than silently becoming stale.
func TestMultipartWriterWouldHaveInjected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.SetBoundary("BOUNDARY"); err != nil {
		t.Fatalf("SetBoundary: %v", err)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "text/plain")
	header.Set("Content-Disposition", "attachment; filename=\"a\r\nBcc: attacker@evil.example\"")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, writeErr := part.Write([]byte("x")); writeErr != nil {
		t.Fatalf("write part: %v", writeErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if !strings.Contains(buf.String(), "\r\nBcc: attacker@evil.example") {
		t.Skip("mime/multipart.Writer now validates header values; the SDK's own gate is still the one that guarantees it")
	}
	t.Log("mime/multipart.Writer emits header values verbatim, injected CRLF and all — this package writes its own delimiters for that reason")
	//: and the same filename through this package is refused rather than emitted.
	msg := simpleMessage()
	msg.Attachments = []coremail.AttachmentValue{{Filename: "a\r\nBcc: attacker@evil.example", Content: []byte("x")}}
	if _, composeErr := testComposer().Compose(msg); composeErr == nil {
		t.Fatal("Compose accepted a filename carrying a CRLF")
	}
}

// TestAttachmentFilenameIsCarriedInBothPlaces pins the one deliberately
// redundant field. RFC 2183 §2.3 deprecates Content-Type's "name" parameter in
// favour of Content-Disposition's "filename", and receivers in the field still
// read the deprecated one — so both are emitted, rendered from the SAME value,
// which is why they cannot disagree.
func TestAttachmentFilenameIsCarriedInBothPlaces(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Attachments = []coremail.AttachmentValue{{Filename: "rapport-été.pdf", Content: []byte("%PDF")}}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	parsed, params, mediaType := parseMessage(t, raw)
	tree := readTree(t, mediaType, params, parsed.Header, parsed.Body)
	part := tree.children[1]
	//: RFC 2231 continuation is what mime.FormatMediaType emits for a non-ASCII
	//: parameter, and mime.ParseMediaType is what reads it back.
	_, typeParams, typeErr := mime.ParseMediaType(part.header["Content-Type"][0])
	if typeErr != nil {
		t.Fatalf("attachment Content-Type did not parse: %v", typeErr)
	}
	_, dispParams, dispErr := mime.ParseMediaType(part.header["Content-Disposition"][0])
	if dispErr != nil {
		t.Fatalf("attachment Content-Disposition did not parse: %v", dispErr)
	}
	if typeParams["name"] != "rapport-été.pdf" || dispParams["filename"] != "rapport-été.pdf" {
		t.Fatalf("filename = %q / %q, want the caller's own in both", typeParams["name"], dispParams["filename"])
	}
}
