package multi_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/multi"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

// recordingSink captures every Write/Flush/Close call for assertions.
type recordingSink struct {
	writes int
	flush  int
	closed int
	werr   error
	ferr   error
	cerr   error
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes++
	return len(p), r.werr
}

func (r *recordingSink) Flush(_ context.Context) error {
	r.flush++
	return r.ferr
}

func (r *recordingSink) Close() error {
	r.closed++
	return r.cerr
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		branches []corelogger.Sink
	}{
		{"no branches yields a no-op sink", nil},
		{"nil entries are silently dropped", []corelogger.Sink{nil, &recordingSink{}, nil}},
		{"two real branches", []corelogger.Sink{&recordingSink{}, &recordingSink{}}},
		{"all-nil branches yields a no-op sink", []corelogger.Sink{nil, nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := multi.New(tc.branches...)
			if s == nil {
				t.Error("New returned nil")
			}
		})
	}
}

func TestNew_AllNilBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"all-nil branches collapse to a no-op sink that accepts every Write"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: every supplied branch is nil, so New must drop them all and
			//: hand back a sink that swallows Write without error.
			s := multi.New(nil, nil)
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: a no-op fanout reports zero bytes accepted — no branch took the payload.
			if n != 0 {
				t.Errorf("no-op Write n = %d, want 0", n)
			}
			if err != nil {
				t.Errorf("no-op Write err = %v, want nil", err)
			}
		})
	}
}

func TestFanout_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		branches []*recordingSink
		wantErr  bool
	}{
		{"all branches succeed", []*recordingSink{{}, {}}, false},
		{
			"one branch fails — fanout returns FANOUT_WRITE_FAILED",
			[]*recordingSink{{}, {werr: errors.New("boom")}},
			true,
		},
		{"empty fanout is a no-op", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: convert recordingSinks to the corelogger.Sink interface for wiring.
			branches := make([]corelogger.Sink, len(tc.branches))
			for i, b := range tc.branches {
				branches[i] = b
			}
			s := multi.New(branches...)
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.wantErr {
				if !errs.HasCode(err, multi.CodeFanoutWriteFailed) {
					t.Errorf("err = %v, want FanoutWriteFailed", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			//: every non-nil branch must have observed the Write call.
			for i, b := range tc.branches {
				if b.writes != 1 {
					t.Errorf("branch %d writes = %d, want 1", i, b.writes)
				}
			}
		})
	}
}

func TestFanout_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close hit every branch and aggregate errors"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{}
			b := &recordingSink{ferr: errors.New("flush boom")}
			s := multi.New(a, b)
			//: Flush surfaces the joined errors via errors.Is.
			if ferr := s.Flush(t.Context()); ferr == nil {
				t.Error("Flush err = nil, want joined error")
			}
			if a.flush != 1 || b.flush != 1 {
				t.Errorf("flush counters: a=%d b=%d, want both 1", a.flush, b.flush)
			}
			//: Close also visits every branch even if one fails.
			b.cerr = errors.New("close boom")
			if cerr := s.Close(); cerr == nil {
				t.Error("Close err = nil, want joined error")
			}
			if a.closed != 1 || b.closed != 1 {
				t.Errorf("close counters: a=%d b=%d, want both 1", a.closed, b.closed)
			}
		})
	}
}

func TestFanoutWriteFailedSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"FanoutWriteFailed carries 0.3.16.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(multi.FanoutWriteFailed, multi.CodeFanoutWriteFailed) {
				t.Errorf("HasCode failed for FanoutWriteFailed")
			}
		})
	}
}

