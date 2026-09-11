// Package health_test — the lifecycle wiring, and the ordering it exists for.
package health_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// TestNotReadyPrecedesTheDrain is the reason this domain hooks into lifecycle
// at all, asserted through a real Lifecycle rather than described.
//
// Added LAST, the health component stops FIRST — so readiness reports
// not-ready before any other component has begun closing. Get that order
// wrong and the drain runs while the load balancer is still sending traffic:
// the connections the drain waits for keep being created, and the shutdown
// budget expires against work that never stops arriving.
func TestNotReadyPrecedesTheDrain(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{Name: "db", Check: passing(&calls)})

	//: what the server SAW when it was asked to stop.
	var readyAtServerStop atomic.Bool
	app := svclc.New(svclc.Config{})
	//: the server is added first, so it stops LAST.
	mustAddComponent(t, app, corelc.ComponentValue{
		Name:  "http",
		Start: func(context.Context) error { return nil },
		Stop: func(ctx context.Context) error {
			readyAtServerStop.Store(registry.Probe(ctx, corehealth.ProbeReadiness).Status.Serving())
			return nil
		},
	})
	//: health is added LAST, so it stops FIRST.
	mustAddComponent(t, app, svchealth.Component(registry, "health"))

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	if !registry.Probe(ctx, corehealth.ProbeReadiness).Status.Serving() {
		t.Fatal("the replica is not ready after a successful start")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if readyAtServerStop.Load() {
		t.Error("the server was still advertised as ready when it was told to stop — " +
			"the drain would run against traffic that keeps arriving")
	}
}

// TestStartupGatesTheLifecycle pins that a failing startup check keeps the
// application from declaring itself up, and that the caller's OWN error is
// what comes back — relabelling it here would cost them their errors.Is.
func TestStartupGatesTheLifecycle(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddStartup(t, registry, corehealth.StartupCheckValue{
		Name: "migrations", Check: failingWith(&calls, errDependency),
	})
	component := svchealth.Component(registry, "health")
	err := component.Start(context.Background())
	if err == nil {
		t.Fatal("Start = nil while a startup check is failing")
	}
	if !errors.Is(err, errDependency) {
		t.Errorf("Start = %v, want the caller's own error to survive", err)
	}
	//: and it still carries the SDK's identity, so a caller who routes on
	//: codes rather than on sentinels gets an answer too.
	if !errs.HasCode(err, svchealth.CodeCheckFailed) {
		t.Errorf("Start = %v, want CHECK_FAILED as well", err)
	}
}

// TestAnEmptyRegistryStartsAndDrainsCleanly pins the smallest legitimate
// wiring: a component with no checks at all starts, stops, and takes the
// replica out of rotation on the way (ADR 0031).
func TestAnEmptyRegistryStartsAndDrainsCleanly(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	component := svchealth.Component(registry, "health")
	ctx := context.Background()
	if err := component.Start(ctx); err != nil {
		t.Fatalf("Start on an empty registry = %v, want nil", err)
	}
	if !registry.Probe(ctx, corehealth.ProbeReadiness).Status.Serving() {
		t.Error("an empty registry is not ready; the process answering IS the evidence")
	}
	if err := component.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if registry.Probe(ctx, corehealth.ProbeReadiness).Status.Serving() {
		t.Error("Stop did not withdraw the replica from routing")
	}
	//: liveness keeps answering right through, so nothing kills the process
	//: while it drains.
	if !registry.Probe(ctx, corehealth.ProbeLiveness).Status.Serving() {
		t.Error("liveness stopped answering during the drain")
	}
}

// mustAddComponent registers a lifecycle component or fails the test.
func mustAddComponent(tb testing.TB, app corelc.Lifecycle, component corelc.ComponentValue) {
	tb.Helper()
	if err := app.Add(component); err != nil {
		tb.Fatalf("Add(%q) = %v, want nil", component.Name, err)
	}
}
