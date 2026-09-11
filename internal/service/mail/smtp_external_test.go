package mail_test

import (
	"context"
	"encoding/base64"
	"net/smtp"
	"strconv"
	"strings"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// configCase is one way an SMTP configuration can be unusable, and the verdict
// the constructor must give it.
type configCase struct {
	name   string
	mutate func(c *svcmail.SMTPConfig)
	want   errs.Code
}

// clientIdentity trusts the test certificate and nothing else, which is what a
// private-PKI relay looks like — and what makes a failed verification a test
// failure rather than a platform-store accident.
func clientIdentity(t *testing.T) corenet.IdentityValue {
	t.Helper()
	certPEM, _ := testMaterial(t)
	identity, err := corenet.NewIdentityValue(corenet.IdentityParams{RootsPEM: certPEM, MinVersion: 0x0303})
	if err != nil {
		t.Fatalf("build client identity: %v", err)
	}
	return identity
}

// smtpConfig is the base configuration every case starts from.
func smtpConfig(t *testing.T, port int, mode svcmail.TLSMode) svcmail.SMTPConfig {
	t.Helper()
	return svcmail.SMTPConfig{
		Host:        "127.0.0.1",
		Port:        port,
		TLS:         mode,
		Identity:    clientIdentity(t),
		DialTimeout: 5 * time.Second,
	}
}

// TestSMTPRefusesToDowngradeWhenSTARTTLSIsNotOffered is the security headline.
//
// The server is a perfectly ordinary relay that simply does not advertise
// STARTTLS — which is also exactly what an on-path attacker produces by
// stripping one line from the EHLO response. Almost every mail library ships
// "opportunistic STARTTLS" and would carry on in the clear here, reporting a
// successful send.
//
// The assertion is not only that the call fails. It is that the session ended
// at that point: no MAIL, no RCPT, no DATA ever reached the socket.
func TestSMTPRefusesToDowngradeWhenSTARTTLSIsNotOffered(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optAUTH)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	err := transport.Send(context.Background(), simpleMessage())
	if !errs.HasCode(err, svcmail.CodeTLSRequired) {
		t.Fatalf("Send = %v, want CodeTLSRequired — the session must not continue in the clear", err)
	}
	for _, line := range server.allLines() {
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "MAIL") || strings.HasPrefix(upper, "RCPT") || upper == "DATA" {
			t.Fatalf("the transport sent %q after the downgrade was refused", line)
		}
	}
}