func TestFanout_Write_AllBranchesFail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"both branches fail — fanout still visits each and aggregates"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{werr: errors.New("a-failed")}
			b := &recordingSink{werr: errors.New("b-failed")}
			s := multi.New(a, b)
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: every branch failed, so the wrapped FanoutWriteFailed sentinel surfaces.
			if !errs.HasCode(err, multi.CodeFanoutWriteFailed) {
				t.Errorf("err = %v, want FanoutWriteFailed", err)
			}
			//: no branch accepted the payload, so the reported byte count stays zero.
			if n != 0 {
				t.Errorf("n = %d, want 0", n)
			}
			//: failure must NOT short-circuit — both branches were invoked exactly once.
			if a.writes != 1 || b.writes != 1 {
				t.Errorf("writes a=%d b=%d, want 1/1", a.writes, b.writes)
			}
		})
	}
}

func TestFanout_Write_UnwrapChain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"errors.Join causes survive errs.Wrap via the *errs.Error Unwrap path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: distinct sentinels per branch so errors.Is can target each one
			//: individually after they cross errors.Join + errs.Wrap.
			errA := errors.New("err-a")
			errB := errors.New("err-b")
			a := &recordingSink{werr: errA}
			b := &recordingSink{werr: errB}
			s := multi.New(a, b)
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: the joined causes must remain reachable — proves Unwrap walks through
			//: the wrapping *errs.Error into the errors.Join multi-cause node.
			if !errors.Is(err, errA) {
				t.Errorf("errors.Is(err, errA) = false, want true")
			}
			if !errors.Is(err, errB) {
				t.Errorf("errors.Is(err, errB) = false, want true")
			}
		})
	}
}

func TestFanout_Write_Fields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"wrapped error carries failed-count and level structured fields"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: 3 branches with 2 failing drives the "failed"=2 field; one clean
			//: branch proves the count reflects failures only, not total branches.
			ok := &recordingSink{}
			a := &recordingSink{werr: errors.New("a-failed")}
			b := &recordingSink{werr: errors.New("b-failed")}
			s := multi.New(ok, a, b)
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Error}, []byte("x"))
			fields := errs.FieldsOf(err)
			//: the failure count is recorded as a structured field for observability.
			if !hasField(fields, "failed", "2") {
				t.Errorf("fields = %v, want failed=2", fields)
			}
			//: the record's level is stamped so operators can correlate by severity.
			wantLevel := strconv.Itoa(int(level.Error))
			if !hasField(fields, "level", wantLevel) {
				t.Errorf("fields = %v, want level=%s", fields, wantLevel)
			}
		})
	}
}

func TestFanout_Write_ByteCountLastSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"n reports the last successful branch's byte count, not a sum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a accepts the full 3-byte payload (returns len(p)=3); b fails. The
			//: reported n must equal a's accepted count, proving "last success wins".
			a := &recordingSink{}
			b := &recordingSink{werr: errors.New("b-failed")}
			s := multi.New(a, b)
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("abc"))
			//: one branch failed, so the aggregate error must still surface.
			if !errs.HasCode(err, multi.CodeFanoutWriteFailed) {
				t.Errorf("err = %v, want FanoutWriteFailed", err)
			}
			//: n is the accepting branch's count (3), never a cross-branch sum.
			if n != 3 {
				t.Errorf("n = %d, want 3", n)
			}
		})
	}
}

func TestFanout_Flush_AllBranchesFail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"both branches' Flush fail — each is visited and causes survive Join"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errA := errors.New("flush-a")
			a := &recordingSink{ferr: errA}
			b := &recordingSink{ferr: errors.New("flush-b")}
			s := multi.New(a, b)
			err := s.Flush(t.Context())
			//: an all-fail flush must report a non-nil aggregated error.
			if err == nil {
				t.Error("Flush err = nil, want joined error")
			}
			//: no short-circuit — both branches flushed exactly once.
			if a.flush != 1 || b.flush != 1 {
				t.Errorf("flush a=%d b=%d, want 1/1", a.flush, b.flush)
			}
			//: the joined chain keeps each branch's cause reachable via errors.Is.
			if !errors.Is(err, errA) {
				t.Errorf("errors.Is(err, errA) = false, want true")
			}
		})
	}
}

