// Package lifecycle_test — the shared recorder every case in this suite
// asserts against.
//
// The whole suite is driven by clock.ManualClock and by channel rendezvous.
// Nothing here waits on the wall clock, and TestPackageNeverWaitsOnTheWallClock
// fails the build if anyone makes it.
package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// The suite's stand-ins for a CALLER's own errors. They are deliberately
// plain stdlib errors: a component belongs to the consumer, so what it
// returns is never an SDK error, and every claim about errors.Is surviving
// the aggregate has to be made against one of these.
var (
	errDial        = errors.New("dial tcp: connection refused")
	errDirtySchema = errors.New("migrations: dirty schema")
	errFlushShort  = errors.New("cache: flush wrote 0 of 900")
	errAddrInUse   = errors.New("listen: address already in use")
)

// origin is the fixed instant every ManualClock in this suite starts from.
// A literal date makes a failure message readable and keeps the suite
// independent of the machine's own clock.
var origin = time.Date(2031, time.March, 7, 4, 5, 0, 0, time.UTC)

// recorder collects the calls the engine made into components, in order.
// Every ordering claim in this package is asserted against it rather than
// against timing, which is what lets the suite be deterministic.
type recorder struct {
	mu    sync.Mutex
	calls []string
	// stopCtxLive records, per component, whether the context its Stop
	// received was still alive on entry. It is how "the unwind does not
	// inherit the cancellation that caused it" stops being a claim.
	stopCtxLive map[string]bool
}

// newRecorder returns an empty recorder.
func newRecorder() *recorder {
	return &recorder{stopCtxLive: map[string]bool{}}
}

// note appends one call.
func (r *recorder) note(entry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, entry)
}

// noteStopCtx records whether a Stop's context was live on entry.
func (r *recorder) noteStopCtx(ctx context.Context, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopCtxLive[name] = ctx.Err() == nil
}

// snapshot returns a copy of the calls recorded so far.
func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// ok returns a component that starts and stops cleanly, recording both.
func (r *recorder) ok(name string) corelc.ComponentValue {
	return corelc.ComponentValue{
		Name: name,
		Start: func(_ context.Context) error {
			r.note("start:" + name)
			return nil
		},
		Stop: func(ctx context.Context) error {
			r.noteStopCtx(ctx, name)
			r.note("stop:" + name)
			return nil
		},
	}
}

// signalling returns a clean component whose Start closes up once it is done.
// It is the suite's rendezvous with a lifecycle running on another goroutine:
// a channel close, never a duration.
func (r *recorder) signalling(name string, up chan<- struct{}) corelc.ComponentValue {
	component := r.ok(name)
	component.Start = func(_ context.Context) error {
		r.note("start:" + name)
		close(up)
		return nil
	}
	return component
}

// failing returns a component whose Start records and then fails.
func (r *recorder) failing(name string, cause error) corelc.ComponentValue {
	component := r.ok(name)
	component.Start = func(_ context.Context) error {
		r.note("start:" + name)
		return cause
	}
	return component
}

// blocking returns a component whose Stop parks until release is closed, and
// signals on entered as soon as it is called. The two channels are the
// rendezvous that replaces every sleep this suite would otherwise need.
func (r *recorder) blocking(name string, entered chan<- struct{}, release <-chan struct{}) (
	component corelc.ComponentValue, sawCancel *bool, done chan struct{},
) {
	observed := new(bool)
	finished := make(chan struct{})
	component = r.ok(name)
	component.Stop = func(ctx context.Context) error {
		r.noteStopCtx(ctx, name)
		close(entered)
		<-release
		//: the component reports what the announcement looked like from the
		//: inside: a cancelled context means the budget expired, and it is the
		//: ONLY thing that reached it — nothing it owns was closed for it.
		*observed = ctx.Err() != nil
		r.note("stop:" + name)
		close(finished)
		return nil
	}
	return component, observed, finished
}

// stopAsync runs Stop on its own goroutine and hands back the channel its
// result will arrive on. Every budget case needs it: the shutdown has to be in
// flight before the ManualClock can be moved past a budget.
//
// Goroutine lifecycle: one goroutine per call, ending when Stop returns. The
// channel is buffered, so the goroutine never parks on a receiver that has
// gone away — including when the test fails before receiving.
func stopAsync(lc corelc.Lifecycle) <-chan error {
	stopped := make(chan error, 1)
	go func() { stopped <- lc.Stop(context.Background()) }()
	return stopped
}

// runAsync runs Run on its own goroutine and hands back its result channel.
//
// Goroutine lifecycle: one goroutine per call, ending when Run returns —
// which is when ctx is cancelled or a subscribed signal arrives. Buffered for
// the same reason stopAsync's channel is.
func runAsync(ctx context.Context, lc corelc.Lifecycle, cfg svclc.RunConfig) <-chan error {
	returned := make(chan error, 1)
	go func() { returned <- svclc.Run(ctx, lc, cfg) }()
	return returned
}

// assertCalls fails unless the recorded call order is exactly want.
func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order = %v, want %v", got, want)
		}
	}
}

// assertHasCode fails unless err carries c anywhere in its chain — including
// through an errors.Join, which is how this package aggregates.
func assertHasCode(t *testing.T, err error, c kerrs.Code, what string) {
	t.Helper()
	if !kerrs.HasCode(err, c) {
		t.Fatalf("%s: error %v does not carry %s", what, err, c)
	}
}

// manual returns a fresh ManualClock pinned at origin.
func manual() *clock.ManualClock {
	return clock.NewManualClock(origin)
}
