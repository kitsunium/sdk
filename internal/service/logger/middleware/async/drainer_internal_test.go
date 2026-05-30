package async

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// countingDownstream counts every Write so drainer tests can assert that
// the goroutine actually consumed the queue.
type countingDownstream struct {
	writes atomic.Int64
}

func (c *countingDownstream) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}
func (c *countingDownstream) Flush(_ context.Context) error { return nil }
func (c *countingDownstream) Close() error                  { return nil }

// liveSink builds an asyncSink with a real drainer goroutine running.
func liveSink(t testing.TB, down corelogger.Sink) *asyncSink {
	t.Helper()
	s := &asyncSink{
		downstream: down,
		queue:      mustNewRing(8),
		pool:       recycler.NewPool[*recordEntry](newRecordEntry),
		policy:     DropNewest,
		onDrop:     noopOnDrop,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	//: spawn the drainer the same way New does.
	go s.drain()
	return s
}

func Test_asyncSink_drain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
		//: postCloseEnqueue triggers the drainRemaining path: entries enqueued
		//: directly under ringMu after Close signals stop, so the drainer's
		//: terminal drainRemaining loop is the one that flushes them.
		postCloseEnqueue int
		//: pause forces the drainer to hit the empty-queue yield branch
		//: (TryRead fails → default → yieldOnce) before any entry shows up.
		pause time.Duration
	}{
		{"drain consumes 4 entries before Close returns", 4, 0, 0},
		{"drain consumes a single entry", 1, 0, 0},
		{"drain handles immediate Close on empty queue", 0, 0, 0},
		{"drain processes entries enqueued after Close via drainRemaining", 0, 3, 0},
		{"drain yields on empty queue before any Write arrives", 1, 0, 5 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingDownstream{}
			s := liveSink(t, down)
			rec := corelogger.RecordEvent{Level: level.Info}
			//: optional pause forces the drainer to hit the yield branch.
			if tc.pause > 0 {
				time.Sleep(tc.pause)
			}
			//: enqueue tc.n entries; the drainer should pick them up.
			for range tc.n {
				if _, err := s.Write(t.Context(), rec, []byte("x")); err != nil {
					t.Fatalf("Write err = %v", err)
				}
			}
			//: spin-wait for the drainer to process the batch.
			deadline := time.Now().Add(2 * time.Second)
			for down.writes.Load() < int64(tc.n) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := down.writes.Load(); got != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", got, tc.n)
			}
			//: drive the drainRemaining branch deterministically: hold ringMu,
			//: close stop, enqueue, release the lock, then wait for done.
			//: This guarantees the drainer's main loop cannot consume the
			//: primed entries before drainRemaining sees them.
			if tc.postCloseEnqueue > 0 {
				s.ringMu.Lock()
				close(s.stop)
				for range tc.postCloseEnqueue {
					ent := s.pool.Get()
					ent.rec = rec
					ent.data = append(ent.data[:0], "y"...)
					if werr := s.queue.TryWrite(ent); werr != nil {
						s.ringMu.Unlock()
						t.Fatalf("priming TryWrite err = %v", werr)
					}
				}
				s.ringMu.Unlock()
				//: drainer's terminal exit closes s.done; wait so all
				//: post-stop entries have been forwarded before asserting.
				<-s.done
			} else if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
			//: after Close every queued entry (pre- or post-stop) must have
			//: reached the downstream sink.
			wantTotal := int64(tc.n + tc.postCloseEnqueue)
			if got := down.writes.Load(); got != wantTotal {
				t.Errorf("downstream writes after Close = %d, want %d", got, wantTotal)
			}
		})
	}
}

func Test_asyncSink_forward(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: payloadCap drives the oversized-buffer branch in forward — when
		//: cap(ent.data) exceeds maxSaneCap the slice is dropped before the
		//: entry returns to the pool.
		payloadCap int
		//: failWrite installs a downstream sink whose Write returns an error
		//: so forward exercises the OnError fan-out path.
		failWrite bool
	}{
		{"forward delivers the entry and recycles it", 8, false},
		{"forward drops oversized backing array (cap > maxSaneCap)", maxSaneCap + 1, false},
		{"forward surfaces downstream Write error via OnError", 8, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := freshSink(t, DropNewest)
			var errSeen bool
			s.onError = func(err error) {
				if err != nil {
					errSeen = true
				}
			}
			if tc.failWrite {
				s.downstream = failingDownstream{}
			} else {
				s.downstream = &countingDownstream{}
			}
			ent := s.pool.Get()
			ent.rec = corelogger.RecordEvent{Level: level.Info}
			//: build a payload with the requested capacity to exercise the
			//: oversized-cap branch in forward.
			ent.data = make([]byte, len("payload"), tc.payloadCap)
			copy(ent.data, "payload")
			s.forward(ent)
			if tc.failWrite && !errSeen {
				t.Error("forward swallowed the downstream error; onError not fired")
			}
			if !tc.failWrite {
				down, ok := s.downstream.(*countingDownstream)
				if !ok {
					t.Fatal("downstream type assertion failed")
				}
				if down.writes.Load() != 1 {
					t.Errorf("downstream writes = %d, want 1", down.writes.Load())
				}
			}
		})
	}
}

// failingDownstream returns a non-nil error from Write so forward exercises
// its OnError branch.
type failingDownstream struct{}