func TestFanout_Close_AllBranchesFail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"both branches' Close fail — each is visited and an error surfaces"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{cerr: errors.New("close-a")}
			b := &recordingSink{cerr: errors.New("close-b")}
			s := multi.New(a, b)
			err := s.Close()
			//: an all-fail close must report a non-nil aggregated error.
			if err == nil {
				t.Error("Close err = nil, want joined error")
			}
			//: no short-circuit — both branches closed exactly once.
			if a.closed != 1 || b.closed != 1 {
				t.Errorf("close a=%d b=%d, want 1/1", a.closed, b.closed)
			}
		})
	}
}

// TestFanout_Write_Concurrent spawns many concurrent producer goroutines onto a
// single fanout sink and asserts each branch observes exactly one Write per
// producer. The goroutines are bounded by a WaitGroup so none outlives the test,
// and the -race detector guards the stateless fan-out contract under contention.
func TestFanout_Write_Concurrent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
	}{
		{"20 concurrent producers each reach every branch exactly once", 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: atomic-counter branches so the race detector can prove the fanout
			//: never races on shared state across concurrent producers.
			a := &concurrentRecordingSink{}
			b := &concurrentRecordingSink{}
			s := multi.New(a, b)
			//: errCount lets goroutines flag failures without sharing t directly.
			var errCount atomic.Int64
			var wg sync.WaitGroup
			wg.Add(tc.goroutines)
			for range tc.goroutines {
				go func() {
					defer wg.Done()
					//: each producer drives a full Write through the shared fanout;
					//: the WaitGroup keeps every goroutine inside the test scope so
					//: t.Context() stays live for the duration of the fan-in.
					if _, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); werr != nil {
						errCount.Add(1)
					}
				}()
			}
			wg.Wait()
			//: no producer may have observed a write failure under concurrency.
			if got := errCount.Load(); got != 0 {
				t.Errorf("concurrent Write errors = %d, want 0", got)
			}
			//: every producer must have reached both branches — no dropped or
			//: doubled writes under concurrency.
			if got := a.writes.Load(); got != int64(tc.goroutines) {
				t.Errorf("branch a writes = %d, want %d", got, tc.goroutines)
			}
			if got := b.writes.Load(); got != int64(tc.goroutines) {
				t.Errorf("branch b writes = %d, want %d", got, tc.goroutines)
			}
		})
	}
}

// hasField reports whether fields contains an entry whose Key equals key and
// whose StringValue equals want. Used by the structured-field assertions so a
// test states intent ("failed=2 present") without depending on field ordering.
func hasField(fields []errs.FieldValue, key, want string) bool {
	//: linear scan — the field set is tiny and order is not part of the contract.
	for _, f := range fields {
		//: match on both key and rendered value so distinct keys never alias.
		if f.Key() == key && f.StringValue() == want {
			return true
		}
	}
	return false
}

// concurrentRecordingSink is a race-safe Sink mock counting Write calls via an
// atomic so TestFanout_Write_Concurrent can assert exact visit counts under the
// race detector without mutating the sequential recordingSink contract.
type concurrentRecordingSink struct {
	// writes counts every Write invocation; atomic for concurrent producers.
	writes atomic.Int64
}

func (c *concurrentRecordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}

func (c *concurrentRecordingSink) Flush(_ context.Context) error { return nil }
func (c *concurrentRecordingSink) Close() error                  { return nil }

// TestFanout_RealSocketBroadcastsToEveryBranch drives production Write end-to-end
// through the fanout middleware fronting two console sinks, each writing to a
// live TCP connection to its own in-process loopback server. This proves the
// fanout performs genuine broadcast I/O: the single payload handed to Write must
// arrive verbatim on BOTH accepting sockets, having crossed the fanout, two
// console sinks, and two real kernel-backed TCP streams.
func TestFanout_RealSocketBroadcastsToEveryBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"one record reaches both branch sockets verbatim", "fanout-over-socket\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: stand up two independent loopback servers — one per fanout branch —
			//: so each branch exercises a distinct real TCP stream.
			downA, gotA := dialBranch(t)
			downB, gotB := dialBranch(t)
			s := multi.New(downA, downB)
			t.Cleanup(func() { swallowFanoutClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info}
			//: production Write — the fanout must hand the payload to every branch.
			if _, werr := s.Write(t.Context(), rec, []byte(tc.want)); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			//: both accepting sockets must observe the exact bytes production wrote.
			assertSocketReceived(t, "branch A", gotA, tc.want)
			assertSocketReceived(t, "branch B", gotB, tc.want)
		})
	}
}

