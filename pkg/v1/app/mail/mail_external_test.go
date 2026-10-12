package mail_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// notify is the shape a consumer's code takes: it asks for the narrowest thing
// it uses, which is the frozen port and not the union.
func notify(ctx context.Context, transport mail.Transport, to string) error {
	return transport.Send(ctx, mail.Message{
		From:    mail.Address{Name: "Ops", Addr: "ops@example.com"},
		To:      []mail.Address{{Addr: to}},
		Bcc:     []mail.Address{{Addr: "audit@example.com"}},
		Subject: "Réunion à 9h",
		Text:    "Bonjour,",
		HTML:    "<p>Bonjour,</p>",
	})
}

// TestTheDoubleIsWiredWhereTheTransportGoes is the facade's whole job in one
// test: a consumer wires [mail.NewMemory] where production wires
// [mail.NewSMTP], and asserts on the bytes that would have gone out.
func TestTheDoubleIsWiredWhereTheTransportGoes(t *testing.T) {
	t.Parallel()
	box := mail.NewMemory()
	if err := notify(context.Background(), box, "user@example.net"); err != nil {
		t.Fatalf("notify = %v, want nil", err)
	}
	sent := box.Sent()
	if len(sent) != 1 {
		t.Fatalf("recorded %d deliveries, want 1", len(sent))
	}
	raw := string(sent[0].Raw)
	//: the structure the fields imply.
	if !strings.Contains(raw, "Content-Type: multipart/alternative") {
		t.Fatalf("text + HTML did not become a multipart/alternative:\n%s", raw)
	}
	//: the subject arrives encoded, not raw.
	if !strings.Contains(raw, "=?utf-8?q?R=C3=A9union_=C3=A0_9h?=") {
		t.Fatalf("the subject was not RFC 2047 encoded:\n%s", raw)
	}
	//: and the blind recipient is in the envelope and nowhere else.
	if strings.Contains(raw, "audit@example.com") {
		t.Fatalf("the composed bytes disclose the Bcc recipient:\n%s", raw)
	}
	if len(sent[0].Envelope.To) != 2 || sent[0].Envelope.To[1] != "audit@example.com" {
		t.Fatalf("envelope.To = %v, want the Bcc recipient", sent[0].Envelope.To)
	}
}

// TestHeaderInjectionIsRefusedThroughTheFacade pins that the guard is reachable
// and typed from the public surface, with the sentinel this package exports.
func TestHeaderInjectionIsRefusedThroughTheFacade(t *testing.T) {
	t.Parallel()
	box := mail.NewMemory()
	err := box.Send(context.Background(), mail.Message{
		From:    mail.Address{Addr: "ops@example.com"},
		To:      []mail.Address{{Addr: "user@example.net"}},
		Subject: "hi\r\nBcc: attacker@evil.example",
		Text:    "body",
	})
	if err == nil {
		t.Fatal("Send accepted an injected subject")
	}
	//: errors.Is against the exported sentinel is the consumer-facing way to
	//: match one refusal; errs.Is compares (Code, Reason) rather than pointers.
	if !errors.Is(err, mail.HeaderInjection) {
		t.Fatalf("errors.Is(err, mail.HeaderInjection) = false for %v", err)
	}
	if code, ok := errs.CodeOf(err); !ok || code.String() != "0.2.31.1" {
		t.Fatalf("CodeOf = %v (ok=%v), want 0.2.31.1", code, ok)
	}
	if strings.Contains(err.Error(), "attacker@evil.example") {
		t.Fatalf("err.Error() = %q — it discloses the refused value", err)
	}
}

