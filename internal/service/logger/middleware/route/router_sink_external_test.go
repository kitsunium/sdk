package route_test

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
	"github.com/kitsunium/sdk/internal/service/logger/middleware/route"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

type recordingSink struct {
	writes atomic.Int64
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes.Add(1)
	return len(p), nil
}

func (r *recordingSink) Flush(_ context.Context) error { return nil }
func (r *recordingSink) Close() error                  { return nil }

// erroringSink is a Sink whose Flush and Close always fail with a fixed
// error so the router's error-aggregation arms (errors.Join over per-sink
// failures) are exercised on both the entry and the fallback path.
type erroringSink struct {
	err error
}

func (e *erroringSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}

func (e *erroringSink) Flush(_ context.Context) error { return e.err }
func (e *erroringSink) Close() error                  { return e.err }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		recLevel level.Level
		wantHits string
	}{
		{"warn matches the warn route", level.Warn, "warn"},
		{"info falls through to fallback", level.Info, "fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			warnSink := &recordingSink{}
			fallback := &recordingSink{}
			r := route.New(fallback, route.Params{
				When: route.LevelAtLeast(level.Warn),
				Sink: warnSink,
			})
			//: incomplete Params is silently skipped — asserting via the
			//: fallback hit count proves the entry was dropped (no match
			//: ⇒ fallback receives the write).
			incompleteSink := &recordingSink{}
			incompleteFallback := &recordingSink{}
			rIncomplete := route.New(incompleteFallback, route.Params{When: nil, Sink: incompleteSink})
			if _, err := rIncomplete.Write(t.Context(), corelogger.RecordEvent{Level: level.Warn}, []byte("y")); err != nil {
				t.Errorf("incomplete Params Write err = %v", err)
			}
			if incompleteFallback.writes.Load() != 1 {
				t.Errorf("incomplete Params: fallback writes = %d, want 1", incompleteFallback.writes.Load())
			}
			if incompleteSink.writes.Load() != 0 {
				t.Errorf("incomplete Params: incompleteSink should not have received writes via rIncomplete")
			}
			rec := corelogger.RecordEvent{Level: tc.recLevel}
			if _, err := r.Write(t.Context(), rec, []byte("x")); err != nil {
				t.Errorf("Write err = %v", err)
			}
			switch tc.wantHits {
			case "warn":
				if warnSink.writes.Load() != 1 || fallback.writes.Load() != 0 {
					t.Errorf("warn route hit count wrong: warn=%d fallback=%d", warnSink.writes.Load(), fallback.writes.Load())
				}
			case "fallback":
				if warnSink.writes.Load() != 0 || fallback.writes.Load() != 1 {
					t.Errorf("fallback hit count wrong: warn=%d fallback=%d", warnSink.writes.Load(), fallback.writes.Load())
				}
			}
		})
	}
}

func TestRouterNoMatchWithoutFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty router returns NoMatch sentinel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := route.New(nil)
			_, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errs.HasCode(err, route.CodeRouteNoMatch) {
				t.Errorf("err = %v, want NoMatch", err)
			}
		})
	}
}

func TestRouter_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close hit every route + fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{}
			b := &recordingSink{}
			r := route.New(b, route.Params{When: route.LevelAtLeast(level.Error), Sink: a})
			if err := r.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v", err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
		})
	}
}

func TestRouter_FlushAndClose_AggregatesErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close join the entry and fallback failures"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: distinct sentinels prove the join captured BOTH the entry and the
			//: fallback failure rather than short-circuiting on the first.
			entryErr := errors.New("entry sink failed")
			fallbackErr := errors.New("fallback sink failed")
			entrySink := &erroringSink{err: entryErr}
			fallbackSink := &erroringSink{err: fallbackErr}
			r := route.New(fallbackSink, route.Params{When: route.LevelAtLeast(level.Error), Sink: entrySink})
			//: Flush walks the entry then the fallback, joining both failures.
			ferr := r.Flush(t.Context())
			if !errors.Is(ferr, entryErr) || !errors.Is(ferr, fallbackErr) {
				t.Errorf("Flush err = %v, want join of entry + fallback failures", ferr)
			}
			//: Close mirrors Flush — both downstream Close failures are joined.
			cerr := r.Close()
			if !errors.Is(cerr, entryErr) || !errors.Is(cerr, fallbackErr) {
				t.Errorf("Close err = %v, want join of entry + fallback failures", cerr)
			}
		})
	}
}

func TestNoMatchSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NoMatch carries 0.3.18.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(route.NoMatch, route.CodeRouteNoMatch) {
				t.Errorf("HasCode(NoMatch) = false")
			}
		})
	}
}

// alwaysTrue is a Predicate that matches every record. Used to pin the
// first-match-wins ordering and the byte-count propagation paths to a
// guaranteed-hit entry independent of record level.
func alwaysTrue(_ corelogger.RecordEvent) bool { return true }

// TestRouterNoMatch_TypedCode is the black-box mirror of the internal
// NoMatch assertion: it proves the public sentinel both carries the
// dotted-quad code AND satisfies errors.Is, on both the empty-router path
// and a non-matching-predicate path with no fallback.
func TestRouterNoMatch_TypedCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []route.Params
		recLvl  level.Level
	}{
		{"empty router no fallback", nil, level.Info},
		//: a predicate that demands Error against an Info record misses; with
		//: no fallback the same typed sentinel must surface, proving the code
		//: is intrinsic to NoMatch and not an artefact of the empty table.
		{"predicate misses no fallback", []route.Params{{When: route.LevelAtLeast(level.Error), Sink: &recordingSink{}}}, level.Info},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := route.New(nil, tc.entries...)
			_, err := r.Write(t.Context(), corelogger.RecordEvent{Level: tc.recLvl}, []byte("x"))
			//: dotted-quad identity — the wire-routable code consumers match on.
			if !errs.HasCode(err, route.CodeRouteNoMatch) {
				t.Errorf("HasCode(err, CodeRouteNoMatch) = false, err = %v", err)
			}
			//: sentinel identity — errors.Is must also hold for the public var.
			if !errors.Is(err, route.NoMatch) {
				t.Errorf("errors.Is(err, NoMatch) = false, err = %v", err)
			}
		})
	}
}

// TestRouter_FirstMatchWins proves the router stops at the first matching
// entry: a second entry behind a matching first one is never consulted, and
// a missing first entry correctly falls through to a matching second.
func TestRouter_FirstMatchWins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		firstPred  route.Predicate
		secondPred route.Predicate
		recLvl     level.Level
		wantFirst  int64
		wantSecond int64
	}{
		//: always-true first short-circuits the walk before the second entry's
		//: predicate is ever evaluated.
		{"first matches second never called", alwaysTrue, route.LevelAtLeast(level.Error), level.Error, 1, 0},
		//: first predicate misses on an Info record, so the walk advances to
		//: the always-true second entry.
		{"first misses second matches", route.LevelAtLeast(level.Error), alwaysTrue, level.Info, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := &recordingSink{}
			second := &recordingSink{}
			r := route.New(
				nil,
				route.Params{When: tc.firstPred, Sink: first},
				route.Params{When: tc.secondPred, Sink: second},
			)
			if _, err := r.Write(t.Context(), corelogger.RecordEvent{Level: tc.recLvl}, []byte("x")); err != nil {
				t.Errorf("Write err = %v", err)
			}
			if first.writes.Load() != tc.wantFirst {
				t.Errorf("first writes = %d, want %d", first.writes.Load(), tc.wantFirst)
			}
			if second.writes.Load() != tc.wantSecond {
				t.Errorf("second writes = %d, want %d", second.writes.Load(), tc.wantSecond)
			}
		})
	}
}

// TestRouter_Write_ReturnsByteCount proves the byte count returned by the
// downstream sink propagates verbatim through the router on both the
// matched-entry path and the fallback path.
func TestRouter_Write_ReturnsByteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fallback bool
	}{
		//: matched entry returns len(p); the router must not rewrite n.
		{"matched entry propagates n", false},
		//: fallback path returns len(p); the router must propagate it too.
		{"fallback propagates n", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingSink{}
			var r corelogger.Sink
			if tc.fallback {
				//: no entries — every write falls through to the fallback sink.
				r = route.New(sink)
			} else {
				//: single always-matching entry routes the write to sink.
				r = route.New(nil, route.Params{When: alwaysTrue, Sink: sink})
			}
			payload := []byte("7-bytes")
			n, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, payload)
			if err != nil {
				t.Errorf("Write err = %v", err)
			}
			//: recordingSink returns len(p); the router must surface it unchanged.
			if n != len(payload) {
				t.Errorf("n = %d, want %d", n, len(payload))
			}
		})
	}
}

