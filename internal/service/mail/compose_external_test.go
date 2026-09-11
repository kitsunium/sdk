package mail_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// fixedInstant pins the Date header so a composed message is byte-for-byte
// reproducible.
var fixedInstant = time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)

// testComposer builds a composer whose clock does not move and whose boundary
// randomness is a repeating byte, so every assertion below is deterministic.
func testComposer() *svcmail.Composer {
	return svcmail.NewComposer(svcmail.ComposerConfig{
		Clock: clock.NewManualClock(fixedInstant),
		Rand:  repeatingReader{},
	})
}

// repeatingReader is a deterministic stand-in for crypto/rand.
type repeatingReader struct{}

// Read fills p with a constant, which is exactly what a boundary must not
// depend on for correctness.
func (repeatingReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 0xA7
	}
	return len(p), nil
}

// simpleMessage is the message every transport test sends.
func simpleMessage() coremail.MessageValue {
	return coremail.MessageValue{
		From:    coremail.AddressValue{Name: "Ops", Addr: "ops@fake.example"},
		To:      []coremail.AddressValue{{Addr: "user@fake.example"}},
		Subject: "Quarterly report",
		Text:    "Please find the numbers attached.",
	}
}

// parseMessage reparses composed bytes with net/mail, which is the standard
// library's own RFC 5322 reader. A structural assertion made by REPARSING is
// worth something; one made by comparing a golden string only says the output
// did not change.
func parseMessage(t *testing.T, raw []byte) (*mail.Message, map[string]string, string) {
	t.Helper()
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("net/mail could not reparse the composed message: %v\n---\n%s", err, raw)
	}
	mediaType, params, typeErr := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if typeErr != nil {
		t.Fatalf("unparseable Content-Type %q: %v", parsed.Header.Get("Content-Type"), typeErr)
	}
	return parsed, params, mediaType
}

// structureCase is one combination of populated fields, the root media type it
// must produce, and the layout assertion for the tree beneath it.
type structureCase struct {
	name   string
	mutate func(m *coremail.MessageValue)
	want   string
	layout func(t *testing.T, tree partTree)
}

// partTree is a reparsed MIME tree, built with mime/multipart's Reader.
type partTree struct {
	mediaType string
	params    map[string]string
	header    map[string][]string
	body      string
	children  []partTree
}

// readTree walks the multipart structure the composer produced.
func readTree(t *testing.T, mediaType string, params map[string]string, header map[string][]string, body io.Reader) partTree {
	t.Helper()
	node := partTree{mediaType: mediaType, params: params, header: header}
	if !strings.HasPrefix(mediaType, "multipart/") {
		raw, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read part body: %v", err)
		}
		node.body = decodeBody(t, header, raw)
		return node
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("%s has no boundary parameter", mediaType)
	}
	reader := multipart.NewReader(body, boundary)
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("multipart.Reader refused the composed %s: %v", mediaType, err)
		}
		childType, childParams, typeErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if typeErr != nil {
			t.Fatalf("unparseable part Content-Type %q: %v", part.Header.Get("Content-Type"), typeErr)
		}
		node.children = append(node.children, readTree(t, childType, childParams, part.Header, part))
	}
	return node
}

// decodeBody reverses the Content-Transfer-Encoding so a test asserts on the
// caller's own bytes rather than on their encoding.
func decodeBody(t *testing.T, header map[string][]string, raw []byte) string {
	t.Helper()
	encoding := ""
	if values := header["Content-Transfer-Encoding"]; len(values) > 0 {
		encoding = values[0]
	}
	switch encoding {
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			t.Fatalf("quoted-printable body did not decode: %v", err)
		}
		return string(decoded)
	case "base64":
		//: the wrapping is part of what is being asserted, so it is checked and
		//: then removed rather than ignored.
		for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\r\n"), "\r\n") {
			if len(line) > 76 {
				t.Fatalf("a base64 line is %d octets, want at most 76 (RFC 2045 §6.8)", len(line))
			}
		}
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
		if err != nil {
			t.Fatalf("base64 body did not decode: %v", err)
		}
		return string(decoded)
	default:
		return string(raw)
	}
}

