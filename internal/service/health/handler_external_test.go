// Package health_test — what leaves the process on the wire.
package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// serve runs one request against a handler and returns the recorder.
func serve(tb testing.TB, handler http.Handler, method string) *httptest.ResponseRecorder {
	tb.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, "/healthz", nil))
	return recorder
}

// TestTheBodyNeverCarriesInfrastructureDetail is the security property of this
// domain, and it has its own test because prose cannot enforce it.
//
// A probe endpoint is reachable by more than whoever added it — a mesh
// sidecar, a load balancer's health page, an uptime checker, occasionally the
// public internet. A driver's error names a host, a port, sometimes a query
// and occasionally a credential, and trimming one is a guess about where the
// secret is. So the SDK replaces it wholesale.
func TestTheBodyNeverCarriesInfrastructureDetail(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: failingWith(&calls, errDependency),
	})
	//: detail ON — the most permissive configuration the SDK offers is the
	//: one worth asserting.
	recorder := serve(t, svchealth.NewReadinessHandler(registry,
		svchealth.HandlerConfig{Detail: true}), http.MethodGet)
	body := recorder.Body.String()
	for _, secret := range []string{"10.0.3.14", "5432", "dial tcp", "connection refused"} {
		if strings.Contains(body, secret) {
			t.Errorf("the probe body leaks %q:\n%s", secret, body)
		}
	}
	//: and it still says something useful — a body with no reason at all is
	//: the least actionable thing a probe can return.
	if !strings.Contains(body, svchealth.CheckFailed.Public()) {
		t.Errorf("the body carries no reason at all:\n%s", body)
	}
}

// TestACallersOwnPublicHalfIsWhatAStrangerReads pins the other side of the
// same rule. A caller who already writes typed errors keeps their own
// wire-safe message — that is the whole reason the errs public/private split
// is the template here rather than a blanket redaction.
func TestACallersOwnPublicHalfIsWhatAStrangerReads(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	//: an application error in the SDK's own model: a Public written to be
	//: read by a stranger, and a Private that must never leave the process.
	appErr := kerrs.NewRuntime(kerrs.Pack(0x40, 1, 1, 1), "PRIMARY_DOWN",
		"The primary datastore is not accepting connections",
		"pg://writer.internal:5432 refused; failover has not promoted a replica")
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: failingWith(&calls, appErr),
	})
	body := serve(t, svchealth.NewReadinessHandler(registry,
		svchealth.HandlerConfig{Detail: true}), http.MethodGet).Body.String()
	if !strings.Contains(body, appErr.Public()) {
		t.Errorf("the caller's own Public half did not reach the body:\n%s", body)
	}
	for _, secret := range []string{"writer.internal", "5432", "failover"} {
		if strings.Contains(body, secret) {
			t.Errorf("the body leaks the Private half (%q):\n%s", secret, body)
		}
	}
}

// TestTheDefaultBodyIsTheAggregateAlone pins that detail is opt-in. The
// default reader of a probe body is an orchestrator that only looks at the
// status code; the default REACHER of the endpoint is more or less anyone on
// the network.
func TestTheDefaultBodyIsTheAggregateAlone(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "internal-billing-primary", Check: failingWith(&calls, errDependency),
	})
	body := serve(t, svchealth.NewReadinessHandler(registry,
		svchealth.HandlerConfig{}), http.MethodGet).Body.String()
	if body != `{"status":"unhealthy"}` {
		t.Errorf("the default body is %s, want the aggregate alone", body)
	}
	//: even a check NAME is the caller's vocabulary about their own
	//: infrastructure, and it is not published unless they ask for it.
	if strings.Contains(body, "internal-billing-primary") {
		t.Errorf("the default body names a check:\n%s", body)
	}
}

// TestStatusCodesFollowServingNotHealth pins the mapping an orchestrator
// actually reads — including the one that makes NonCritical mean something.
func TestStatusCodesFollowServingNotHealth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		nonCritical bool
		fail        bool
		want        int
	}
	tests := []tc{
		{"healthy serves", false, false, http.StatusOK},
		{"degraded serves too", true, true, http.StatusOK},
		{"unhealthy does not", false, true, http.StatusServiceUnavailable},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			registry, _ := newRegistry(t, svchealth.Config{})
			var calls atomic.Int64
			check := passing(&calls)
			if c.fail {
				check = failingWith(&calls, errDependency)
			}
			mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
				Name: "cache", Check: check, NonCritical: c.nonCritical,
			})
			got := serve(t, svchealth.NewReadinessHandler(registry, svchealth.HandlerConfig{}),
				http.MethodGet).Code
			if got != c.want {
				t.Errorf("status = %d, want %d", got, c.want)
			}
		})
	}
}

// TestTheResponseIsNotCacheable pins the headers. An intermediary replaying
// yesterday's 200 is indistinguishable from a healthy replica, which turns the
// endpoint into a source of the exact wrong answer.
func TestTheResponseIsNotCacheable(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	recorder := serve(t, svchealth.NewLivenessHandler(registry, svchealth.HandlerConfig{}),
		http.MethodGet)
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

// TestOnlyReadsAreAnswered pins that a probe endpoint is a read. Anything else
// is a mistake or somebody exploring, and both get the same flat answer.
func TestOnlyReadsAreAnswered(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	handler := svchealth.NewLivenessHandler(registry, svchealth.HandlerConfig{})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if got := serve(t, handler, method).Code; got != http.StatusOK {
			t.Errorf("%s = %d, want 200", method, got)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		recorder := serve(t, handler, method)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s Allow = %q, want \"GET, HEAD\"", method, got)
		}
	}
}

// TestEachHandlerServesItsOwnProbe pins why there are three constructors
// rather than one that reads the probe out of the URL: a routing mistake must
// not be able to serve liveness on the readiness path.
//
// The registry is drained, which is exactly the state where the two answers
// differ and where getting them the wrong way round is most expensive — a
// replica killed instead of withdrawn.
func TestEachHandlerServesItsOwnProbe(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	registry.Drain()
	if got := serve(t, svchealth.NewReadinessHandler(registry, svchealth.HandlerConfig{}),
		http.MethodGet).Code; got != http.StatusServiceUnavailable {
		t.Errorf("readiness while draining = %d, want 503", got)
	}
	if got := serve(t, svchealth.NewLivenessHandler(registry, svchealth.HandlerConfig{}),
		http.MethodGet).Code; got != http.StatusOK {
		t.Errorf("liveness while draining = %d, want 200 — a draining replica is "+
			"withdrawn, never killed", got)
	}
}

// TestTheHandlerReportsAgeSoAReplayIsDated pins that a cached answer arrives
// as a dated statement rather than as a current one. Serving a five-minute-old
// measurement without saying so is the failure mode a cache introduces; saying
// so is what keeps it honest.
func TestTheHandlerReportsAgeSoAReplayIsDated(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "expensive", Check: passing(&calls), MaxAge: svchealth.MaxCacheAge,
	})
	registry.Probe(context.Background(), corehealth.ProbeReadiness)
	clk.Advance(svchealth.MaxCacheAge / 2)
	body := serve(t, svchealth.NewReadinessHandler(registry,
		svchealth.HandlerConfig{Detail: true}), http.MethodGet).Body.String()
	if !strings.Contains(body, `"cached":true`) {
		t.Errorf("a replayed answer is not marked cached:\n%s", body)
	}
	if !strings.Contains(body, `"ageMs":15000`) {
		t.Errorf("a replayed answer carries no age:\n%s", body)
	}
}