// TestNew_DropsSinkNilEntry proves an entry with a nil Sink is dropped at
// construction: the write falls through to the fallback rather than panicking
// on the nil reference or returning NoMatch.
func TestNew_DropsSinkNilEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"entry with non-nil When but nil Sink is dropped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fallback := &recordingSink{}
			//: When is valid but Sink is nil — the documented contract drops it,
			//: so the always-true predicate never routes anywhere.
			r := route.New(fallback, route.Params{When: alwaysTrue, Sink: nil})
			_, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: dropped entry means no match in the table → fallback, never NoMatch.
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			if fallback.writes.Load() != 1 {
				t.Errorf("fallback writes = %d, want 1", fallback.writes.Load())
			}
		})
	}
}

// TestNew_DropsBothNilEntry proves an entry with both fields nil is dropped,
// leaving the fallback to receive every write.
func TestNew_DropsBothNilEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"entry with nil When and nil Sink is dropped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fallback := &recordingSink{}
			//: both fields nil — the entry is wholly invalid and dropped.
			r := route.New(fallback, route.Params{When: nil, Sink: nil})
			_, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			if fallback.writes.Load() != 1 {
				t.Errorf("fallback writes = %d, want 1", fallback.writes.Load())
			}
		})
	}
}

// TestRouter_FlushClose_OnlyEntryFails proves error isolation on the
// aggregation path: when only the entry sink fails, the joined error carries
// the entry failure and NOT a spurious fallback failure.
func TestRouter_FlushClose_OnlyEntryFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"entry errs, fallback succeeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entryErr := errors.New("entry sink failed")
			fallbackErr := errors.New("fallback sink failed")
			//: entry sink fails; fallback is a clean recordingSink so only the
			//: entry's error must appear in the join.
			r := route.New(&recordingSink{}, route.Params{When: route.LevelAtLeast(level.Error), Sink: &erroringSink{err: entryErr}})
			ferr := r.Flush(t.Context())
			//: join must carry the entry failure but never the fallback sentinel.
			if !errors.Is(ferr, entryErr) || errors.Is(ferr, fallbackErr) {
				t.Errorf("Flush err = %v, want only entry failure", ferr)
			}
			cerr := r.Close()
			if !errors.Is(cerr, entryErr) || errors.Is(cerr, fallbackErr) {
				t.Errorf("Close err = %v, want only entry failure", cerr)
			}
		})
	}
}

// TestRouter_FlushClose_OnlyFallbackFails proves the mirror isolation: when
// only the fallback fails, the joined error carries the fallback failure and
// NOT a spurious entry failure.
func TestRouter_FlushClose_OnlyFallbackFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"entry succeeds, fallback errs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entryErr := errors.New("entry sink failed")
			fallbackErr := errors.New("fallback sink failed")
			//: fallback fails; entry is a clean recordingSink so only the
			//: fallback's error must appear in the join.
			r := route.New(&erroringSink{err: fallbackErr}, route.Params{When: route.LevelAtLeast(level.Error), Sink: &recordingSink{}})
			ferr := r.Flush(t.Context())
			//: join must carry the fallback failure but never the entry sentinel.
			if errors.Is(ferr, entryErr) || !errors.Is(ferr, fallbackErr) {
				t.Errorf("Flush err = %v, want only fallback failure", ferr)
			}
			cerr := r.Close()
			if errors.Is(cerr, entryErr) || !errors.Is(cerr, fallbackErr) {
				t.Errorf("Close err = %v, want only fallback failure", cerr)
			}
		})
	}
}

// TestRouter_ConcurrentWrite proves the stateless dispatch is safe under
// concurrent producers: 50 goroutines each Write once through the same
// always-matching entry. Meaningful under -race, which would flag any
// unsynchronised access in the router's dispatch loop.
func TestRouter_ConcurrentWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
	}{
		{"50 concurrent writers each land once", 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingSink{}
			r := route.New(nil, route.Params{When: alwaysTrue, Sink: sink})
			//: failed records any Write error so a goroutine failure surfaces
			//: as an assertion rather than being silently discarded.
			var failed atomic.Int64
			var wg sync.WaitGroup
			wg.Add(tc.goroutines)
			//: lifecycle: each writer goroutine performs exactly one Write and
			//: exits; wg.Wait below is the join point, so none outlive the test.
			for range tc.goroutines {
				go func() {
					defer wg.Done()
					//: single write per goroutine; recordingSink counts atomically
					//: so the only thing under test is router-side data-race safety.
					if _, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); err != nil {
						failed.Add(1)
					}
				}()
			}
			wg.Wait()
			if got := failed.Load(); got != 0 {
				t.Errorf("concurrent Write errors = %d, want 0", got)
			}
			if got := sink.writes.Load(); got != int64(tc.goroutines) {
				t.Errorf("writes = %d, want %d", got, tc.goroutines)
			}
		})
	}
}