// TestComposedStructureMatchesTheFieldsThatArePopulated is the structure table
// from Compose's doc comment, asserted by REPARSING every message with
// mime/multipart rather than by comparing strings.
//
// Getting this table wrong is not a cosmetic defect: a multipart/mixed whose
// first part is an attachment shows as a message with no body in some clients,
// and an alternative in the wrong order shows the plain text to everybody.
func TestComposedStructureMatchesTheFieldsThatArePopulated(t *testing.T) {
	t.Parallel()
	attachment := coremail.AttachmentValue{Filename: "report.pdf", Content: []byte("%PDF-1.7 fake")}
	inline := coremail.AttachmentValue{Filename: "logo.png", ContentID: "logo@fake.example", Content: []byte("\x89PNG fake")}

	cases := []structureCase{
		{
			"text only",
			func(m *coremail.MessageValue) {},
			"text/plain",
			func(t *testing.T, tree partTree) {
				if tree.body != "Please find the numbers attached." {
					t.Fatalf("body = %q", tree.body)
				}
			},
		},
		{
			"html only",
			func(m *coremail.MessageValue) { m.Text = ""; m.HTML = "<p>hi</p>" },
			"text/html",
			nil,
		},
		{
			"text and html",
			func(m *coremail.MessageValue) { m.HTML = "<p>hi</p>" },
			"multipart/alternative",
			func(t *testing.T, tree partTree) {
				if len(tree.children) != 2 {
					t.Fatalf("alternative has %d parts, want 2", len(tree.children))
				}
				//: RFC 2046 §5.1.4: increasing preference, so plain FIRST.
				if tree.children[0].mediaType != "text/plain" || tree.children[1].mediaType != "text/html" {
					t.Fatalf("alternative order = %s, %s — want text/plain then text/html (RFC 2046 §5.1.4)",
						tree.children[0].mediaType, tree.children[1].mediaType)
				}
			},
		},
		{
			"body and attachment",
			func(m *coremail.MessageValue) { m.Attachments = []coremail.AttachmentValue{attachment} },
			"multipart/mixed",
			func(t *testing.T, tree partTree) {
				if len(tree.children) != 2 {
					t.Fatalf("mixed has %d parts, want 2", len(tree.children))
				}
				if tree.children[0].mediaType != "text/plain" {
					t.Fatalf("the first part of a mixed is %s, want the body", tree.children[0].mediaType)
				}
				if tree.children[1].body != "%PDF-1.7 fake" {
					t.Fatalf("attachment body = %q", tree.children[1].body)
				}
				disposition := tree.children[1].header["Content-Disposition"][0]
				if !strings.HasPrefix(disposition, "attachment;") {
					t.Fatalf("Content-Disposition = %q, want attachment", disposition)
				}
			},
		},
		{
			"body and inline",
			func(m *coremail.MessageValue) {
				m.HTML = `<img src="cid:logo@fake.example">`
				m.Attachments = []coremail.AttachmentValue{inline}
			},
			"multipart/related",
			func(t *testing.T, tree partTree) {
				//: RFC 2387 §3.1 makes "type" REQUIRED and it names the root.
				if tree.params["type"] != "multipart/alternative" {
					t.Fatalf("related type parameter = %q, want the root part's media type (RFC 2387 §3.1)", tree.params["type"])
				}
				if len(tree.children) != 2 {
					t.Fatalf("related has %d parts, want 2", len(tree.children))
				}
				//: RFC 2387 §3.2: the root is the first part when "start" is absent.
				if tree.children[0].mediaType != "multipart/alternative" {
					t.Fatalf("the first part of a related is %s, want the body root", tree.children[0].mediaType)
				}
				if id := tree.children[1].header["Content-Id"]; len(id) == 0 || id[0] != "<logo@fake.example>" {
					t.Fatalf("Content-ID = %v, want <logo@fake.example> so the cid: URL resolves", id)
				}
			},
		},
		{
			"body, inline and attachment",
			func(m *coremail.MessageValue) {
				m.HTML = `<img src="cid:logo@fake.example">`
				m.Attachments = []coremail.AttachmentValue{inline, attachment}
			},
			"multipart/mixed",
			func(t *testing.T, tree partTree) {
				if len(tree.children) != 2 {
					t.Fatalf("mixed has %d parts, want 2", len(tree.children))
				}
				related := tree.children[0]
				if related.mediaType != "multipart/related" {
					t.Fatalf("mixed[0] = %s, want multipart/related", related.mediaType)
				}
				if related.children[0].mediaType != "multipart/alternative" {
					t.Fatalf("related[0] = %s, want multipart/alternative", related.children[0].mediaType)
				}
				if tree.children[1].mediaType != "application/pdf" {
					t.Fatalf("mixed[1] = %s, want the attachment", tree.children[1].mediaType)
				}
			},
		},
		{
			"attachment only",
			func(m *coremail.MessageValue) { m.Text = ""; m.Attachments = []coremail.AttachmentValue{attachment} },
			"multipart/mixed",
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := simpleMessage()
			tc.mutate(&msg)
			raw, err := testComposer().Compose(msg)
			if err != nil {
				t.Fatalf("Compose = %v, want nil", err)
			}
			parsed, params, mediaType := parseMessage(t, raw)
			if mediaType != tc.want {
				t.Fatalf("root Content-Type = %s, want %s", mediaType, tc.want)
			}
			tree := readTree(t, mediaType, params, parsed.Header, parsed.Body)
			if tc.layout != nil {
				tc.layout(t, tree)
			}
		})
	}
}

