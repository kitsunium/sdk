package mailengine

import (
	"context"
	"errors"
	"net/textproto"
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

// A dead letter's failure says why the spool gave it up through the code the
// spool adds to its trail, wrapped again or not; any other failure is its
// last attempt's. The SMTP reply is read through the chain, nil without one.
func TestADeadLetterSaysWhyItWasGivenUp(t *testing.T) {
	reply := &textproto.Error{Code: 550, Msg: "5.1.1 no such user"}
	refused := errs.Wrap(reply, errs.WrapParams{Code: 0x00_03_3D_09, Reason: "SEND_REFUSED", Public: "The mail server refused the message"})
	rejected := errs.Wrap(refused, errs.WrapParams{Code: mailspool.CodeMailRejected})
	for _, c := range []struct {
		err  error
		want plug.MailGiveUp
	}{
		{rejected, plug.MailGivenUpRejected},
		{errs.Wrap(rejected, errs.WrapParams{}), plug.MailGivenUpRejected},
		{errs.Wrap(refused, errs.WrapParams{Code: mailspool.CodeMailExpired}), plug.MailGivenUpExpired},
		{refused, plug.MailGivenUpExhausted},
		{nil, plug.MailGivenUpExhausted},
	} {
		if got := givenUpOf(c.err); got != c.want {
			t.Errorf("givenUpOf(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	if got := replyOf(rejected); got != reply {
		t.Errorf("replyOf = %v, want the reply in the chain", got)
	}
	if got := replyOf(errors.New("no reply")); got != nil {
		t.Errorf("replyOf of a failure without a reply = %v", got)
	}
}

// A dead letter of an earlier run is given up as the spool gave it up, read
// from what its outbox kept (issue #500): rejected or expired when the
// spool's code is in its trail — the transport's failure its origin — or is
// its code — a failure that was no SDK error, marked —, else exhausted. A
// rejection wins over an expiry, as givenUpOf has it.
func TestADeadLetterIsGivenUpAsItsOutboxKeptIt(t *testing.T) {
	const refused uint32 = 0x00_03_3D_09
	rejected, expired := uint32(mailspool.CodeMailRejected), uint32(mailspool.CodeMailExpired)
	for _, c := range []struct {
		dead mailspool.DeadLetter
		want plug.MailGiveUp
	}{
		{mailspool.DeadLetter{Code: refused, Trail: []uint32{rejected, 1}}, plug.MailGivenUpRejected},
		{mailspool.DeadLetter{Code: rejected}, plug.MailGivenUpRejected},
		{mailspool.DeadLetter{Code: refused, Trail: []uint32{expired}}, plug.MailGivenUpExpired},
		{mailspool.DeadLetter{Code: expired}, plug.MailGivenUpExpired},
		{mailspool.DeadLetter{Code: refused, Trail: []uint32{expired, rejected}}, plug.MailGivenUpRejected},
		{mailspool.DeadLetter{Code: refused}, plug.MailGivenUpExhausted},
		{mailspool.DeadLetter{}, plug.MailGivenUpExhausted},
	} {
		if got := deadGivenUp(&c.dead); got != c.want {
			t.Errorf("deadGivenUp(code %#x, trail %#x) = %d, want %d", c.dead.Code, c.dead.Trail, got, c.want)
		}
	}
}
