package mailengine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	mailspool "github.com/kitsunium/sdk/pkg/v1/app/mail/spool"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A failed attempt waits one second, doubling, up to five minutes: the
// bounds kit's core hands, on the SDK's backoff.
func TestTheBackoffDoublesUpToItsBound(t *testing.T) {
	b := backoffOf(&plug.MailSpoolConfig{BackoffBase: time.Second, BackoffMax: 5 * time.Minute})
	var got []time.Duration
	for attempt := 1; attempt <= 12; attempt++ {
		got = append(got, b.Delay(attempt))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300, 300}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backoff %v, want %v", got, want)
	}
}

// Each refusal kit tells apart is the SDK's sentinel of its kind, wrapped or
// not, and no other: kit's core never matches a sentinel itself.
func TestEachRefusalIsItsSentinel(t *testing.T) {
	type tc struct {
		err  error
		kind plug.MailRefusal
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !refused(c.err, c.kind) {
			t.Errorf("%v is not refused as %d", c.err, c.kind)
		}
		if !refused(errs.Wrap(c.err, errs.WrapParams{}), c.kind) {
			t.Errorf("%v, wrapped, is not refused as %d", c.err, c.kind)
		}
		for _, other := range []plug.MailRefusal{plug.MailRefusalNone, plug.MailRefusalClosed, plug.MailRefusalUnencodable, plug.MailRefusalInvalidURL} {
			if other != c.kind && refused(c.err, other) {
				t.Errorf("%v is refused as %d too", c.err, other)
			}
		}
	}
	for _, c := range []tc{
		{mailspool.Closed, plug.MailRefusalClosed},
		{mailspool.MessageUnencodable, plug.MailRefusalUnencodable},
		{mail.InvalidURL, plug.MailRefusalInvalidURL},
	} {
		t.Run(c.err.Error(), func(t *testing.T) { runCase(t, c) })
	}
	if refused(errors.New("other"), plug.MailRefusalClosed) {
		t.Error("an error of no kind is refused")
	}
}

// The capture transport keeps the message it composed, and the SMTP
// transport is built from what ParseSMTP read and nothing else.
func TestTheTransports(t *testing.T) {
	c := capture()
	if got := c.(plug.MailCaptureSource).Composed(); got != nil {
		t.Fatalf("a capture that sent nothing composed %q", got)
	}
	msg := mail.Message{From: mail.Address{Addr: "a@example.com"}, To: []mail.Address{{Addr: "b@example.com"}}, Subject: "S", Text: "body"}
	if err := c.Send(context.Background(), plug.MailValue{Value: msg}); err != nil {
		t.Fatal(err)
	}
	if got := c.(plug.MailCaptureSource).Composed(); len(got) == 0 {
		t.Error("the capture kept no composed message")
	}
	relay, err := parseSMTP("smtps://user:pw@mail.example.com")
	if err != nil || relay.Server != "mail.example.com:465" || relay.TLS != "implicit" {
		t.Fatalf("parsed %+v, %v", relay, err)
	}
	if _, err := newSMTP(&relay); err != nil {
		t.Errorf("the relay builds no transport: %v", err)
	}
	if _, err := newSMTP(&plug.MailRelayValue{Server: relay.Server}); !errors.Is(err, mail.InvalidConfig) {
		t.Errorf("a relay ParseSMTP did not read builds a transport: %v", err)
	}
}