// TestComposedMessageIsReparseableByTheStandardLibrary is the blunt version of
// the same idea: whatever the structure, net/mail and mime/multipart must be
// able to read it back.
func TestComposedMessageIsReparseableByTheStandardLibrary(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Subject = "Réunion trimestrielle — résultats du 1ᵉʳ trimestre"
	msg.HTML = `<p>Bonjour, <img src="cid:logo@fake.example"></p>`
	msg.Cc = []coremail.AddressValue{{Name: "Doe, John", Addr: "john@fake.example"}}
	msg.Bcc = []coremail.AddressValue{{Addr: "audit@fake.example"}}
	msg.Headers = []coremail.HeaderFieldValue{{Name: "X-Campaign", Value: "q1"}}
	msg.Attachments = []coremail.AttachmentValue{
		{Filename: "logo.png", ContentID: "logo@fake.example", Content: bytes.Repeat([]byte{0x89}, 300)},
		{Filename: "rapport-été.pdf", Content: bytes.Repeat([]byte("%PDF"), 500)},
	}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	parsed, params, mediaType := parseMessage(t, raw)
	readTree(t, mediaType, params, parsed.Header, parsed.Body)

	//: the subject survives the RFC 2047 round trip with its accents intact.
	decoder := new(mime.WordDecoder)
	subject, decodeErr := decoder.DecodeHeader(parsed.Header.Get("Subject"))
	if decodeErr != nil {
		t.Fatalf("Subject did not decode: %v", decodeErr)
	}
	if subject != msg.Subject {
		t.Fatalf("Subject round-tripped to %q, want %q", subject, msg.Subject)
	}
	//: the display name with a comma parses as ONE address, not two.
	cc, addrErr := parsed.Header.AddressList("Cc")
	if addrErr != nil {
		t.Fatalf("Cc did not parse: %v", addrErr)
	}
	if len(cc) != 1 || cc[0].Name != "Doe, John" || cc[0].Address != "john@fake.example" {
		t.Fatalf("Cc = %+v, want one mailbox named %q — an unquoted comma makes it two", cc, "Doe, John")
	}
}

// TestBccIsNeverInTheComposedBytes is the disclosure guard on the composed
// message: the blind recipient reaches RCPT TO and appears nowhere a recipient
// can read.
func TestBccIsNeverInTheComposedBytes(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Bcc = []coremail.AddressValue{{Name: "Auditor", Addr: "blind@fake.example"}}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	if bytes.Contains(raw, []byte("blind@fake.example")) || bytes.Contains(raw, []byte("Auditor")) {
		t.Fatalf("the composed message discloses a Bcc recipient:\n%s", raw)
	}
	envelope, envelopeErr := msg.Envelope()
	if envelopeErr != nil {
		t.Fatalf("Envelope = %v", envelopeErr)
	}
	if len(envelope.To) != 2 || envelope.To[1] != "blind@fake.example" {
		t.Fatalf("envelope.To = %v, want the Bcc recipient present", envelope.To)
	}
}

// TestEveryLineRespectsTheHardLimit pins RFC 5322 §2.1.1 over a message built
// to break it: a very long subject, a long recipient list and a binary
// attachment.
func TestEveryLineRespectsTheHardLimit(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Subject = strings.Repeat("quarterly ", 200)
	for index := range 40 {
		msg.To = append(msg.To, coremail.AddressValue{Name: "Recipient Number", Addr: "user" + strings.Repeat("x", index) + "@fake.example"})
	}
	msg.Attachments = []coremail.AttachmentValue{{Filename: "blob.bin", Content: bytes.Repeat([]byte{0xFE}, 200_000)}}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a header or body line is %d octets, want at most 998 (RFC 5322 §2.1.1): %.60q", len(line), line)
		}
	}
}