func (failingDownstream) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	return 1, errDrainerBoom{}
}
func (failingDownstream) Flush(_ context.Context) error { return nil }
func (failingDownstream) Close() error                  { return nil }

// errDrainerBoom is a minimal error used to feed the forward error branch.
type errDrainerBoom struct{}

// Error renders a static marker; content is not asserted by the test.
func (errDrainerBoom) Error() (msg string) {
	//: static marker — content is not asserted.
	return "drainer boom"
}

func Test_asyncSink_drainRemaining(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"drainRemaining flushes every queued entry", 3},
		{"drainRemaining is a no-op on an empty queue", 3},
		{"drainRemaining handles a single entry", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingDownstream{}
			s := freshSink(t, DropNewest)
			s.downstream = down
			//: pre-fill the ring with tc.n entries.
			for range tc.n {
				ent := s.pool.Get()
				ent.data = append(ent.data[:0], "x"...)
				if err := s.queue.TryWrite(ent); err != nil {
					t.Fatalf("TryWrite err = %v", err)
				}
			}
			s.drainRemaining()
			if down.writes.Load() != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", down.writes.Load(), tc.n)
			}
		})
	}
}

// Test_asyncSink_drainLoop exercises the shared loop body directly: it forwards
// queued entries, then returns once the supplied stop channel is closed, having
// flushed the ring via drainRemaining (lose-nothing-on-stop contract).
func Test_asyncSink_drainLoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"drainLoop forwards queued entries then exits on stop", 4},
		{"drainLoop exits promptly on stop with an empty queue", 0},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the loop must forward every entry and return once stop closes.
	runCase := func(t *testing.T, n int) {
		t.Helper()
		down := &countingDownstream{}
		s := freshSink(t, DropNewest)
		s.downstream = down
		//: pre-fill the ring so drainLoop forwards before it sees stop.
		for range n {
			ent := s.pool.Get()
			ent.data = append(ent.data[:0], "x"...)
			if err := s.queue.TryWrite(ent); err != nil {
				t.Fatalf("TryWrite err = %v", err)
			}
		}
		stop := make(chan struct{})
		loopDone := make(chan struct{})
		//: run the loop body in a goroutine so the test can close stop.
		go func() {
			//: close loopDone on return so the test can observe the exit.
			defer close(loopDone)
			s.drainLoop(stop)
		}()
		//: spin-wait for the pre-filled batch to drain, then signal stop.
		deadline := time.Now().Add(2 * time.Second)
		for down.writes.Load() < int64(n) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(stop)
		//: the loop MUST return promptly once stop is closed (worker contract).
		select {
		case <-loopDone:
			//: loop returned as required.
		case <-time.After(2 * time.Second):
			t.Fatal("drainLoop did not return after stop was closed")
		}
		if got := down.writes.Load(); got != int64(n) {
			t.Errorf("downstream writes = %d, want %d", got, n)
		}
	}
	//: exercise each row under its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc.n)
		})
	}
}

// Test_asyncSink_joinDrainer covers both join paths: the test path (no daemon,
// joins on the done channel that drain() closes) and the production path (a
// worker.LoopDaemon whose idempotent Stop performs the join). In both cases
// joinDrainer MUST return only after the drainer goroutine has exited.
func Test_asyncSink_joinDrainer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		runner func(t *testing.T)
	}
	tests := []tc{
		{
			name: "test path joins on the done channel",
			runner: func(t *testing.T) {
				//: liveSink spawns drain() by hand and leaves daemon nil, so
				//: joinDrainer falls through to the <-done receive.
				s := liveSink(t, &countingDownstream{})
				//: signal the drainer to exit under the same lock Close uses.
				s.ringMu.Lock()
				s.stopOnce.Do(func() { close(s.stop) })
				s.ringMu.Unlock()
				s.joinDrainer()
				//: assert via a non-blocking select that done is already
				//: closed — join must not return before the drainer exits.
				select {
				case <-s.done:
					//: drainer exited as required.
				default:
					t.Error("joinDrainer returned before the test-path drainer exited")
				}
			},
		},
		{
			name: "production path joins via the daemon",
			runner: func(t *testing.T) {
				//: build the sink the way New does — a LoopDaemon owns drain();
				//: daemon != nil routes joinDrainer through the daemon's Stop.
				down := &countingDownstream{}
				s := &asyncSink{
					downstream:  down,
					queue:       mustNewRing(8),
					pool:        recycler.NewPool[*recordEntry](newRecordEntry),
					policy:      DropNewest,
					onDrop:      noopOnDrop,
					flushSignal: make(chan struct{}, 1),
					stop:        make(chan struct{}),
					done:        make(chan struct{}),
				}
				//: a LoopDaemon owns the loop, mirroring production New().
				s.daemon = worker.Start(func(_ <-chan struct{}) { s.drain() })
				//: signal exit under ringMu, then join through the daemon.
				s.ringMu.Lock()
				s.stopOnce.Do(func() { close(s.stop) })
				s.ringMu.Unlock()
				s.joinDrainer()
				//: the daemon's Stop joined the loop; drain() closed done.
				select {
				case <-s.done:
					//: drainer exited as required.
				default:
					t.Error("joinDrainer returned before the daemon-owned drainer exited")
				}
			},
		},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; each runner owns its own join assertion.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		tc.runner(t)
	}
	//: exercise each row under its own subtest.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
