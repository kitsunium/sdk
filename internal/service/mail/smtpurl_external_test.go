package mail_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// urlPassword is the password the refusal cases hunt for in every verdict.
const urlPassword string = "u4l-s3cr3t-77"

// TestParseURLReadsWhatNewSMTPAccepts pins the accepted grammar, its defaults,
// and that every result is a configuration NewSMTP takes.
func TestParseURLReadsWhatNewSMTPAccepts(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  string
		want svcmail.SMTPConfig
	}
	tests := []tc{
		{
			"smtp defaults to STARTTLS on 587", "smtp://relay.example",
			svcmail.SMTPConfig{Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS},
		},
		{
			"smtps defaults to implicit TLS on 465", "smtps://relay.example",
			svcmail.SMTPConfig{Host: "relay.example", Port: 465, TLS: svcmail.TLSImplicit},
		},
		{
			"tls=none defaults to the relay port", "smtp://relay.example?tls=none",
			svcmail.SMTPConfig{Host: "relay.example", Port: 25, TLS: svcmail.TLSDisabled},
		},
		{
			"tls=implicit on smtp", "smtp://relay.example?tls=implicit",
			svcmail.SMTPConfig{Host: "relay.example", Port: 465, TLS: svcmail.TLSImplicit},
		},
		{
			"smtps may repeat implicit", "smtps://relay.example?tls=implicit",
			svcmail.SMTPConfig{Host: "relay.example", Port: 465, TLS: svcmail.TLSImplicit},
		},
		{
			"an explicit port and credentials", "smtp://bob:pw@relay.example:2525?tls=starttls",
			svcmail.SMTPConfig{Host: "relay.example", Port: 2525, TLS: svcmail.TLSStartTLS, Username: "bob", Password: "pw"},
		},
		{
			"a percent-encoded password", "smtp://bob:p%40ss%3Aw%2Fd@relay.example",
			svcmail.SMTPConfig{Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS, Username: "bob", Password: "p@ss:w/d"},
		},
		{
			"a user and no password", "smtp://bob@relay.example",
			svcmail.SMTPConfig{Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS, Username: "bob"},
		},
		{
			"a scheme in capitals and a trailing slash", "SMTP://relay.example/",
			svcmail.SMTPConfig{Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS},
		},
		{
			"an IPv6 literal", "smtp://[::1]:25?tls=none",
			svcmail.SMTPConfig{Host: "::1", Port: 25, TLS: svcmail.TLSDisabled},
		},
		{
			"a tls value in capitals", "smtp://relay.example?tls=NONE",
			svcmail.SMTPConfig{Host: "relay.example", Port: 25, TLS: svcmail.TLSDisabled},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := svcmail.ParseURL(c.raw)
		if err != nil {
			t.Fatalf("%s: ParseURL = %v", c.name, err)
		}
		if !sameConfig(got, c.want) {
			t.Fatalf("%s: ParseURL = %+v, want %+v", c.name, got, c.want)
		}
		if _, buildErr := svcmail.NewSMTP(got); buildErr != nil {
			t.Fatalf("%s: NewSMTP refused what ParseURL returned: %v", c.name, buildErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestParseURLRefusesWithoutQuotingTheURL pins every refusal, the verdict it
// carries, and that no refusal repeats the URL, its password or its host.
func TestParseURLRefusesWithoutQuotingTheURL(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  string
		want errs.Code
	}
	credentials := "bob:" + urlPassword + "@"
	tests := []tc{
		{"not a URL", "%zz" + urlPassword, svcmail.CodeInvalidURL},
		{"an opaque URL", "smtp:" + urlPassword, svcmail.CodeInvalidURL},
		{"another scheme", "https://" + credentials + "relay.example", svcmail.CodeInvalidURL},
		{"a path", "smtp://" + credentials + "relay.example/inbox", svcmail.CodeInvalidURL},
		{"a fragment", "smtp://" + credentials + "relay.example#x", svcmail.CodeInvalidURL},
		{"an unknown parameter", "smtp://" + credentials + "relay.example?tsl=none", svcmail.CodeInvalidURL},
		{"a parameter named like the password", "smtp://relay.example?" + urlPassword, svcmail.CodeInvalidURL},
		{"tls twice", "smtp://" + credentials + "relay.example?tls=starttls&tls=starttls", svcmail.CodeInvalidURL},
		{"an unknown tls mode", "smtp://" + credentials + "relay.example?tls=opportunistic", svcmail.CodeInvalidURL},
		{"smtps asking for STARTTLS", "smtps://" + credentials + "relay.example?tls=starttls", svcmail.CodeInvalidURL},
		{"smtps asking for plaintext", "smtps://" + credentials + "relay.example?tls=none", svcmail.CodeInvalidURL},
		{"no host", "smtp://" + credentials + ":587", svcmail.CodeInvalidURL},
		{"port zero", "smtp://" + credentials + "relay.example:0", svcmail.CodeInvalidURL},
		{"a port past the range", "smtp://" + credentials + "relay.example:70000", svcmail.CodeInvalidURL},
		{"a port that is not a number", "smtp://" + credentials + "relay.example:x", svcmail.CodeInvalidURL},
		{"credentials in the clear", "smtp://" + credentials + "relay.example?tls=none", svcmail.CodeAuthInsecure},
		{"a password without a user", "smtp://:" + urlPassword + "@relay.example", svcmail.CodeInvalidConfig},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := svcmail.ParseURL(c.raw)
		if !errs.HasCode(err, c.want) {
			t.Fatalf("%s: ParseURL = %v, want %v", c.name, err, c.want)
		}
		if !sameConfig(got, svcmail.SMTPConfig{}) {
			t.Errorf("%s: a refused URL handed back a configuration", c.name)
		}
		rendered := err.Error() + errs.PrivateOf(err)
		for _, field := range errs.FieldsOf(err) {
			rendered += field.Key() + "=" + field.StringValue() + " "
		}
		for _, leak := range []string{urlPassword, "relay.example", c.raw} {
			if strings.Contains(rendered, leak) {
				t.Fatalf("%s: the refusal repeats %q: %s", c.name, leak, rendered)
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

// sameConfig compares every field a URL can set; SMTPConfig itself is not
// comparable, because its TLS identity holds slices.
func sameConfig(left, right svcmail.SMTPConfig) bool {
	return left.Host == right.Host && left.Port == right.Port && left.TLS == right.TLS &&
		left.Username == right.Username && left.Password == right.Password &&
		left.LocalName == right.LocalName && left.DialTimeout == right.DialTimeout &&
		left.Identity.IsZero() == right.Identity.IsZero()
}
