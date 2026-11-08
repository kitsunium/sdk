//go:build !windows

package service_test

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The shell's tests: the actor, its timers on a ManualClock, its observers,
// its end — over a fake spawner whose processes end when the test says.

// fakeProc is a process of the fake spawner.
type fakeProc struct {
	exit     chan coreproc.ExitValue
	pid      int
	stubborn bool
	mu       sync.Mutex
	signals  []coreproc.Signal
	ended    bool
}

func (p *fakeProc) PID() int { return p.pid }

func (p *fakeProc) Wait() (coreproc.ExitValue, error) { return <-p.exit, nil }

// end ends the process once.
func (p *fakeProc) end(v coreproc.ExitValue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.ended = true
		p.exit <- v
	}
}

// Signal records sig; a terminating one ends the process, but SIGTERM on a
// stubborn one.
func (p *fakeProc) Signal(sig coreproc.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	stubborn := p.stubborn
	p.mu.Unlock()
	switch syscall.Signal(sig) {
	case syscall.SIGTERM:
		if !stubborn {
			p.end(coreproc.ExitValue{Code: -1, Signal: sig, Signaled: true})
		}
	case syscall.SIGKILL, syscall.SIGABRT, syscall.SIGINT:
		p.end(coreproc.ExitValue{Code: -1, Signal: sig, Signaled: true})
	}
	return nil
}

func (p *fakeProc) SignalGroup(sig coreproc.Signal) error { return p.Signal(sig) }

func (p *fakeProc) Stop(context.Context, time.Duration, coreproc.Signal) error {
	return p.Signal(coreproc.Signal(syscall.SIGKILL))
}

// sent is what the process was sent.
func (p *fakeProc) sent() []coreproc.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]coreproc.Signal(nil), p.signals...)
}

// fakeSpawner spawns fake processes: "/bin/true" ends 0 at once, "/bin/false"
// ends 1, "/bin/stubborn" ignores SIGTERM, "/bin/unspawnable" is refused,
// anything else runs until signalled.
type fakeSpawner struct {
	mu    sync.Mutex
	next  int
	procs []*fakeProc
	specs []coreproc.Spec
}

func (s *fakeSpawner) Spawn(_ context.Context, spec coreproc.Spec) (coreproc.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if spec.Path == "/bin/unspawnable" {
		return nil, coreproc.InvalidSpec
	}
	s.next++
	p := &fakeProc{pid: 1000 + s.next, exit: make(chan coreproc.ExitValue, 1), stubborn: spec.Path == "/bin/stubborn"}
	s.procs = append(s.procs, p)
	s.specs = append(s.specs, spec)
	switch spec.Path {
	case "/bin/true":
		p.end(coreproc.ExitValue{})
	case "/bin/false":
		p.end(coreproc.ExitValue{Code: 1})
	}
	return p, nil
}

// last is the last process spawned.
func (s *fakeSpawner) last() *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[len(s.procs)-1]
}

// count is how many processes were spawned.
func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

// command parses a test command.
func command(t *testing.T, words ...string) coresvc.CommandValue {
	t.Helper()
	c, err := coresvc.ParseCommand(words)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newUnit builds a unit over the fake spawner and a ManualClock.
func newUnit(t *testing.T, cfg service.Config) (*service.Unit, *fakeSpawner, *clock.ManualClock) {
	t.Helper()
	sp := &fakeSpawner{}
	clk := clock.NewManualClock(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	cfg.Name, cfg.Spawner, cfg.Clock = "unit", sp, clk
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = u.Close(ctx)
	})
	return u, sp, clk
}

// waitState reads a subscription until the unit is at state, failing after
// ten seconds of real time.
func waitState(t *testing.T, sub <-chan coresvc.EventValue, state coresvc.State) coresvc.EventValue {
	t.Helper()
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatalf("subscription closed before %s", state)
			}
			if ev.State == state {
				return ev
			}
		case <-guard.C:
			t.Fatalf("no %s within 10 s", state)
		}
	}
}