// dialBranch binds an ephemeral loopback server, accepts on a goroutine that
// reads one line, dials the server, and wraps the dialed connection in a console
// sink — the production io.Writer for one fanout branch. It returns that sink
// plus a channel delivering the single line the server receives. The accepting
// goroutine keeps its connection open until the test ends so the read never
// races a premature close.
func dialBranch(t *testing.T) (corelogger.Sink, <-chan string) {
	t.Helper()
	//: bind to an ephemeral loopback port — a real listening socket, not an
	//: in-memory pipe, so the branch exercises genuine TCP I/O.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen err = %v", err)
	}
	t.Cleanup(func() { swallowFanoutSocketErr(ln.Close()) })
	got := make(chan string, 1)
	//: done gates the accepting goroutine's connection close on test teardown so
	//: the server side stays open through the read instead of closing mid-stream.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	//: lifecycle: serveOneLine accepts once, reads one line, then blocks on done
	//: until t.Cleanup closes it at test end — bounding the goroutine's life to
	//: this test so it never leaks past teardown.
	go serveOneLine(ln, got, done)
	//: dial the server; this conn is the production io.Writer the console sink
	//: writes through.
	conn, derr := net.Dial("tcp", ln.Addr().String())
	if derr != nil {
		t.Fatalf("net.Dial err = %v", derr)
	}
	t.Cleanup(func() { swallowFanoutSocketErr(conn.Close()) })
	//: console sink over the live conn is this branch's terminal downstream.
	down, cerr := console.New(conn)
	if cerr != nil {
		t.Fatalf("console.New err = %v", cerr)
	}
	return down, got
}

// serveOneLine accepts a single connection on ln, reads one newline-delimited
// line, reports it on got, then keeps the connection open until done closes so
// the accepted socket is never torn down underneath the in-flight read.
func serveOneLine(ln net.Listener, got chan<- string, done <-chan struct{}) {
	//: accept the single producer connection; a failed accept means the
	//: listener was closed, so report empty and let the test time out.
	conn, err := ln.Accept()
	if err != nil {
		//: surface the closed-listener case as an empty line.
		got <- ""
		return
	}
	//: read exactly one line; a read error yields an empty line so the waiting
	//: assertion fails loudly rather than the read result being silently dropped.
	line, rerr := bufio.NewReader(conn).ReadString('\n')
	if rerr != nil {
		//: report empty on a read failure — the assertion mismatch names the branch.
		got <- ""
		<-done
		swallowFanoutSocketErr(conn.Close())
		return
	}
	got <- line
	//: hold the connection open until teardown so the read above completes
	//: against a live socket rather than racing a premature close.
	<-done
	swallowFanoutSocketErr(conn.Close())
}

// assertSocketReceived blocks until got delivers a line and fails the test if it
// does not match want within a deadline. The label distinguishes which branch
// failed when the fanout drops a payload on one side only.
func assertSocketReceived(t *testing.T, label string, got <-chan string, want string) {
	t.Helper()
	//: a bounded wait so a dropped payload surfaces as a failure, not a hang.
	select {
	case line := <-got:
		if line != want {
			t.Errorf("%s received %q, want %q", label, line, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s timed out waiting for the socket to receive the payload", label)
	}
}

// swallowFanoutSocketErr drops a net resource Close/Listen error in cleanup
// paths where the failure is not the assertion target.
func swallowFanoutSocketErr(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// swallowFanoutClose drops the fanout sink's Close error in cleanup paths where
// the close result is not the assertion target.
func swallowFanoutClose(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
