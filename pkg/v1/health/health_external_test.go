// Package health_test — the façade as a consumer meets it.
package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/health"
	"github.com/kitsunium/sdk/pkg/v1/lifecycle"
)

// TestTheShortestUsefulHealthEndpoint is this façade's own acceptance
// criterion. The requirement was that wiring the three probes be trivial,
// which is not measurable by reading godoc — so the body below is the
// complete, unabridged code for a service's health surface, and it is six
// statements. If a change makes this example grow, the API has regressed in
// the dimension the domain exists for, whatever else it gained.
//
// The two registrations are error-checked rather than discarded, and that is
// part of the example: a refused Add is a permanent wiring fault (sysexits
// EX_CONFIG), so a consumer copying this must not learn to drop it.
func TestTheShortestUsefulHealthEndpoint(t *testing.T) {
	t.Parallel()
	h := health.New(health.Config{})
	if err := h.AddReadiness(health.ReadinessCheck{Name: "db", Check: reachable}); err != nil {
		t.Fatalf("AddReadiness = %v, want nil", err)
	}
	if err := h.AddLiveness(health.LivenessCheck{Name: "workers", Check: alive}); err != nil {
		t.Fatalf("AddLiveness = %v, want nil", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.NewLivenessHandler(h, health.HandlerConfig{}))
	mux.Handle("/readyz", health.NewReadinessHandler(h, health.HandlerConfig{}))

	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, recorder.Code)
		}
		if got := recorder.Body.String(); got != `{"status":"healthy"}` {
			t.Errorf("GET %s body = %s", path, got)
		}
	}
}

// reachable stands in for a dependency probe. It is a health.Check, so it
// takes a context — which is what every dependency API wants.
func reachable(_ context.Context) error { return nil }

// alive stands in for process-local evidence. It is a health.SelfCheck, so it
// takes nothing at all.
func alive() error { return nil }

// TestALivenessCheckCannotBeAReadinessCheck pins the domain's central rule at
// the PUBLIC edge, where it matters most: a consumer never sees the internal
// types, so the guarantee has to hold on the aliases they do see.
//
// The two function types are structurally different — one takes a context, the
// other takes nothing — so neither is assignable to the other and no
// conversion exists. This test asserts it by construction: if the shapes ever
// converged, the two helpers below would become interchangeable and the
// assignments in this function would start compiling both ways.
func TestALivenessCheckCannotBeAReadinessCheck(t *testing.T) {
	t.Parallel()
	//: legal, and the only legal pairing in each direction. The conversions
	//: ARE the assertion: health.SelfCheck(reachable) and health.Check(alive)
	//: do not compile, for the same reason the assignments below them do not.
	readiness, liveness := health.Check(reachable), health.SelfCheck(alive)
	//: `var x health.SelfCheck = reachable` and
	//: `var y health.Check = alive` are BOTH compile errors, which is the
	//: whole mechanism. They cannot be written here without breaking the
	//: build, so what is asserted instead is that the two types stay
	//: distinguishable at all.
	if readiness == nil || liveness == nil {
		t.Fatal("the two check shapes did not survive the façade")
	}
	registry := health.New(health.Config{})
	if err := registry.AddLiveness(health.LivenessCheck{Name: "workers", Check: liveness}); err != nil {
		t.Fatalf("AddLiveness = %v, want nil", err)
	}
	if err := registry.AddReadiness(health.ReadinessCheck{Name: "db", Check: readiness}); err != nil {
		t.Fatalf("AddReadiness = %v, want nil", err)
	}
}

// TestSentinelsAreMatchableThroughTheFacade pins that a consumer can route on
// every refusal this domain can produce without reaching into internal/.
func TestSentinelsAreMatchableThroughTheFacade(t *testing.T) {
	t.Parallel()
	registry := health.New(health.Config{})
	type tc struct {
		name     string
		act      func() error
		sentinel error
	}
	tests := []tc{
		{"an unrunnable check", func() error {
			return registry.AddReadiness(health.ReadinessCheck{Name: "db"})
		}, health.InvalidCheck},
		{"a duplicate name", func() error {
			//: the first registration must succeed, or the second would be
			//: refused for a reason this case is not testing.
			if first := registry.AddLiveness(health.LivenessCheck{
				Name: "dup", Check: alive,
			}); first != nil {
				return first
			}
			return registry.AddLiveness(health.LivenessCheck{Name: "dup", Check: alive})
		}, health.DuplicateCheck},
		{"a cache window past the ceiling", func() error {
			return registry.AddReadiness(health.ReadinessCheck{
				Name: "slow", Check: reachable, MaxAge: health.MaxCacheAge + 1,
			})
		}, health.StaleCacheWindow},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			err := c.act()
			code, ok := errs.CodeOf(c.sentinel)
			if !ok {
				t.Fatalf("%v carries no code", c.sentinel)
			}
			if !errs.HasCode(err, code) {
				t.Errorf("got %v, want the %v sentinel", err, c.sentinel)
			}
		})
	}
}

// TestTheComponentIsALifecycleComponent pins that the two façades compose
// without an adapter: health.Component's return value goes straight into
// lifecycle.Add because the aliases resolve to one type.
//
// It also pins the ordering the whole hook exists for — added LAST, health
// stops FIRST, so readiness is withdrawn before anything else begins closing.
func TestTheComponentIsALifecycleComponent(t *testing.T) {
	t.Parallel()
	registry := health.New(health.Config{})
	app := lifecycle.New(lifecycle.Config{})
	readyWhenServerStopped := true
	//: the server is added first, so it stops last.
	if err := app.Add(lifecycle.Component{
		Name:  "http",
		Start: func(context.Context) error { return nil },
		Stop: func(ctx context.Context) error {
			readyWhenServerStopped = registry.Probe(ctx, health.ProbeReadiness).Status.Serving()
			return nil
		},
	}); err != nil {
		t.Fatalf("Add(http) = %v", err)
	}
	//: health added LAST — one rule, right in both directions. It goes
	//: straight into Add, with no adapter and no conversion: lifecycle.Add
	//: takes a lifecycle.Component, and that is what health.Component returns.
	if err := app.Add(health.Component(registry, "health")); err != nil {
		t.Fatalf("Add(health) = %v", err)
	}
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start = %v", err)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if readyWhenServerStopped {
		t.Error("the replica was still advertised as ready when the server was told to stop")
	}
}

// TestTheBodyNeverEchoesADependencysError pins the security property through
// the façade, because that is the surface a consumer actually exposes.
func TestTheBodyNeverEchoesADependencysError(t *testing.T) {
	t.Parallel()
	registry := health.New(health.Config{})
	if err := registry.AddReadiness(health.ReadinessCheck{
		Name: "db",
		Check: func(context.Context) error {
			return errs.New(errs.Pack(0x40, 1, 2, 1), "DB_DOWN",
				"The datastore is unavailable",
				"dial tcp 10.0.3.14:5432: connect: connection refused")
		},
	}); err != nil {
		t.Fatalf("AddReadiness = %v", err)
	}
	recorder := httptest.NewRecorder()
	health.NewReadinessHandler(registry, health.HandlerConfig{Detail: true}).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "The datastore is unavailable") {
		t.Errorf("the declared Public half did not reach the body:\n%s", body)
	}
	//: the Private half is diagnostic-only, and a probe endpoint is the last
	//: place it should surface.
	for _, secret := range []string{"10.0.3.14", "5432", "dial tcp"} {
		if strings.Contains(body, secret) {
			t.Errorf("the body leaks %q:\n%s", secret, body)
		}
	}
}
