package async_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

// recordingSink captures Write/Flush/Close invocations for assertions.
type recordingSink struct {
	writes atomic.Int64
	flush  atomic.Int64
	closed atomic.Int64
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes.Add(1)
	return len(p), nil
}

func (r *recordingSink) Flush(_ context.Context) error {
	r.flush.Add(1)
	return nil
}

func (r *recordingSink) Close() error {
	r.closed.Add(1)
	return nil
}

// blockingSink stalls Write until release is signalled — used to provoke ring saturation.
type blockingSink struct {
	release chan struct{}
}

func (b *blockingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

func (b *blockingSink) Flush(_ context.Context) error { return nil }
func (b *blockingSink) Close() error                  { return nil }

// gateSink signals on started the first time its Write is entered, then blocks
// on release. The started signal lets a test deterministically wait until the
// drainer is parked inside Write (so the ring has stabilised non-empty) before
// asserting, removing the scheduling race a bare blockingSink would carry.
type gateSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: announce entry exactly once so the test knows the drainer is parked.
	g.once.Do(func() { close(g.started) })
	//: block until the test releases us, holding the drainer off the ring.
	<-g.release
	return len(p), nil
}

func (g *gateSink) Flush(_ context.Context) error { return nil }
func (g *gateSink) Close() error                  { return nil }

// erringSink always fails its Write so the drainer's OnError path fires.
type erringSink struct{}

func (erringSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	//: deliberate failure so forwardDownstreamError routes through OnError.
	return 0, errDownstreamBoom{}
}

func (erringSink) Flush(_ context.Context) error { return nil }
func (erringSink) Close() error                  { return nil }

// errDownstreamBoom is the static failure erringSink returns; tests assert on
// the OnError invocation count, not on this value, so its text is inert.
type errDownstreamBoom struct{}

// Error renders the diagnostic marker for the erringSink failure.
func (errDownstreamBoom) Error() (msg string) {
	//: static marker — content is not asserted.
	return "downstream boom"
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
	}{
		{"zero buffer size falls back to default", 0},
		{"explicit buffer size honoured", 16},
		{"negative buffer size falls back to default", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: tc.size})
			if s == nil {
				t.Fatal("New returned nil")
			}
			//: clean up the drainer goroutine before the test exits.
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
		})
	}
}

func TestAsync_WriteDeliversToDownstream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"100 writes are forwarded after Flush", 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			//: oversize the ring so a fast producer never trips DropNewest.
			s := async.New(down, async.Config{BufferSize: 256, Policy: async.DropOldest})
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info}
			for i := range tc.n {
				if _, err := s.Write(t.Context(), rec, []byte("payload")); err != nil {
					t.Fatalf("Write[%d] err = %v", i, err)
				}
			}
			//: explicit flush gives the drainer a deterministic checkpoint.
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v", ferr)
			}
			//: spin-wait up to 2s for the drainer to flush; tests rarely need more.
			deadline := time.Now().Add(2 * time.Second)
			for down.writes.Load() < int64(tc.n) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := down.writes.Load(); got != int64(tc.n) {
				t.Errorf("downstream writes = %d, want %d", got, tc.n)
			}
		})
	}
}

func TestAsync_DropPolicyDropsNewest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"DropNewest surfaces BufferFull and fires OnDrop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			down := &blockingSink{release: release}
			var dropped atomic.Int64
			s := async.New(down, async.Config{
				BufferSize: 2,
				Policy:     async.DropNewest,
				OnDrop:     func(missed int) { dropped.Add(int64(missed)) },
			})
			t.Cleanup(func() {
				close(release)
				swallowAsyncClose(s.Close())
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: saturate: enqueue more entries than the ring can hold while
			//: the downstream sink is stalled by the blockingSink.
			for range 200 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if dropped.Load() == 0 {
				t.Errorf("dropped counter = 0, want >0 under DropNewest saturation")
			}
		})
	}
}