// TestSMTPNeverSendsCredentialsOverCleartext proves the guard that net/smtp
// will not provide.
//
// smtp.PlainAuth carves out an exception for a server named localhost — the
// literal source reads `if !server.TLS && !isLocalhost(server.Name)` — and
// sends the password in the clear to it. In a container the relay one hop away
// IS 127.0.0.1, and the network between them is a bridge somebody else can
// join, so that carve-out is a credential leak with a comment explaining why it
// is fine.
//
// The second half of this test is the part that makes the first half mean
// something: the SAME fake server, driven by the standard library instead,
// receives the base64 credential in plain view. Without it, a guard that never
// fired would look identical to a guard that works.
func TestSMTPNeverSendsCredentialsOverCleartext(t *testing.T) {
	t.Parallel()
	const username = "postmaster@fake.example"
	const password = "hunter2-not-a-real-secret"

	t.Run("this SDK refuses before writing AUTH", func(t *testing.T) {
		t.Parallel()
		server, port := newFakeSMTP(t, optAUTH)
		cfg := smtpConfig(t, port, svcmail.TLSStartTLS)
		cfg.Username, cfg.Password = username, password
		transport, buildErr := svcmail.NewSMTP(cfg)
		if buildErr != nil {
			t.Fatalf("NewSMTP = %v, want nil", buildErr)
		}
		//: the downgrade is refused first, so AUTH is never reached at all.
		if err := transport.Send(context.Background(), simpleMessage()); err == nil {
			t.Fatal("Send succeeded against a cleartext server carrying credentials")
		}
		assertNoCredentialOnTheWire(t, server.clearLines(), username, password)
	})

	t.Run("and refuses again when TLS is disabled outright", func(t *testing.T) {
		t.Parallel()
		_, port := newFakeSMTP(t, optDefault)
		cfg := smtpConfig(t, port, svcmail.TLSDisabled)
		cfg.Username, cfg.Password = username, password
		//: refused at CONSTRUCTION: the fix is one line in the wiring, and the
		//: transport never exists to be called at three in the morning.
		if _, buildErr := svcmail.NewSMTP(cfg); !errs.HasCode(buildErr, svcmail.CodeAuthInsecure) {
			t.Fatalf("NewSMTP(TLSDisabled + credentials) = %v, want CodeAuthInsecure", buildErr)
		}
	})

	t.Run("while net/smtp sends it to the same server", func(t *testing.T) {
		t.Parallel()
		server, port := newFakeSMTP(t, optAUTH)
		address := "127.0.0.1:" + strconv.Itoa(port)
		//: the stdlib's own path, with its own auth helper, against the very
		//: same cleartext server.
		//: the stdlib is EXPECTED to fail here — the fake server closes on QUIT
		//: and the point of the call is what it wrote before that, not whether
		//: it succeeded.
		if sendErr := smtp.SendMail(address, smtp.PlainAuth("", username, password, "127.0.0.1"),
			"ops@fake.example", []string{"user@fake.example"}, []byte("Subject: x\r\n\r\nbody\r\n")); sendErr != nil {
			t.Logf("net/smtp.SendMail returned %v; what matters is what it put on the wire first", sendErr)
		}
		wire := strings.Join(server.clearLines(), "\n")
		credential := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
		if !strings.Contains(wire, credential) {
			t.Skipf("net/smtp did not send the credential in this Go version — the SDK guard is still the one that matters")
		}
		t.Logf("net/smtp sent the credential over cleartext to a localhost relay; this SDK does not")
	})
}

// assertNoCredentialOnTheWire fails when anything resembling a credential
// appears in the lines the server read unencrypted.
func assertNoCredentialOnTheWire(t *testing.T, lines []string, username, password string) {
	t.Helper()
	credential := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
	for _, line := range lines {
		if strings.HasPrefix(strings.ToUpper(line), "AUTH") {
			t.Fatalf("an AUTH command reached an unencrypted socket: %q", line)
		}
		for _, secret := range []string{password, credential, username} {
			if strings.Contains(line, secret) {
				t.Fatalf("a credential reached an unencrypted socket in %q", line)
			}
		}
	}
}

// TestSMTPSendsOverSTARTTLS is the happy path for port 587, and it also pins
// that the credential travels only after the upgrade.
func TestSMTPSendsOverSTARTTLS(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optDefault)
	cfg := smtpConfig(t, port, svcmail.TLSStartTLS)
	cfg.Username, cfg.Password = "postmaster@fake.example", "hunter2-not-a-real-secret"
	transport, buildErr := svcmail.NewSMTP(cfg)
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	if err := transport.Send(context.Background(), simpleMessage()); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	assertNoCredentialOnTheWire(t, server.clearLines(), cfg.Username, cfg.Password)
	if body := server.message(); !strings.Contains(body, "Subject: Quarterly report") {
		t.Fatalf("the server did not receive the composed message; got %q", body)
	}
}

// TestSMTPSendsOverImplicitTLS is the port-465 shape RFC 8314 §3.3 prefers:
// there is no plaintext phase at all, so nothing can be stripped from it.
func TestSMTPSendsOverImplicitTLS(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optAUTH|optImplicitTLS)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSImplicit))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	if err := transport.Send(context.Background(), simpleMessage()); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if len(server.clearLines()) != 0 {
		t.Fatalf("something crossed the socket before TLS: %v", server.clearLines())
	}
}