// TestTLSModeZeroValueIsRefused pins ADR 0031 at the public constructor: the
// caller must name a mode, because neither "encrypt" nor "do not" is a safe
// guess on their behalf.
func TestTLSModeZeroValueIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := mail.NewSMTP(mail.SMTPConfig{Host: "relay.example", Port: 587}); err == nil {
		t.Fatal("NewSMTP accepted a configuration with no TLS mode")
	}
	//: and a named mode is accepted.
	if _, err := mail.NewSMTP(mail.SMTPConfig{Host: "relay.example", Port: 587, TLS: mail.TLSStartTLS}); err != nil {
		t.Fatalf("NewSMTP(TLSStartTLS) = %v, want nil", err)
	}
	//: credentials over a session that is unencrypted by configuration are
	//: refused where the fix is one line.
	_, err := mail.NewSMTP(mail.SMTPConfig{
		Host: "relay.example", Port: 25, TLS: mail.TLSDisabled, Username: "u", Password: "p",
	})
	if err == nil {
		t.Fatal("NewSMTP accepted credentials over a cleartext session")
	}
}

// TestComposeIsAvailableWithoutATransport pins that a caller who hands the
// bytes to something this SDK does not implement still gets the composer and
// its guards.
func TestComposeIsAvailableWithoutATransport(t *testing.T) {
	t.Parallel()
	raw, err := mail.Compose(mail.Message{
		From: mail.Address{Addr: "ops@example.com"},
		To:   []mail.Address{{Addr: "user@example.net"}},
		Text: "body",
	})
	if err != nil {
		t.Fatalf("Compose = %v, want nil", err)
	}
	if !strings.HasPrefix(string(raw), "From: <ops@example.com>\r\n") {
		t.Fatalf("composed message starts with %q", string(raw[:40]))
	}
	//: and Validate answers the same question at the edge.
	if err := mail.Validate(mail.Message{From: mail.Address{Addr: "ops@example.com"}}); err == nil {
		t.Fatal("Validate accepted a message with no recipients")
	}
}

// TestFieldsOfReadsTheClauseAnEmitterAttached reads, with the public names
// alone, the clause mail.ParseURL attaches to a refusal — what a framework's
// settings page shows beside a setting it could not use — and pins the two
// promises the FieldsOf doc makes: the fields come oldest cause first, newest
// wrapper last, and the password in the URL is in no field and in no message.
func TestFieldsOfReadsTheClauseAnEmitterAttached(t *testing.T) {
	t.Parallel()
	const password = "correct-horse-battery"
	type tc struct {
		name string
		raw  string
		want string
	}
	tests := []tc{
		{
			"an unknown tls mode",
			"smtp://camille:" + password + "@mail.example.com:587?tls=bogus",
			"has a tls parameter that is not starttls, implicit or none",
		},
		{
			"a port out of range",
			"smtp://camille:" + password + "@mail.example.com:99999",
			"has a port that is not a number from 1 to 65535",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := mail.ParseURL(c.raw)
		if !errors.Is(err, mail.InvalidURL) {
			t.Fatalf("ParseURL = %v, want InvalidURL", err)
		}
		//: a caller adds its own clause on the way up; it comes last.
		wrapped := errs.Wrap(err, errs.WrapParams{Code: 0x40_01_01_01}, errs.String("setting", "KIT_SMTP_URL"))
		fields := errs.FieldsOf(wrapped)
		keys := make([]string, 0, len(fields))
		problem := ""
		for _, field := range fields {
			keys = append(keys, field.Key())
			if field.Key() == "problem" {
				problem = field.StringValue()
			}
			if strings.Contains(field.StringValue(), password) {
				t.Errorf("field %q carries the password", field.Key())
			}
		}
		if !slices.Equal(keys, []string{"problem", "setting"}) {
			t.Fatalf("FieldsOf keys = %v, want [problem setting]", keys)
		}
		if problem != c.want {
			t.Fatalf("problem = %q, want %q", problem, c.want)
		}
		for _, text := range []string{wrapped.Error(), errs.PublicOf(wrapped), errs.PrivateOf(wrapped)} {
			if strings.Contains(text, password) {
				t.Errorf("a rendering carries the password: %q", text)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