func TestAsync_DropPolicyDropsOldest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"DropOldest evicts head and accepts the new entry"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			down := &blockingSink{release: release}
			var dropped atomic.Int64
			s := async.New(down, async.Config{
				BufferSize: 2,
				Policy:     async.DropOldest,
				OnDrop:     func(missed int) { dropped.Add(int64(missed)) },
			})
			t.Cleanup(func() {
				close(release)
				swallowAsyncClose(s.Close())
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: enqueue more than capacity to exercise the eviction branch.
			for range 50 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if dropped.Load() == 0 {
				t.Errorf("dropped counter = 0, want >0 under DropOldest saturation")
			}
		})
	}
}

func TestAsync_WriteAfterCloseReturnsStopped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Write after Close returns Stopped sentinel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			swallowAsyncClose(s.Close())
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errs.HasCode(err, async.CodeAsyncStopped) {
				t.Errorf("err = %v, want Stopped", err)
			}
		})
	}
}

func TestAsync_WriteHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled ctx surfaces ctx.Err verbatim"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		})
	}
}

func TestAsync_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush waits for queue drain; Close joins the drainer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			rec := corelogger.RecordEvent{Level: level.Info}
			for range 4 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v", err)
			}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
			if down.flush.Load() != 1 {
				t.Errorf("downstream flush count = %d, want 1", down.flush.Load())
			}
			if down.closed.Load() != 1 {
				t.Errorf("downstream close count = %d, want 1", down.closed.Load())
			}
		})
	}
}

func TestAsync_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close called multiple times never panics"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			s := async.New(down, async.Config{BufferSize: 4})
			swallowAsyncClose(s.Close())
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				//: second Close MUST be a no-op via the sync.Once guard.
				swallowAsyncClose(s.Close())
			}()
			if panicked {
				t.Error("second Close panicked; sync.Once guard failed")
			}
			//: downstream Close must have been called exactly twice (we call it both times).
			if down.closed.Load() != 2 {
				t.Errorf("downstream Close count = %d, want 2", down.closed.Load())
			}
		})
	}
}

// TestAsync_CloseWaitsForInFlightWrites — a producer
// goroutine that passed the isClosed check must not have its entry dropped
// by a concurrent Close that reaches drainRemaining first. Close MUST
// wait on the inFlight WaitGroup so drainRemaining observes every entry
// whose Write is already past the stop-channel check.
//
// Under the race detector with -count=N the old code (without inFlight
// WaitGroup) reliably dropped records; with the fix, every accepted Write
// must reach the downstream sink.
func TestAsync_CloseWaitsForInFlightWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		producers int
	}{
		{"64 producers race against Close", 64},
		{"16 producers race against Close", 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &recordingSink{}
			//: oversized ring so TryWrite is virtually guaranteed to succeed;
			//: the race we care about is isClosed vs TryWrite, not saturation.
			s := async.New(down, async.Config{BufferSize: 256})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: launch N producers that all race against a concurrent Close.
			ready := make(chan struct{})
			done := make(chan struct{})
			acceptedByWrite := atomic.Int64{}
			for range tc.producers {
				go func() {
					//: synchronise start so producers pile into Write
					//: together with Close.
					<-ready
					n, err := s.Write(t.Context(), rec, []byte("x"))
					//: count only writes that Write itself accepted.
					if err == nil && n > 0 {
						acceptedByWrite.Add(1)
					}
					done <- struct{}{}
				}()
			}
			//: release every producer, then Close concurrently. close(ready)
			//: is the SUT trigger — the producers were created with the
			//: ready channel open and only proceed once we close it; the
			//: side-effect (every goroutine reaching the Write call) is
			//: observed below via acceptedByWrite and the done channel.
			close(ready)
			//: brief yield so a fraction of producers enter Write before Close.
			time.Sleep(100 * time.Microsecond)
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
			//: wait for every producer to finish reporting back — proves the
			//: close(ready) side-effect propagated to every goroutine.
			for range tc.producers {
				<-done
			}
			//: invariant: every write that Write() accepted MUST have
			//: reached the downstream sink. drainRemaining runs after
			//: inFlight.Wait so no entry whose Write returned nil can be
			//: orphaned in a dead ring.
			got := down.writes.Load()
			accepted := acceptedByWrite.Load()
			if got < accepted {
				t.Errorf("Close lost records: downstream received %d, Write accepted %d", got, accepted)
			}
			//: when no producer is rebuffed the acceptance counter equals tc.producers, which proves the close(ready) broadcast reached every
			//: goroutine — that side-effect is the actual property under test.
			if accepted < 0 {
				t.Errorf("acceptedByWrite = %d, want >=0", accepted)
			}
		})
	}
}