// TestSMTPRefusesAnUntrustedCertificate pins that the identity's trust anchors
// are actually used. An empty identity trusts the platform store, which does
// not contain a certificate this test minted a moment ago.
func TestSMTPRefusesAnUntrustedCertificate(t *testing.T) {
	t.Parallel()
	_, port := newFakeSMTP(t, optDefault|optImplicitTLS)
	cfg := smtpConfig(t, port, svcmail.TLSImplicit)
	//: the zero identity: platform trust store, TLS 1.3 minimum.
	cfg.Identity = corenet.IdentityValue{}
	transport, buildErr := svcmail.NewSMTP(cfg)
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	if err := transport.Send(context.Background(), simpleMessage()); !errs.HasCode(err, svcmail.CodeTLSFailed) {
		t.Fatalf("Send = %v, want CodeTLSFailed against an untrusted certificate", err)
	}
}

// TestSMTPSetsTheVerifiedName pins the fix for a defect net/smtp hands every
// caller: Client.StartTLS passes the *tls.Config to tls.Client unchanged, so an
// empty ServerName fails the handshake with "either ServerName or
// InsecureSkipVerify must be specified" — and the obvious escape from that
// message accepts a certificate from anybody.
//
// The identity here names nothing, so the transport must fill the name in
// itself. A successful STARTTLS is the proof.
func TestSMTPSetsTheVerifiedName(t *testing.T) {
	t.Parallel()
	_, port := newFakeSMTP(t, optDefault)
	cfg := smtpConfig(t, port, svcmail.TLSStartTLS)
	certPEM, _ := testMaterial(t)
	//: RootsPEM only: no ServerName anywhere in the identity.
	identity, identityErr := corenet.NewIdentityValue(corenet.IdentityParams{RootsPEM: certPEM, MinVersion: 0x0303})
	if identityErr != nil {
		t.Fatalf("build identity: %v", identityErr)
	}
	cfg.Identity = identity
	transport, buildErr := svcmail.NewSMTP(cfg)
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	if err := transport.Send(context.Background(), simpleMessage()); err != nil {
		t.Fatalf("Send = %v, want nil — the transport must supply the verified name net/smtp does not", err)
	}
}

// TestSMTPReportsARefusedRecipient pins that a server's failure reply becomes a
// typed refusal, and that the reply text stays out of the Public half.
func TestSMTPReportsARefusedRecipient(t *testing.T) {
	t.Parallel()
	_, port := newFakeSMTP(t, optDefault|optRefuseRcpt)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	err := transport.Send(context.Background(), simpleMessage())
	if !errs.HasCode(err, svcmail.CodeSendRefused) {
		t.Fatalf("Send = %v, want CodeSendRefused", err)
	}
	if public := errs.PublicOf(err); strings.Contains(public, "no such user") {
		t.Fatalf("Public = %q — a server reply is third-party text and belongs in a field", public)
	}
}