// TestRouter_RealSocketDeliversPayload drives production Write end-to-end
// through the router into a console sink whose io.Writer is a live TCP
// connection to an in-process loopback server. The router's first-match
// entry must forward the bytes onto a genuine kernel-backed socket: what the
// producer hands to Write must arrive verbatim on the accepting connection.
func TestRouter_RealSocketDeliversPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"matched entry forwards across the socket verbatim", "route-over-socket\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: dialRouteSocket stands up a real loopback server and hands back the
			//: console sink wired to the live conn plus the channel that delivers
			//: the bytes the server reads.
			down, got := dialRouteSocket(t)
			r := route.New(nil, route.Params{When: route.LevelAtLeast(level.Error), Sink: down})
			rec := corelogger.RecordEvent{Level: level.Error}
			//: production Write — the matched entry forwards the payload through
			//: the console sink onto the socket.
			if _, werr := r.Write(t.Context(), rec, []byte(tc.want)); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			//: the bytes observed on the accepting socket must match exactly
			//: what production handed to Write.
			assertRouteSocketReceived(t, got, tc.want)
		})
	}
}

// dialRouteSocket stands up an in-process loopback TCP server, launches an
// accepting goroutine that reads one line, dials the server, and wraps the
// dialed connection in a console sink — the production io.Writer the router's
// matched entry writes through. It returns that sink plus a channel delivering
// the single line the server receives. The accepting goroutine holds its
// connection open until the test ends so the read never races a premature close.
func dialRouteSocket(t *testing.T) (corelogger.Sink, <-chan string) {
	t.Helper()
	//: bind to an ephemeral loopback port — a real listening socket, not an
	//: in-memory pipe, so the E2E exercises genuine TCP I/O.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen err = %v", err)
	}
	t.Cleanup(func() { swallowRouteSocketErr(ln.Close()) })
	got := make(chan string, 1)
	//: done gates the accepting goroutine's connection close on test teardown so
	//: the server side stays open through the read instead of closing mid-stream.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	//: lifecycle: serveOneRouteLine accepts once, reads one line, then blocks on
	//: done until t.Cleanup closes it at test end — bounding the goroutine's life
	//: to this test so it never leaks past teardown.
	go serveOneRouteLine(ln, got, done)
	//: dial the server; this conn is the production io.Writer the console sink
	//: writes through.
	conn, derr := net.Dial("tcp", ln.Addr().String())
	if derr != nil {
		t.Fatalf("net.Dial err = %v", derr)
	}
	t.Cleanup(func() { swallowRouteSocketErr(conn.Close()) })
	//: console sink over the live conn is the router's terminal downstream.
	down, cerr := console.New(conn)
	if cerr != nil {
		t.Fatalf("console.New err = %v", cerr)
	}
	return down, got
}

// serveOneRouteLine accepts a single connection on ln, reads one
// newline-delimited line, reports it on got, then keeps the connection open
// until done closes so the accepted socket is never torn down underneath the
// in-flight read.
func serveOneRouteLine(ln net.Listener, got chan<- string, done <-chan struct{}) {
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
		//: report empty on a read failure, then hold open until teardown.
		got <- ""
		<-done
		swallowRouteSocketErr(conn.Close())
		return
	}
	got <- line
	//: hold the connection open until teardown so the read above completes
	//: against a live socket rather than racing a premature close.
	<-done
	swallowRouteSocketErr(conn.Close())
}

// assertRouteSocketReceived blocks until got delivers a line and fails the test
// if it does not match want within a bounded deadline.
func assertRouteSocketReceived(t *testing.T, got <-chan string, want string) {
	t.Helper()
	//: a bounded wait so a dropped payload surfaces as a failure, not a hang.
	select {
	case line := <-got:
		if line != want {
			t.Errorf("socket received %q, want %q", line, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the socket to receive the payload")
	}
}

// swallowRouteSocketErr drops a net resource Close/Listen error in cleanup
// paths where the failure is not the assertion target.
func swallowRouteSocketErr(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
