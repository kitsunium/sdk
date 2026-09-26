// Package server — the accept loop's backoff: a failing Accept waits on the
// engine's clock, the wait grows and starts over, and a closed listener ends
// it at once.
package server

import (
	"context"
	"errors"
	stdnet "net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// errDescriptors stands for the process out of file descriptors.
var errDescriptors = errors.New("accept: too many open files")

// temporaryError is a net.Error that says it is temporary, as EMFILE does.
type temporaryError struct{}

func (temporaryError) Error() string   { return "accept: temporary failure" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

// scriptedListener answers Accept from a script — an error, or a connection —
// then blocks until closed, as a real listener does.
type scriptedListener struct {
	mu      sync.Mutex
	script  []acceptStep
	accepts atomic.Int32
	shut    sync.Once
	closed  chan struct{}
}

// acceptStep is one scripted Accept: a connection, or the error in its place.
type acceptStep struct {
	conn stdnet.Conn
	err  error
}

// newScriptedListener returns a listener that plays steps, then blocks.
func newScriptedListener(steps ...acceptStep) *scriptedListener {
	return &scriptedListener{script: steps, closed: make(chan struct{})}
}

// Accept plays the next step, or blocks until Close once the script is over.
func (l *scriptedListener) Accept() (stdnet.Conn, error) {
	l.accepts.Add(1)
	l.mu.Lock()
	//: a step left to play.
	if len(l.script) > 0 {
		step := l.script[0]
		l.script = l.script[1:]
		l.mu.Unlock()
		return step.conn, step.err
	}
	l.mu.Unlock()
	<-l.closed
	return nil, stdnet.ErrClosed
}

// Close ends a blocked Accept.
func (l *scriptedListener) Close() error {
	l.shut.Do(func() { close(l.closed) })
	return nil
}

// Addr is a fixed loopback address.
func (l *scriptedListener) Addr() stdnet.Addr {
	return &stdnet.TCPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 1}
}

// connection returns one end of an in-memory pipe, the other closed by cleanup.
func connection(t *testing.T) stdnet.Conn {
	t.Helper()
	near, far := stdnet.Pipe()
	t.Cleanup(func() {
		//: an in-memory pipe closes without failing; say so if it ever does.
		if err := far.Close(); err != nil {
			t.Errorf("close pipe: %v", err)
		}
	})
	return near
}

// waitArmed waits, within serveDeadline, for the loop to arm a backoff on the
// manual clock — and fails naming the defect if it never does, which is what a
// loop retrying at once looks like from here.
func waitArmed(t *testing.T, manual *clock.ManualClock) {
	t.Helper()
	deadline := time.Now().Add(serveDeadline)
	//: poll the clock's armed waits.
	for manual.Pending() < 1 {
		//: never armed: the loop did not back off.
		if time.Now().After(deadline) {
			t.Fatal("no backoff was armed on the engine's clock: the accept loop retried at once")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAFailingAcceptWaitsOnTheEnginesClock pins the backoff: after each failed
// Accept — temporary or not — the loop waits 5 ms, then 10, 20, 40, on the
// engine's clock and not a nanosecond less, calls Accept nowhere in between,
// and starts the curve over once a connection is accepted.
//
// Goroutine lifecycle: one goroutine runs the accept loop under test and closes
// a channel on exit; the test ends it by closing its listener and receives
// from that channel, so none outlives the test.
func TestAFailingAcceptWaitsOnTheEnginesClock(t *testing.T) {
	t.Parallel()
	listener := newScriptedListener(
		acceptStep{err: temporaryError{}},
		acceptStep{err: errDescriptors},
		acceptStep{err: temporaryError{}},
		acceptStep{err: errDescriptors},
		acceptStep{conn: connection(t)},
		acceptStep{err: errDescriptors},
	)
	srv := newTestServer(t)
	manual := clock.NewManualClock(time.Unix(0, 0))
	srv.clk = manual
	group := &StreamGroup{name: "api"}
	served := make(chan struct{}, 1)
	group.HandleFunc(func(context.Context, corenet.Conn) error {
		served <- struct{}{}
		return nil
	})
	bound := &boundListener{group: group.name, ln: listener}
	stopped := make(chan struct{})
	srv.inFlight.Add(1)
	go func() {
		defer close(stopped)
		srv.acceptLoop(bound, group, group.resolved())
	}()

	waits := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	//: each failure: a wait armed, exactly its length, and no Accept during it.
	for index, wait := range waits {
		waitArmed(t, manual)
		calls := listener.accepts.Load()
		//: one Accept per failure so far — none spent spinning.
		if calls != int32(index+1) {
			t.Fatalf("failure %d: Accept called %d times, want %d", index+1, calls, index+1)
		}
		manual.Advance(wait - time.Nanosecond)
		//: a nanosecond short: still waiting, still one wait armed.
		if manual.Pending() != 1 || listener.accepts.Load() != calls {
			t.Fatalf("failure %d: the wait ended before %v", index+1, wait)
		}
		manual.Advance(time.Nanosecond)
	}
	<-served
	//: the failure after a success waits the first delay again.
	waitArmed(t, manual)
	manual.Advance(5*time.Millisecond - time.Nanosecond)
	//: still waiting a nanosecond short of 5 ms.
	if manual.Pending() != 1 {
		t.Fatal("the backoff did not start over after an accepted connection")
	}
	manual.Advance(time.Nanosecond)
	//: every wait is counted in State.
	if got := srv.State().AcceptBackoffs; got != 5 {
		t.Errorf("State().AcceptBackoffs = %d, want 5", got)
	}
	//: the listener closing ends the loop.
	if err := bound.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(serveDeadline):
		t.Fatal("the accept loop outlived its listener")
	}
}

// TestShutdownDuringABackoffReturnsAtOnce pins the other end: a loop waiting
// out a backoff is not blocked in Accept, so closing the listener alone would
// not reach it until the wait ended. Shutdown returns while the engine's clock
// never moves at all — the wait is interrupted, not waited out.
//
// Goroutine lifecycle: one goroutine runs the accept loop, released by the
// Shutdown under test; a second runs Shutdown and publishes its result on a
// buffered channel the test receives from within serveDeadline.
func TestShutdownDuringABackoffReturnsAtOnce(t *testing.T) {
	t.Parallel()
	listener := newScriptedListener(acceptStep{err: errDescriptors})
	srv := newTestServer(t)
	manual := clock.NewManualClock(time.Unix(0, 0))
	srv.clk = manual
	group := &StreamGroup{name: "api"}
	group.HandleFunc(noopConn)
	bound := &boundListener{group: group.name, ln: listener}
	srv.mu.Lock()
	srv.listeners = append(srv.listeners, bound)
	srv.mu.Unlock()
	srv.phase.Store(uint32(corenet.PhaseServing))
	srv.inFlight.Add(1)
	go srv.acceptLoop(bound, group, group.resolved())
	waitArmed(t, manual)

	returned := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), serveDeadline)
		defer cancel()
		returned <- srv.Shutdown(ctx)
	}()
	select {
	case err := <-returned:
		//: a clean drain: the loop left, and nothing was in flight.
		if err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
	case <-time.After(serveDeadline):
		t.Fatal("Shutdown waited for a backoff the clock never ended")
	}
	//: the one wait, counted.
	if got := srv.State().AcceptBackoffs; got != 1 {
		t.Errorf("State().AcceptBackoffs = %d, want 1", got)
	}
}

// TestAcceptDelayIsNetHTTPsCurve pins the curve itself: 5 ms, doubling, held
// at one second however many failures follow.
func TestAcceptDelayIsNetHTTPsCurve(t *testing.T) {
	t.Parallel()
	want := map[int]time.Duration{
		1: 5 * time.Millisecond, 2: 10 * time.Millisecond, 3: 20 * time.Millisecond,
		8: 640 * time.Millisecond, 9: time.Second, 10: time.Second, 1000: time.Second,
	}
	//: failure count by failure count.
	for failures, delay := range want {
		//: the wait after that many consecutive failures.
		if got := acceptDelay(failures); got != delay {
			t.Errorf("acceptDelay(%d) = %v, want %v", failures, got, delay)
		}
	}
}