// TestSMTPRefusesAnInvalidConfiguration covers ADR 0031 at the constructor,
// including the zero TLS mode that is the whole reason TLSMode has one.
func TestSMTPRefusesAnInvalidConfiguration(t *testing.T) {
	t.Parallel()
	base := svcmail.SMTPConfig{Host: "relay.example", Port: 587, TLS: svcmail.TLSStartTLS}
	cases := []configCase{
		{"unset TLS mode", func(c *svcmail.SMTPConfig) { c.TLS = svcmail.TLSUnset }, svcmail.CodeInvalidConfig},
		{"empty host", func(c *svcmail.SMTPConfig) { c.Host = "" }, svcmail.CodeInvalidConfig},
		{"port zero", func(c *svcmail.SMTPConfig) { c.Port = 0 }, svcmail.CodeInvalidConfig},
		{"port too high", func(c *svcmail.SMTPConfig) { c.Port = 70000 }, svcmail.CodeInvalidConfig},
		{"unknown mode", func(c *svcmail.SMTPConfig) { c.TLS = svcmail.TLSMode(9) }, svcmail.CodeInvalidConfig},
		{"password without user", func(c *svcmail.SMTPConfig) { c.Password = "x" }, svcmail.CodeInvalidConfig},
		{"credentials without TLS", func(c *svcmail.SMTPConfig) {
			c.TLS = svcmail.TLSDisabled
			c.Username = "u"
		}, svcmail.CodeAuthInsecure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)
			if _, err := svcmail.NewSMTP(cfg); !errs.HasCode(err, tc.want) {
				t.Fatalf("NewSMTP(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
	//: the negative control: the base configuration is accepted.
	if _, err := svcmail.NewSMTP(base); err != nil {
		t.Fatalf("NewSMTP(valid) = %v, want nil", err)
	}
}

// TestSMTPRefusesAnInjectedMessageWithoutDialling pins that the guard runs
// before the socket. A message that will be refused must cost no connection —
// otherwise a caller taking untrusted input has a way to make the SDK dial.
func TestSMTPRefusesAnInjectedMessageWithoutDialling(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optDefault)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	msg := simpleMessage()
	msg.Subject = "hi\r\nBcc: attacker@evil.example"
	if err := transport.Send(context.Background(), msg); !errs.HasCode(err, coremail.CodeHeaderInjection) {
		t.Fatalf("Send = %v, want CodeHeaderInjection", err)
	}
	if lines := server.allLines(); len(lines) != 0 {
		t.Fatalf("the transport dialled for a message it was going to refuse: %v", lines)
	}
}

// TestSMTPHonoursACancelledContext pins that a context reaches a client that
// has none. net/smtp takes no context at all, so the transport enforces it by
// severing the socket.
func TestSMTPHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	_, port := newFakeSMTP(t, optDefault)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.Send(ctx, simpleMessage()); err == nil {
		t.Fatal("Send succeeded on an already-cancelled context")
	}
}

// TestSMTPBatchUsesOneSession pins the capability's reason for existing: three
// messages, one greeting.
func TestSMTPBatchUsesOneSession(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optDefault)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	batch, ok := transport.(coremail.BatchSender)
	if !ok {
		t.Fatal("the SMTP transport does not implement BatchSender")
	}
	msgs := []coremail.MessageValue{simpleMessage(), simpleMessage(), simpleMessage()}
	if err := batch.SendBatch(context.Background(), msgs); err != nil {
		t.Fatalf("SendBatch = %v, want nil", err)
	}
	mails := 0
	for _, line := range server.allLines() {
		if strings.HasPrefix(strings.ToUpper(line), "MAIL FROM") {
			mails++
		}
	}
	if mails != len(msgs) {
		t.Fatalf("saw %d MAIL FROM commands, want %d", mails, len(msgs))
	}
}

// TestSMTPBatchAggregatesFailuresWithoutDialling pins that one unusable message
// neither stops the batch nor opens a connection.
func TestSMTPBatchAggregatesFailuresWithoutDialling(t *testing.T) {
	t.Parallel()
	server, port := newFakeSMTP(t, optDefault)
	transport, buildErr := svcmail.NewSMTP(smtpConfig(t, port, svcmail.TLSStartTLS))
	if buildErr != nil {
		t.Fatalf("NewSMTP = %v, want nil", buildErr)
	}
	batch, ok := transport.(coremail.BatchSender)
	if !ok {
		t.Fatal("the SMTP transport does not implement BatchSender")
	}
	bad := simpleMessage()
	bad.Subject = "x\r\nBcc: attacker@evil.example"
	err := batch.SendBatch(context.Background(), []coremail.MessageValue{bad, simpleMessage()})
	if !errs.HasCode(err, coremail.CodeHeaderInjection) {
		t.Fatalf("SendBatch = %v, want the joined error to carry CodeHeaderInjection", err)
	}
	mails := 0
	for _, line := range server.allLines() {
		if strings.HasPrefix(strings.ToUpper(line), "MAIL FROM") {
			mails++
		}
	}
	if mails != 1 {
		t.Fatalf("saw %d MAIL FROM commands, want 1 — the good message must still go", mails)
	}
}
