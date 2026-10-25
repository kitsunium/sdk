// Package server — R1 (ADR 0188): a ring drained while others accept is a
// fault, a loop group every ring of which is drained makes Serve return
// LoopGroupStalled and liveness fail after loopStallGrace; readiness is built
// from State and registered with the health registry.
package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/app/health"
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeEngine is a loop engine whose rings say what the test sets.
type fakeEngine struct {
	mu       sync.Mutex
	group    string
	draining []bool
}

func (f *fakeEngine) Shutdown(context.Context) error { return nil }
func (f *fakeEngine) Close() error                   { return nil }

func (f *fakeEngine) set(draining ...bool) {
	f.mu.Lock()
	f.draining = draining
	f.mu.Unlock()
}

func (f *fakeEngine) State() corenet.LoopStateValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := corenet.LoopStateValue{Group: f.group, Backend: corenet.LoopBackendIOURing, Loops: 1, Rings: len(f.draining)}
	for i, d := range f.draining {
		st.RingStates = append(st.RingStates, corenet.LoopRingStateValue{Ring: i, Draining: d})
	}
	return st
}

// servingWith is a started server with no group, the fake engine added as its
// loop group and a manual clock.
func servingWith(t *testing.T, eng *fakeEngine) (*Server, *clock.ManualClock) {
	t.Helper()
	srv := newServer(envOf(nil))
	manual := clock.NewManualClock(time.Unix(0, 0))
	srv.clk = manual
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.mu.Lock()
	srv.loopEngines = append(srv.loopEngines, eng)
	srv.mu.Unlock()
	return srv, manual
}

// sources lists the faults' sources.
func sources(fs []corenet.FaultValue) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Source)
	}
	return out
}

// TestStall_faultsAndLiveness walks one group from healthy to one ring drained
// to every ring drained: the first is a ring fault and the server stays ready
// and alive; the second is a stall fault, readiness fails at once, and
// liveness only once loopStallGrace has passed on the engine's clock.
func TestStall_faultsAndLiveness(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{group: "loop"}
	eng.set(false, false)
	srv, manual := servingWith(t, eng)

	srv.checkStalls()
	if st := srv.State(); st.Degraded() || readinessOf(st) != nil || srv.liveness() != nil {
		t.Fatalf("a healthy group reads %+v", st.Faults)
	}

	eng.set(true, false)
	srv.checkStalls()
	st := srv.State()
	if got := sources(st.Faults); len(got) != 1 || got[0] != "ring" || !st.Degraded() {
		t.Fatalf("one drained ring: faults %v", st.Faults)
	}
	if readinessOf(st) != nil || srv.liveness() != nil {
		t.Fatal("one drained ring made the server unready or dead")
	}
	select {
	case <-srv.stall.stalled:
		t.Fatal("one drained ring is not a stall")
	default:
	}

	eng.set(true, true)
	srv.checkStalls()
	st = srv.State()
	if got := sources(st.Faults); len(got) != 1 || got[0] != "stall" {
		t.Fatalf("every ring drained: faults %v", st.Faults)
	}
	if err := readinessOf(st); !errs.HasCode(err, corenet.CodeLoopGroupStalled) {
		t.Fatalf("readiness = %v, want LoopGroupStalled", err)
	}
	select {
	case <-srv.stall.stalled:
	default:
		t.Fatal("the stall was not signalled")
	}
	if err := srv.liveness(); err != nil {
		t.Fatalf("liveness failed within the grace: %v", err)
	}
	manual.Advance(loopStallGrace - time.Nanosecond)
	if err := srv.liveness(); err != nil {
		t.Fatalf("liveness failed a nanosecond early: %v", err)
	}
	manual.Advance(time.Nanosecond)
	if err := srv.liveness(); !errs.HasCode(err, corenet.CodeLoopGroupStalled) {
		t.Fatalf("liveness = %v after the grace, want LoopGroupStalled", err)
	}
}

// TestStall_aDrainIsNotAStall pins that rings drained by Shutdown are not read
// as a stall: nothing is watched outside PhaseServing.
func TestStall_aDrainIsNotAStall(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{group: "loop"}
	eng.set(true, true)
	srv, _ := servingWith(t, eng)
	srv.phase.Store(uint32(corenet.PhaseDraining))
	srv.checkStalls()
	if g, _ := srv.stalledGroup(); g != "" {
		t.Fatalf("a drain was recorded as a stall of %q", g)
	}
	if fs := srv.State().Faults; len(fs) != 0 {
		t.Fatalf("a drain reads as faults %v", fs)
	}
}

// TestStall_theWatcherTicksOnTheEnginesClock starts the watcher on the
// manual clock and moves it one poll: the stall is seen without sleeping.
func TestStall_theWatcherTicksOnTheEnginesClock(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{group: "loop"}
	eng.set(true)
	srv, manual := servingWith(t, eng)
	srv.startStallWatch()
	waitArmed(t, manual)
	manual.Advance(stallPoll)
	select {
	case <-srv.stall.stalled:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not see the stall")
	}
	srv.stopStallWatch()
}