// TestUnitStartStop: Start answers once the unit is up; Stop once it is dead;
// every snapshot is published with a growing generation.
func TestUnitStartStop(t *testing.T) {
	u, sp, _ := newUnit(t, service.Config{ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon", "-f")}})
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := u.State()
	if st.State != coresvc.StateActive || st.MainPID != sp.last().pid || st.Generation == 0 {
		t.Fatalf("after Start: %+v", st)
	}
	if err := u.Start(ctx); err != nil || sp.count() != 1 {
		t.Fatalf("a second Start: %v, %d spawns", err, sp.count())
	}
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := u.State(); got.State != coresvc.StateInactive || got.Generation <= st.Generation {
		t.Fatalf("after Stop: %+v", got)
	}
}

// TestUnitStopOutlivesItsCaller: a Stop whose context ends first returns its
// error, and the escalation goes on on the unit's own deadline.
func TestUnitStopOutlivesItsCaller(t *testing.T) {
	u, sp, clk := newUnit(t, service.Config{
		TimeoutStop: 30 * time.Second,
		ExecStart:   []coresvc.CommandValue{command(t, "/bin/stubborn")},
	})
	if err := u.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	main := sp.last()
	ctx, cancel := context.WithCancel(context.Background())
	sub, _ := u.Subscribe(16)
	go func() {
		waitState(t, sub, coresvc.StateDeactivating)
		cancel()
	}()
	if err := u.Stop(ctx); err != context.Canceled {
		t.Fatalf("Stop with a canceled context: %v", err)
	}
	clk.BlockUntil(1)
	clk.Advance(30 * time.Second)
	if err := u.Stop(context.Background()); err != nil {
		t.Fatalf("the second Stop: %v", err)
	}
	sent := main.sent()
	if len(sent) == 0 || syscall.Signal(sent[len(sent)-1]) != syscall.SIGKILL {
		t.Fatalf("signals %v, want SIGKILL last", sent)
	}
	if st := u.State(); st.State != coresvc.StateFailed || st.Result != coresvc.ResultTimeout {
		t.Fatalf("after the escalation: %s %s", st.State, st.Result)
	}
}

// TestUnitFailRestarts: Fail aborts the unit with WatchdogSignal=, its run
// ends with the result watchdog, and Restart=on-watchdog restarts it after
// RestartSec= on the clock.
func TestUnitFailRestarts(t *testing.T) {
	u, sp, clk := newUnit(t, service.Config{
		Restart:   coresvc.RestartOnWatchdog,
		ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")},
	})
	if err := u.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := sp.last()
	sub, _ := u.Subscribe(64)
	if err := u.Fail(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := waitState(t, sub, coresvc.StateAutoRestart)
	if ev.Result != coresvc.ResultWatchdog {
		t.Fatalf("result %s", ev.Result)
	}
	if sent := first.sent(); len(sent) == 0 || syscall.Signal(sent[0]) != syscall.SIGABRT {
		t.Fatalf("signals %v, want SIGABRT", sent)
	}
	clk.BlockUntil(1)
	clk.Advance(100 * time.Millisecond)
	waitState(t, sub, coresvc.StateActive)
	if sp.count() != 2 || u.State().Restarts != 1 {
		t.Fatalf("%d spawns, %d restarts", sp.count(), u.State().Restarts)
	}
}

// TestUnitSubscriptionDropsAndResyncs: the actor never waits on a full queue;
// the newest event is dropped and counted in the unit's Dropped, the
// generations show the gap, and State is the level to resynchronise on.
func TestUnitSubscriptionDropsAndResyncs(t *testing.T) {
	u, _, _ := newUnit(t, service.Config{ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}})
	sub, cancel := u.Subscribe(1)
	ctx := context.Background()
	for range 3 {
		if err := u.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if err := u.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if u.State().Dropped == 0 {
		t.Fatal("nothing dropped from a queue of one")
	}
	first := <-sub
	if first.Generation >= u.State().Generation {
		t.Fatalf("the kept event %d is not behind the level %d", first.Generation, u.State().Generation)
	}
	cancel()
	cancel()
	if _, ok := <-sub; ok {
		t.Fatal("a closed subscription still delivers")
	}
}

// TestUnitConcurrentStarts: concurrent callers join one start job.
func TestUnitConcurrentStarts(t *testing.T) {
	u, sp, _ := newUnit(t, service.Config{ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}})
	var wg sync.WaitGroup
	errsCh := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errsCh <- u.Start(context.Background()) })
	}
	wg.Wait()
	close(errsCh)
	for err := range errsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if sp.count() != 1 {
		t.Fatalf("%d spawns for one unit", sp.count())
	}
}

