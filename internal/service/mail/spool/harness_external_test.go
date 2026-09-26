// Package spool_test — the transports, clocks and spools the suite stands up.
package spool_test

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
	"github.com/kitsunium/sdk/internal/service/mail/spool"
)

// patience bounds every wait for the spool's own goroutine, so a defect fails
// the case instead of hanging the binary.
const patience time.Duration = 10 * time.Second

// origin is the instant every manual clock in the suite starts from.
var origin = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// recorder collects a spool's events on a channel.
type recorder struct {
	events chan spool.EventValue
}

// newRecorder returns a recorder with room for every event a case makes.
func newRecorder() *recorder {
	return &recorder{events: make(chan spool.EventValue, 64)}
}

// observe is the spool's Observe.
func (r *recorder) observe(event spool.EventValue) {
	r.events <- event
}

// next returns the next event, failing the case when none comes.
func (r *recorder) next(t *testing.T) spool.EventValue {
	t.Helper()
	select {
	case event := <-r.events:
		return event
	case <-time.After(patience):
		t.Fatal("no event came")
		return spool.EventValue{}
	}
}

// expect returns the next event and fails the case unless it is of kind.
func (r *recorder) expect(t *testing.T, kind spool.EventKind) spool.EventValue {
	t.Helper()
	event := r.next(t)
	if event.Kind != kind {
		t.Fatalf("event %v (err %v), want %v", event.Kind, event.Err, kind)
	}
	return event
}

// scripted is a transport whose attempts fail, succeed or block as the test
// says, recording each message and the attempt its context carried.
type scripted struct {
	// fail makes an attempt fail with it; nil lets it through.
	fail func(attempt int) error
	// deliver is the transport a successful attempt hands the mail to.
	deliver coremail.Transport
	mu      sync.Mutex
	// messages and attempts are what each attempt received.
	messages []coremail.MessageValue
	attempts []spool.AttemptValue
}

// Send records the attempt and does what the script says.
func (s *scripted) Send(ctx context.Context, msg coremail.MessageValue) error {
	attempt, _ := spool.AttemptFrom(ctx)
	s.mu.Lock()
	s.messages = append(s.messages, msg)
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(attempt.Attempt); err != nil {
			return err
		}
	}
	return s.deliver.Send(ctx, msg)
}

// seen returns what the attempts received.
func (s *scripted) seen() ([]coremail.MessageValue, []spool.AttemptValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages), slices.Clone(s.attempts)
}

// relayDown is the failure of a relay nobody listens on.
func relayDown(int) error {
	return errs.Wrap(svcmail.DialFailed, errs.WrapParams{}, errs.String("host", "relay.test"))
}

// newSpool builds a spool over cfg's transport with a manual clock and a
// recorder, and runs it until the case ends.
func newSpool(t *testing.T, cfg spool.Config) (*spool.Spool, *clock.ManualClock, *recorder) {
	t.Helper()
	clk := clock.NewManualClock(origin)
	rec := newRecorder()
	cfg.Clock, cfg.Observe = clk, rec.observe
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	s, err := spool.New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	run(t, s)
	return s, clk, rec
}

// run runs s's consumer until the case ends, and closes s.
//
// Goroutine lifecycle: one consumer per spool, ended by cancelling its
// context in the cleanup, which waits for Run to return before closing.
func run(t *testing.T, s *spool.Spool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
}

// message is a valid mail to one recipient.
func message(subject string) coremail.MessageValue {
	return coremail.MessageValue{
		To:      []coremail.AddressValue{{Name: "Guest", Addr: "guest@example.org"}},
		Subject: subject,
		Text:    "Hello",
	}
}

// requireDiskSpool builds a spool over a directory. Windows refuses the
// durable queue by design (ADR 0056): the refusal is asserted, then the case
// is skipped.
func requireDiskSpool(t *testing.T, cfg spool.Config) *spool.Spool {
	t.Helper()
	s, err := spool.New(cfg)
	if runtime.GOOS == "windows" {
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) || s != nil {
			t.Fatalf("New over a directory on windows = (%v, %v), want (nil, UNSUPPORTED_PLATFORM)", s, err)
		}
		t.Skip("the durable spool publishes through internal/service/vfs, which refuses windows by design (ADR 0018, ADR 0056); that refusal is asserted above")
	}
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return s
}