// TestServe_returnsLoopGroupStalled pins that Serve returns, drained, with
// LoopGroupStalled naming the group, once a stall is recorded.
func TestServe_returnsLoopGroupStalled(t *testing.T) {
	t.Parallel()
	srv := newServer(envOf(nil))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background()) }()
	deadline := time.Now().Add(10 * time.Second)
	for corenet.Phase(srv.phase.Load()) != corenet.PhaseServing {
		if time.Now().After(deadline) {
			t.Fatal("Serve never started")
		}
		time.Sleep(time.Millisecond)
	}
	srv.markStalled("loop")
	select {
	case err := <-done:
		if !errs.HasCode(err, corenet.CodeLoopGroupStalled) {
			t.Fatalf("Serve = %v, want LoopGroupStalled", err)
		}
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Fatalf("Serve = %T", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return on the stall")
	}
	if p := corenet.Phase(srv.phase.Load()); p != corenet.PhaseStopped {
		t.Fatalf("phase after the stall = %v, want stopped", p)
	}
}

// fakeHealth records the checks registered on it, refusing a second one of a
// name as the registry does.
type fakeHealth struct {
	readiness []corehealth.ReadinessCheckValue
	liveness  []corehealth.LivenessCheckValue
	refuse    error
}

func (f *fakeHealth) AddStartup(corehealth.StartupCheckValue) error { return nil }
func (f *fakeHealth) AddReadiness(c corehealth.ReadinessCheckValue) error {
	if f.refuse != nil {
		return f.refuse
	}
	f.readiness = append(f.readiness, c)
	return nil
}

func (f *fakeHealth) AddLiveness(c corehealth.LivenessCheckValue) error {
	f.liveness = append(f.liveness, c)
	return nil
}

func (f *fakeHealth) Probe(context.Context, corehealth.Probe) corehealth.ReportValue {
	return corehealth.ReportValue{}
}
func (f *fakeHealth) Drain() {}

// TestRegisterHealth registers both checks under one name, readiness from
// State through the phases, and refuses a nil registry and a refused
// registration.
func TestRegisterHealth(t *testing.T) {
	t.Parallel()
	srv := newServer(envOf(nil))
	h := &fakeHealth{}
	if err := srv.RegisterHealth(h); err != nil {
		t.Fatalf("RegisterHealth: %v", err)
	}
	if len(h.readiness) != 1 || len(h.liveness) != 1 ||
		h.readiness[0].Name != "net/server" || h.liveness[0].Name != "net/server" {
		t.Fatalf("registered %+v / %+v", h.readiness, h.liveness)
	}
	ready := h.readiness[0].Check
	if err := ready(t.Context()); !errs.HasCode(err, corenet.CodeNotStarted) {
		t.Fatalf("readiness before Start = %v", err)
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ready(t.Context()); err != nil {
		t.Fatalf("readiness while serving = %v", err)
	}
	if err := h.liveness[0].Check(); err != nil {
		t.Fatalf("liveness while serving = %v", err)
	}
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := ready(t.Context()); !errs.HasCode(err, corenet.CodeServerClosed) {
		t.Fatalf("readiness after Shutdown = %v", err)
	}
	if err := srv.RegisterHealth(nil); !errs.HasCode(err, corehealth.CodeInvalidCheck) {
		t.Fatalf("RegisterHealth(nil) = %v", err)
	}
	refused := errs.Wrap(corehealth.DuplicateCheck, errs.WrapParams{})
	if err := srv.RegisterHealth(&fakeHealth{refuse: refused}); !errs.HasCode(err, corehealth.CodeDuplicateCheck) {
		t.Fatalf("a refused registration = %v", err)
	}
}

// TestState_reportsPlatformAndFallbacks pins the platform's fields and a
// fallback's count and last occurrence.
func TestState_reportsPlatformAndFallbacks(t *testing.T) {
	t.Parallel()
	srv := newServer(envOf(nil))
	manual := clock.NewManualClock(time.Unix(42, 0))
	srv.clk = manual
	st := srv.State()
	if st.Platform.GOOS == "" || st.Platform.GOARCH == "" || st.Platform.Go == "" {
		t.Fatalf("platform = %+v", st.Platform)
	}
	if st.Fallbacks.Count != 0 || !st.Fallbacks.LastAt.IsZero() {
		t.Fatalf("fallbacks before any = %+v", st.Fallbacks)
	}
	srv.recordFallback("a", "first")
	manual.Advance(time.Second)
	srv.recordFallback("b", "second")
	f := srv.State().Fallbacks
	if f.Count != 2 || f.LastGroup != "b" || f.LastReason != "second" || !f.LastAt.Equal(time.Unix(43, 0)) {
		t.Fatalf("fallbacks = %+v", f)
	}
}