// TestUnfoldableHeaderIsRefusedRatherThanTruncated pins the other half of the
// same rule. Folding is legal only at existing whitespace (§2.2.3), so a single
// 1200-octet token has no legal fold point — and cutting it would deliver a
// different value while reporting success.
func TestUnfoldableHeaderIsRefusedRatherThanTruncated(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.Headers = []coremail.HeaderFieldValue{{Name: "X-Trace", Value: strings.Repeat("a", 1200)}}
	_, err := testComposer().Compose(msg)
	if !errs.HasCode(err, coremail.CodeHeaderTooLong) {
		t.Fatalf("Compose = %v, want CodeHeaderTooLong", err)
	}
	//: and the same length WITH whitespace in it is folded and accepted.
	msg.Headers = []coremail.HeaderFieldValue{{Name: "X-Trace", Value: strings.Repeat("a ", 600)}}
	if _, foldErr := testComposer().Compose(msg); foldErr != nil {
		t.Fatalf("Compose(foldable) = %v, want nil", foldErr)
	}
}

// TestBoundaryCannotAppearInAnyEncodedBody pins the structural claim behind the
// boundary format: every leaf is quoted-printable or base64, and neither
// alphabet can produce the two octets "=_" that every boundary starts with.
func TestBoundaryCannotAppearInAnyEncodedBody(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	//: a body that TRIES to contain a boundary, including the exact prefix.
	msg.Text = "=_a7a7a7a7 --=_ boundary-looking text\r\n--=_a7a7\r\n"
	msg.HTML = "<p>=_a7a7a7a7</p>"
	msg.Attachments = []coremail.AttachmentValue{{Filename: "b.bin", Content: []byte("=_=_=_=_=_=_")}}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	parsed, params, mediaType := parseMessage(t, raw)
	//: the proof is that the stdlib reader finds exactly the parts it should,
	//: which it could not if a body had produced a spurious delimiter.
	tree := readTree(t, mediaType, params, parsed.Header, parsed.Body)
	if len(tree.children) != 2 {
		t.Fatalf("mixed has %d parts, want 2 — a body forged a boundary", len(tree.children))
	}
	if got := tree.children[0].children[0].body; got != msg.Text {
		t.Fatalf("text part round-tripped to %q, want %q", got, msg.Text)
	}
}

// TestNestedBoundariesAreAlwaysDistinct pins the second half: the randomness
// source here returns a constant, so if distinctness depended on luck this test
// would fail every time.
func TestNestedBoundariesAreAlwaysDistinct(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.HTML = `<img src="cid:logo@fake.example">`
	msg.Attachments = []coremail.AttachmentValue{
		{Filename: "logo.png", ContentID: "logo@fake.example", Content: []byte("png")},
		{Filename: "r.pdf", Content: []byte("pdf")},
	}
	raw, err := testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if strings.HasPrefix(line, "--=_") && !strings.HasSuffix(line, "--") {
			seen[line] = true
		}
	}
	//: three containers: mixed, related, alternative.
	if len(seen) != 3 {
		t.Fatalf("saw %d distinct boundaries, want 3 (mixed, related, alternative): %v", len(seen), seen)
	}
	parsed, params, mediaType := parseMessage(t, raw)
	readTree(t, mediaType, params, parsed.Header, parsed.Body)
}

// TestComposeIsDeterministic pins that a fixed clock and a fixed randomness
// source produce identical bytes, which is what makes every assertion above
// stable and what lets a consumer golden-test their own templates.
func TestComposeIsDeterministic(t *testing.T) {
	t.Parallel()
	msg := simpleMessage()
	msg.HTML = "<p>hi</p>"
	first, firstErr := testComposer().Compose(msg)
	second, secondErr := testComposer().Compose(msg)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Compose = %v / %v, want nil", firstErr, secondErr)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two composes of the same message produced different bytes")
	}
	if !bytes.Contains(first, []byte("Date: Sat, 14 Mar 2026 09:26:53 +0000")) {
		t.Fatalf("the Date header does not come from the composer's clock:\n%s", first)
	}
}

// TestNoMessageIDIsInventedWhenNoneIsGiven pins the decision to leave the field
// to the submission server (RFC 6409 §8.2) rather than invent entropy and a
// domain the SDK does not own.
func TestNoMessageIDIsInventedWhenNoneIsGiven(t *testing.T) {
	t.Parallel()
	raw, err := testComposer().Compose(simpleMessage())
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	parsed, _, _ := parseMessage(t, raw)
	if got := parsed.Header.Get("Message-Id"); got != "" {
		t.Fatalf("Message-ID = %q, want none when the caller supplied none", got)
	}
	msg := simpleMessage()
	msg.MessageID = "abc123@fake.example"
	raw, err = testComposer().Compose(msg)
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	parsed, _, _ = parseMessage(t, raw)
	if got := parsed.Header.Get("Message-Id"); got != "<abc123@fake.example>" {
		t.Fatalf("Message-ID = %q, want the caller's value in angle brackets", got)
	}
}
