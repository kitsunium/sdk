package mail_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// injectionPayload is a syntactically PERFECT second header, not merely
// invalid input. If the SDK sanitised instead of refusing, the message would
// still be composed and still be delivered — to the attacker as well.
const injectionPayload = "legit\r\nBcc: attacker@evil.example"

// breakCase is one way to poison an otherwise valid message, with the name a
// failure reports.
type breakCase struct {
	name    string
	breakIt func(m *mail.MessageValue)
}

// emptyCase is one zero this domain declines to guess a value for.
type emptyCase struct {
	name   string
	mutate func(m *mail.MessageValue)
	want   errs.Code
}

// addressCase is one addr-spec and the verdict it must receive; a zero want
// means the address is accepted.
type addressCase struct {
	name string
	addr string
	want errs.Code
}

// validMessage returns the smallest message the domain accepts, so a test can
// break exactly one thing about it.
func validMessage() mail.MessageValue {
	return mail.MessageValue{
		From: mail.AddressValue{Name: "Ops", Addr: "ops@example.com"},
		To:   []mail.AddressValue{{Addr: "user@example.net"}},
		Text: "hello",
	}
}

// TestHeaderInjectionIsRefusedOnEveryCallerControlledField is the domain's
// headline guard. Every string a caller can put into a header is driven with a
// CRLF payload that would create a blind-copy field, and every one of them must
// come back as HeaderInjection.
func TestHeaderInjectionIsRefusedOnEveryCallerControlledField(t *testing.T) {
	t.Parallel()
	cases := []breakCase{
		{"subject", func(m *mail.MessageValue) { m.Subject = injectionPayload }},
		{"from display name", func(m *mail.MessageValue) { m.From.Name = injectionPayload }},
		{"from addr-spec", func(m *mail.MessageValue) { m.From.Addr = "ops@a.example" + injectionPayload }},
		{"to display name", func(m *mail.MessageValue) { m.To[0].Name = injectionPayload }},
		{"to addr-spec", func(m *mail.MessageValue) { m.To[0].Addr = "user@a.example" + injectionPayload }},
		{"cc addr-spec", func(m *mail.MessageValue) {
			m.Cc = []mail.AddressValue{{Addr: "c@a.example" + injectionPayload}}
		}},
		{"bcc addr-spec", func(m *mail.MessageValue) {
			m.Bcc = []mail.AddressValue{{Addr: "b@a.example" + injectionPayload}}
		}},
		{"reply-to addr-spec", func(m *mail.MessageValue) {
			m.ReplyTo = []mail.AddressValue{{Addr: "r@a.example" + injectionPayload}}
		}},
		{"message id", func(m *mail.MessageValue) { m.MessageID = injectionPayload }},
		{"extra header value", func(m *mail.MessageValue) {
			m.Headers = []mail.HeaderFieldValue{{Name: "X-Tag", Value: injectionPayload}}
		}},
		{"extra header name", func(m *mail.MessageValue) {
			m.Headers = []mail.HeaderFieldValue{{Name: "X-Tag" + injectionPayload, Value: "v"}}
		}},
		{"attachment name", func(m *mail.MessageValue) {
			m.Attachments = []mail.AttachmentValue{{Filename: "a" + injectionPayload, Content: []byte("x")}}
		}},
		{"attachment type", func(m *mail.MessageValue) {
			m.Attachments = []mail.AttachmentValue{
				{Filename: "a.txt", ContentType: "text/plain" + injectionPayload, Content: []byte("x")},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := validMessage()
			tc.breakIt(&msg)
			err := mail.Validate(msg)
			if err == nil {
				t.Fatalf("Validate accepted a CRLF payload in %s — the composed message would carry an injected blind copy", tc.name)
			}
			if !errs.HasCode(err, mail.CodeHeaderInjection) {
				t.Fatalf("Validate(%s) = %v, want CodeHeaderInjection", tc.name, err)
			}
		})
	}
}

// TestHeaderInjectionRefusesBareCRAndBareLF pins that half a CRLF is already
// fatal. A receiver that normalises a lone LF into a CRLF reconstructs the
// attack downstream from input this domain would otherwise have let through.
func TestHeaderInjectionRefusesBareCRAndBareLF(t *testing.T) {
	t.Parallel()
	payloads := []string{
		"a\nBcc: x@evil.example",
		"a\rBcc: x@evil.example",
		"a\r\nBcc: x@evil.example",
		"a\x00Bcc: x@evil.example",
		"a\n",
	}
	for _, payload := range payloads {
		if err := mail.ValidateHeaderValue("Subject", payload); !errs.HasCode(err, mail.CodeHeaderInjection) {
			t.Fatalf("ValidateHeaderValue(%q) = %v, want CodeHeaderInjection", payload, err)
		}
	}
	//: the negative control: a value with none of the three passes.
	if err := mail.ValidateHeaderValue("Subject", "a perfectly ordinary subject"); err != nil {
		t.Fatalf("ValidateHeaderValue(clean) = %v, want nil", err)
	}
}

// TestHeaderInjectionErrorNeverDisclosesTheValue is the non-disclosure guard,
// and it is a security property with its own test for the same reason
// validation, authz and view have one: an error message travels to places the
// SDK does not control, and the value it would carry is by construction the
// string an attacker chose.
func TestHeaderInjectionErrorNeverDisclosesTheValue(t *testing.T) {
	t.Parallel()
	const secret = "attacker@evil.example"
	msg := validMessage()
	msg.Subject = "hello\r\nBcc: " + secret
	err := mail.Validate(msg)
	if err == nil {
		t.Fatal("Validate accepted an injected subject")
	}
	rendered := err.Error()
	if strings.Contains(rendered, secret) || strings.Contains(rendered, "Bcc") {
		t.Fatalf("err.Error() = %q — it discloses the refused value", rendered)
	}
	if public := errs.PublicOf(err); strings.Contains(public, secret) {
		t.Fatalf("Public = %q — it discloses the refused value", public)
	}
	//: the field NAME is what the caller needs, and it is present.
	if !strings.Contains(rendered, "HEADER_INJECTION") {
		t.Fatalf("err.Error() = %q, want the HEADER_INJECTION reason", rendered)
	}
	//: and the diagnosis is reachable, log-only, through the fields.
	fields := errs.FieldsOf(err)
	if len(fields) == 0 {
		t.Fatal("no fields on the refusal — the header name is not diagnosable")
	}
	for _, field := range fields {
		if strings.Contains(field.Key()+"="+field.StringValue(), secret) {
			t.Fatalf("field %q carries the refused value", field.Key())
		}
	}
}

// TestBccNeverBecomesAHeaderAndAlwaysBecomesARecipient pins the one decision
// RFC 5322 §3.6.3 leaves open. The envelope must name the blind recipient and
// nothing the recipient can read may.
func TestBccNeverBecomesAHeaderAndAlwaysBecomesARecipient(t *testing.T) {
	t.Parallel()
	msg := validMessage()
	msg.Cc = []mail.AddressValue{{Addr: "cc@example.org"}}
	msg.Bcc = []mail.AddressValue{{Addr: "blind@example.org"}}
	envelope, err := msg.Envelope()
	if err != nil {
		t.Fatalf("Envelope() = %v, want nil", err)
	}
	want := []string{"user@example.net", "cc@example.org", "blind@example.org"}
	if !reflect.DeepEqual(envelope.To, want) {
		t.Fatalf("envelope.To = %v, want %v (To, then Cc, then Bcc)", envelope.To, want)
	}
	if envelope.From != "ops@example.com" {
		t.Fatalf("envelope.From = %q, want the From addr-spec", envelope.From)
	}
}

// TestValidateRefusesTheEmptyShapes covers ADR 0031's refusing half: every zero
// this domain declines to guess a value for.
func TestValidateRefusesTheEmptyShapes(t *testing.T) {
	t.Parallel()
	cases := []emptyCase{
		{"no sender", func(m *mail.MessageValue) { m.From = mail.AddressValue{} }, mail.CodeMissingSender},
		{"no recipients", func(m *mail.MessageValue) { m.To = nil }, mail.CodeNoRecipients},
		{"no body", func(m *mail.MessageValue) { m.Text = "" }, mail.CodeEmptyBody},
		{"inline with no body", func(m *mail.MessageValue) {
			m.Text = ""
			m.Attachments = []mail.AttachmentValue{{Filename: "l.png", ContentID: "l@x.example", Content: []byte("x")}}
		}, mail.CodeInvalidAttachment},
		{"unnamed attachment", func(m *mail.MessageValue) {
			m.Attachments = []mail.AttachmentValue{{Content: []byte("x")}}
		}, mail.CodeInvalidAttachment},
		{"filename with a path separator", func(m *mail.MessageValue) {
			m.Attachments = []mail.AttachmentValue{{Filename: "../../etc/passwd", Content: []byte("x")}}
		}, mail.CodeInvalidAttachment},
		{"unspellable media type", func(m *mail.MessageValue) {
			m.Attachments = []mail.AttachmentValue{{Filename: "a.bin", ContentType: "pdf", Content: []byte("x")}}
		}, mail.CodeInvalidAttachment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := validMessage()
			tc.mutate(&msg)
			if err := mail.Validate(msg); !errs.HasCode(err, tc.want) {
				t.Fatalf("Validate(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestReservedHeadersAreRefusedCaseInsensitively pins the second half of the
// injection defence: a caller may not spell a composer-owned field by hand
// either, and "bCc" is Bcc because RFC 5322 makes a field name
// case-insensitive.
func TestReservedHeadersAreRefusedCaseInsensitively(t *testing.T) {
	t.Parallel()
	reserved := []string{"Bcc", "bcc", "bCc", "BCC", "From", "subject", "Content-Type", "MIME-Version"}
	for _, name := range reserved {
		msg := validMessage()
		msg.Headers = []mail.HeaderFieldValue{{Name: name, Value: "attacker@evil.example"}}
		if err := mail.Validate(msg); !errs.HasCode(err, mail.CodeReservedHeader) {
			t.Fatalf("Validate(Headers[%q]) = %v, want CodeReservedHeader", name, err)
		}
	}
	//: and a field the composer does not own is accepted.
	msg := validMessage()
	msg.Headers = []mail.HeaderFieldValue{{Name: "X-Campaign", Value: "spring"}}
	if err := mail.Validate(msg); err != nil {
		t.Fatalf("Validate(X-Campaign) = %v, want nil", err)
	}
}

// TestAddressGrammar covers the accepted subset and the two forms refused by
// name rather than reported as malformed.
func TestAddressGrammar(t *testing.T) {
	t.Parallel()
	cases := []addressCase{
		{"plain", "user@example.com", 0},
		{"dotted", "first.last@sub.example.com", 0},
		{"plus tag", "user+tag@example.com", 0},
		{"no at", "userexample.com", mail.CodeInvalidAddress},
		{"two at", "user@a@example.com", mail.CodeInvalidAddress},
		{"empty local", "@example.com", mail.CodeInvalidAddress},
		{"empty domain", "user@", mail.CodeInvalidAddress},
		{"leading dot", ".user@example.com", mail.CodeInvalidAddress},
		{"doubled dot", "user..name@example.com", mail.CodeInvalidAddress},
		{"space", "us er@example.com", mail.CodeInvalidAddress},
		{"angle bracket", "user<@example.com", mail.CodeInvalidAddress},
		{"comma", "user,other@example.com", mail.CodeInvalidAddress},
		{"local too long", strings.Repeat("a", 65) + "@example.com", mail.CodeInvalidAddress},
		{"domain too long", "user@" + strings.Repeat("a", 250) + ".example.com", mail.CodeInvalidAddress},
		{"non-ascii local (SMTPUTF8)", "réunion@example.com", mail.CodeUnsupportedAddress},
		{"non-ascii domain", "user@exämple.com", mail.CodeUnsupportedAddress},
		{"quoted local part", `"john doe"@example.com`, mail.CodeUnsupportedAddress},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := mail.ValidateAddress("To[0]", mail.AddressValue{Addr: tc.addr})
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("ValidateAddress(%q) = %v, want nil", tc.addr, err)
				}
				return
			}
			if !errs.HasCode(err, tc.want) {
				t.Fatalf("ValidateAddress(%q) = %v, want %v", tc.addr, err, tc.want)
			}
		})
	}
}

// TestNeedsQuotedDisplayName pins the grammar rule the composer depends on. An
// unquoted "Doe, John" makes the comma a list separator, so a message to one
// person parses as a message to two.
func TestNeedsQuotedDisplayName(t *testing.T) {
	t.Parallel()
	bare := []string{"", "Ops", "Ops Team"}
	for _, name := range bare {
		if mail.NeedsQuotedDisplayName(name) {
			t.Fatalf("NeedsQuotedDisplayName(%q) = true, want false", name)
		}
	}
	quoted := []string{"Doe, John", "a@b", `say "hi"`, " leading", "trailing ", "semi;colon", "paren(thesis)"}
	for _, name := range quoted {
		if !mail.NeedsQuotedDisplayName(name) {
			t.Fatalf("NeedsQuotedDisplayName(%q) = false, want true", name)
		}
	}
}

// TestValidMessagePasses is the negative control: without it, a guard that
// refuses everything would pass every test above.
func TestValidMessagePasses(t *testing.T) {
	t.Parallel()
	msg := validMessage()
	msg.Subject = "Réunion à 9h"
	msg.HTML = "<p>hello</p>"
	msg.Cc = []mail.AddressValue{{Name: "Doe, John", Addr: "john@example.org"}}
	msg.Headers = []mail.HeaderFieldValue{{Name: "X-Campaign", Value: "spring"}}
	msg.Attachments = []mail.AttachmentValue{
		{Filename: "report.pdf", Content: []byte("%PDF")},
		{Filename: "logo.png", ContentID: "logo@example.com", Content: []byte("\x89PNG")},
	}
	if err := mail.Validate(msg); err != nil {
		t.Fatalf("Validate(a fully populated valid message) = %v, want nil", err)
	}
}
