// Package lifecycle_test — black-box tests for the public facade: the surface
// a consumer actually compiles against.
package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/lifecycle"
)

// errDial stands in for a component's own error. A component belongs to the
// consumer, so what it returns is never an SDK error — which is the whole
// reason the aggregate keeps both.
var errDial = errors.New("dial tcp: connection refused")

// component returns a clean component that records its calls into log.
func component(name string, log *[]string) lifecycle.Component {
	return lifecycle.Component{
		Name: name,
		Start: func(_ context.Context) error {
			*log = append(*log, "start:"+name)
			return nil
		},
		Stop: func(_ context.Context) error {
			*log = append(*log, "stop:"+name)
			return nil
		},
	}
}

// TestTheFacadeStartsInOrderAndStopsInReverse is the promise a consumer buys,
// asserted through the public types only.
func TestTheFacadeStartsInOrderAndStopsInReverse(t *testing.T) {
	t.Parallel()
	var log []string
	app := lifecycle.New(lifecycle.Config{StopTimeout: time.Second})
	for _, name := range []string{"db", "cache", "http"} {
		if err := app.Add(component(name, &log)); err != nil {
			t.Fatalf("Add(%q): %v", name, err)
		}
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{"start:db", "start:cache", "start:http", "stop:http", "stop:cache", "stop:db"}
	for i := range want {
		if i >= len(log) || log[i] != want[i] {
			t.Fatalf("call order = %v, want %v", log, want)
		}
	}
}

// TestAPartialStartIsCleanedUpThroughTheFacade. The unwind is the reason a
// consumer reaches for this package instead of writing four defers, so it is
// asserted at the boundary they actually use.
func TestAPartialStartIsCleanedUpThroughTheFacade(t *testing.T) {
	t.Parallel()
	var log []string
	app := lifecycle.New(lifecycle.Config{})
	if err := app.Add(component("db", &log)); err != nil {
		t.Fatalf("Add(db): %v", err)
	}
	broken := component("http", &log)
	broken.Start = func(_ context.Context) error { log = append(log, "start:http"); return errDial }
	if err := app.Add(broken); err != nil {
		t.Fatalf("Add(http): %v", err)
	}

	err := app.Start(context.Background())
	if err == nil {
		t.Fatalf("Start succeeded despite a failing component")
	}
	//: both questions answer: the SDK's verdict, and the component's own error.
	if !errs.HasCode(err, lifecycle.StartFailed.Code()) {
		t.Fatalf("the aggregate does not carry START_FAILED: %v", err)
	}
	if !errors.Is(err, errDial) {
		t.Fatalf("the component's own error did not survive: %v", err)
	}
	want := []string{"start:db", "start:http", "stop:db"}
	for i := range want {
		if i >= len(log) || log[i] != want[i] {
			t.Fatalf("call order = %v, want %v", log, want)
		}
	}
}

// TestRefusalsSurfaceThroughTheFacade. A consumer matching on these needs the
// re-exported sentinels to be the same values the engine emits, not copies.
func TestRefusalsSurfaceThroughTheFacade(t *testing.T) {
	t.Parallel()
	var log []string
	app := lifecycle.New(lifecycle.Config{})
	if err := app.Add(lifecycle.Component{Name: "db"}); !errors.Is(err, lifecycle.InvalidComponent) {
		t.Fatalf("a component with no Start returned %v, want InvalidComponent", err)
	}
	if err := app.Add(component("db", &log)); err != nil {
		t.Fatalf("Add(db): %v", err)
	}
	if err := app.Add(component("db", &log)); !errors.Is(err, lifecycle.DuplicateComponent) {
		t.Fatalf("a duplicate name returned %v, want DuplicateComponent", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := app.Add(component("http", &log)); !errors.Is(err, lifecycle.LifecycleRunning) {
		t.Fatalf("an Add mid-flight returned %v, want LifecycleRunning", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestTheZeroRunConfigWiresNothing. Signals and sd_notify are process-wide,
// observable side effects; a consumer who did not ask for them must not get
// them by importing this package. The zero value reduces Run to
// start-wait-on-ctx-stop, which is what this asserts.
//
// Goroutine lifecycle: one goroutine carrying Run, ended by cancelling ctx.
// Its result channel is buffered, so it never parks on a receiver that has
// gone away even if the test fails before receiving.
func TestTheZeroRunConfigWiresNothing(t *testing.T) {
	t.Parallel()
	var log []string
	up := make(chan struct{})
	app := lifecycle.New(lifecycle.Config{})
	gate := component("db", &log)
	inner := gate.Start
	gate.Start = func(ctx context.Context) error {
		err := inner(ctx)
		close(up)
		return err
	}
	if err := app.Add(gate); err != nil {
		t.Fatalf("Add(db): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	returned := make(chan error, 1)
	go func() { returned <- lifecycle.Run(ctx, app, lifecycle.RunConfig{}) }()
	<-up
	cancel()
	if err := <-returned; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(log) != 2 || log[0] != "start:db" || log[1] != "stop:db" {
		t.Fatalf("call order = %v, want [start:db stop:db]", log)
	}
}

// TestDefaultStopTimeoutIsPublishedAndPositive. The clamp is documented in the
// package comment, so the number a reader is pointed at has to exist and be
// usable — a consumer sizing a container's termination grace period reads it
// from here rather than copying a literal out of the prose.
func TestDefaultStopTimeoutIsPublishedAndPositive(t *testing.T) {
	t.Parallel()
	if lifecycle.DefaultStopTimeout <= 0 {
		t.Fatalf("DefaultStopTimeout = %v, want a positive budget", lifecycle.DefaultStopTimeout)
	}
	if lifecycle.PhaseStart.String() != "start" || lifecycle.PhaseStop.String() != "stop" {
		t.Fatalf("the Phase constants did not survive the alias")
	}
}