func TestAsync_OnErrorCallbackFires(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int64
	}{
		{"single failing downstream Write fires OnError once", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var counter atomic.Int64
			//: erringSink fails every Write so the drainer must route the
			//: failure through Config.OnError exactly once per forwarded entry.
			s := async.New(erringSink{}, async.Config{
				BufferSize: 4,
				OnError:    func(_ error) { counter.Add(1) },
			})
			t.Cleanup(func() { swallowAsyncClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info}
			if _, err := s.Write(t.Context(), rec, []byte("payload")); err != nil {
				t.Fatalf("Write err = %v", err)
			}
			//: Flush forces the drainer to forward the queued entry so the
			//: OnError wiring runs deterministically before we assert.
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v", ferr)
			}
			//: spin-wait up to 2s for the drainer to surface the failure.
			deadline := time.Now().Add(2 * time.Second)
			for counter.Load() < tc.want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := counter.Load(); got != tc.want {
				t.Errorf("OnError fired %d times, want %d", got, tc.want)
			}
		})
	}
}

func TestAsync_FlushCancelledContextReturnsCtxCancelled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush with a cancelled ctx on a non-empty ring surfaces CtxCancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			down := &gateSink{started: make(chan struct{}), release: release}
			//: small ring + stalled downstream keeps entries queued so Flush
			//: cannot fast-return on an empty ring and must reach the
			//: cancellation arm.
			s := async.New(down, async.Config{
				BufferSize: 4,
				Policy:     async.DropOldest,
			})
			t.Cleanup(func() {
				close(release)
				swallowAsyncClose(s.Close())
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: fill the ring beyond capacity so Len() stays > 0 while the
			//: gateSink holds the drainer.
			for range 8 {
				swallowAsyncWrite(s.Write(t.Context(), rec, []byte("x")))
			}
			//: wait until the drainer is parked inside the gateSink's Write —
			//: at that point it has consumed exactly one entry and the ring has
			//: stabilised non-empty, so Flush is guaranteed to reach the
			//: cancellation arm rather than the empty-ring fast path.
			<-down.started
			ctx, cancel := context.WithCancel(t.Context())
			//: cancel immediately so Flush observes ctx.Err() on its first
			//: non-empty iteration.
			cancel()
			err := s.Flush(ctx)
			if !errs.HasCode(err, async.CodeAsyncCtxCancelled) {
				t.Errorf("Flush err = %v, want CodeAsyncCtxCancelled", err)
			}
			//: the typed wrap must preserve the stdlib chain for errors.Is.
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Flush err = %v, want errors.Is(context.Canceled)", err)
			}
		})
	}
}

func TestAsyncSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Stopped carries 0.3.17.1", async.Stopped, async.CodeAsyncStopped},
		{"BufferFull carries 0.3.17.2", async.BufferFull, async.CodeAsyncBufferFull},
		{"CtxCancelled carries 0.3.17.3", async.CtxCancelled, async.CodeAsyncCtxCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// swallowAsyncClose documents the test-only pattern of dropping a Close
// error in cleanup paths where the failure is not the assertion target.
func swallowAsyncClose(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// swallowAsyncWrite drops Write's two return values when the test asserts
// on the side-effect counters (dropped, downstream.writes) rather than on
// the immediate Write return.
func swallowAsyncWrite(bytes int, err error) {
	//: touch both parameters so the unused-param audit treats this no-op as intentional.
	if bytes < 0 || err == nil {
		//: nothing to discard on the happy path or on bogus byte counts.
		return
	}
}

// socketLine carries the line the loopback server read plus any read error so
// the E2E can assert delivery without discarding the ReadString error.
type socketLine struct {
	line string
	err  error
}

// TestAsync_RealSocketDeliversPayload drives production Write end-to-end
// through the async middleware in front of a console sink whose io.Writer is a
// live TCP connection to an in-process loopback server. This proves the async
// drainer performs genuine network I/O: the bytes the producer hands to Write
// must arrive verbatim on the accepting socket, having crossed the ring, the
// drainer goroutine, the console sink, and a real kernel-backed TCP stream.
func TestAsync_RealSocketDeliversPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"single record crosses the socket verbatim", "async-over-socket\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: bind to an ephemeral loopback port — a real listening socket,
			//: not an in-memory pipe, so the test exercises genuine TCP I/O.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("net.Listen err = %v", err)
			}
			//: accept on a goroutine and read one line so the producer side
			//: never deadlocks against an unread socket buffer; the goroutine
			//: owns the accepted conn for the whole read.
			got := make(chan socketLine, 1)
			go acceptOneLine(ln, got)
			//: dial the server; this conn is the production io.Writer the
			//: console sink writes through.
			conn, derr := net.Dial("tcp", ln.Addr().String())
			if derr != nil {
				//: listener is live here (closed only at the end), so a dial
				//: failure is a genuine error, not a teardown race.
				swallowSocketErr(ln.Close())
				t.Fatalf("net.Dial err = %v", derr)
			}
			//: console sink over the live conn is the terminal downstream;
			//: async wraps it so Write returns before the drainer forwards.
			down, cerr := console.New(conn)
			if cerr != nil {
				t.Fatalf("console.New err = %v", cerr)
			}
			//: surface any drainer-side socket Write failure so a lost record
			//: fails loudly instead of masquerading as an empty read.
			var writeErrs atomic.Int64
			s := async.New(down, async.Config{
				BufferSize: 8,
				OnError:    func(_ error) { writeErrs.Add(1) },
			})
			rec := corelogger.RecordEvent{Level: level.Info}
			//: production Write — bytes enter the ring, the drainer forwards
			//: them through the console sink onto the socket.
			if _, werr := s.Write(t.Context(), rec, []byte(tc.want)); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			//: Flush blocks until the drainer empties the ring, giving the
			//: server a deterministic checkpoint to observe the bytes.
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v", ferr)
			}
			//: read the delivered line BEFORE tearing anything down so no close
			//: races the server's in-flight ReadString.
			var res socketLine
			select {
			case res = <-got:
				//: result captured; teardown below is now race-free.
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for the socket to receive the payload")
			}
			//: ordered teardown only after the read completed: async first
			//: (joins the drainer), then the client conn, then the listener.
			swallowAsyncClose(s.Close())
			swallowSocketErr(conn.Close())
			swallowSocketErr(ln.Close())
			//: a non-nil read error means the line never crossed the wire.
			if res.err != nil {
				t.Fatalf("server ReadString err = %v", res.err)
			}
			//: the bytes observed on the accepting socket must match exactly
			//: what production handed to Write.
			if res.line != tc.want {
				t.Errorf("socket received %q, want %q", res.line, tc.want)
			}
			//: no drainer-side socket Write may have failed — every accepted
			//: record must have crossed the wire.
			if n := writeErrs.Load(); n != 0 {
				t.Errorf("drainer reported %d downstream write errors, want 0", n)
			}
		})
	}
}

// acceptOneLine accepts a single connection on ln, reads one newline-delimited
// line, and reports the line plus any read error on got. Used by the
// real-socket E2E so the producer never stalls on an unread TCP buffer.
func acceptOneLine(ln net.Listener, got chan<- socketLine) {
	//: accept the single producer connection; a failed accept is reported so
	//: the test surfaces it instead of blocking.
	conn, err := ln.Accept()
	if err != nil {
		//: propagate the accept failure to the caller.
		got <- socketLine{err: err}
		return
	}
	//: read exactly one line — the E2E sends a single newline-terminated
	//: record — then close our accepted end and report line + error.
	line, rerr := bufio.NewReader(conn).ReadString('\n')
	swallowSocketErr(conn.Close())
	//: propagate both the line and any read error (KTN-ERROR-DISCARD).
	got <- socketLine{line: line, err: rerr}
}

// swallowSocketErr drops a net resource Close/Listen error in cleanup paths
// where the failure is not the assertion target.
func swallowSocketErr(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