// TestUnitClose: Close stops the unit, ends the actor and every subscription;
// every later call is UNIT_CLOSED. SurviveFinalKillSignal= leaves the
// processes running.
func TestUnitClose(t *testing.T) {
	u, sp, _ := newUnit(t, service.Config{ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}})
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatal(err)
	}
	sub, _ := u.Subscribe(64)
	if err := u.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for range sub {
	}
	if len(sp.last().sent()) == 0 {
		t.Fatal("Close did not stop the unit")
	}
	if err := u.Start(ctx); !errs.HasCode(err, coresvc.CodeUnitClosed) {
		t.Fatalf("Start after Close: %v", err)
	}
	if late, _ := u.Subscribe(1); func() bool { _, ok := <-late; return ok }() {
		t.Fatal("a subscription after Close is open")
	}
	if err := u.Close(ctx); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	s, ssp, _ := newUnit(t, service.Config{SurviveFinalKillSignal: true, ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ssp.last().sent(); len(got) != 0 {
		t.Fatalf("SurviveFinalKillSignal=: the process got %v", got)
	}
	ssp.last().end(coreproc.ExitValue{})
}

// TestUnitLeavesNoGoroutine runs a unit's whole life in a synctest bubble:
// the bubble fails the test if any goroutine the unit started is still
// blocked when it ends — the actor, the notification reader, the waits.
func TestUnitLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := &fakeSpawner{}
		src := &chanNotify{msgs: make(chan coresvc.NotifyValue)}
		u, err := service.NewUnit(&service.Config{
			Name: "u", Spawner: sp, Notify: src, Type: coresvc.TypeNotify,
			Restart: coresvc.RestartOnFailure, RestartDelay: time.Second,
			ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}, ExecStopPost: []coresvc.CommandValue{command(t, "/bin/true")},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, cancel := u.Subscribe(4)
		ctx := context.Background()
		for range 3 {
			done := make(chan error, 1)
			go func() { done <- u.Start(ctx) }()
			synctest.Wait()
			src.msgs <- coresvc.NotifyValue{Fields: map[string]string{"READY": "1"}, PID: sp.last().pid, Verified: true}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			sp.last().end(coreproc.ExitValue{Code: 1}) // crash: restarts after 1 s
			time.Sleep(2 * time.Second)
			src.msgs <- coresvc.NotifyValue{Fields: map[string]string{"READY": "1"}, PID: sp.last().pid, Verified: true}
			if err := u.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := u.Close(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
	})
}

// chanNotify is a NotifySource fed by a channel.
type chanNotify struct {
	msgs chan coresvc.NotifyValue
}

func (c *chanNotify) Socket() string { return "@chan" }

func (c *chanNotify) Recv(ctx context.Context) (coresvc.NotifyValue, error) {
	select {
	case m := <-c.msgs:
		return m, nil
	case <-ctx.Done():
		return coresvc.NotifyValue{}, ctx.Err()
	}
}

func (c *chanNotify) Close() error { return nil }

// TestUnitStartTimeoutOnManualClock: TimeoutStartSec= and EXTEND_TIMEOUT_USEC=
// on the unit's clock — the deadline at 5 s pushed to 7 s by a message at 4 s,
// then the terminate, the main process's end and START_FAILED with the result
// timeout.
func TestUnitStartTimeoutOnManualClock(t *testing.T) {
	sp := &fakeSpawner{}
	src := &chanNotify{msgs: make(chan coresvc.NotifyValue)}
	origin := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(origin)
	u, err := service.NewUnit(&service.Config{
		Name: "u", Spawner: sp, Clock: clk, Notify: src, Type: coresvc.TypeNotify,
		TimeoutStart: 5 * time.Second, ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close(context.Background()) })
	sub, _ := u.Subscribe(64)
	done := make(chan error, 1)
	go func() { done <- u.Start(context.Background()) }()
	waitState(t, sub, coresvc.StateActivating)
	clk.BlockUntil(1)
	clk.Advance(4 * time.Second)
	src.msgs <- coresvc.NotifyValue{Fields: map[string]string{"EXTEND_TIMEOUT_USEC": "3000000", "STATUS": "slow"}, PID: sp.last().pid, Verified: true}
	for waitState(t, sub, coresvc.StateActivating).Status != "slow" {
		// the events before the message's
	}
	clk.Advance(2 * time.Second)
	clk.Advance(time.Second)
	ev := waitState(t, sub, coresvc.StateDeactivating)
	if got := ev.Changed.Sub(origin); got != 7*time.Second {
		t.Fatalf("the start timed out at %s, want 7 s", got)
	}
	if err := <-done; !errs.HasCode(err, coresvc.CodeStartFailed) || u.State().Result != coresvc.ResultTimeout {
		t.Fatalf("Start: %v, result %s", err, u.State().Result)
	}
}
