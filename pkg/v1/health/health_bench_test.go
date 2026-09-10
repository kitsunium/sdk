package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/health"
)

// sinks so no probe or response can be proven unused and elided.
var (
	reportSink health.Report
	statusSink health.Status
	codeSink   int
)

// benchRegistry builds a registry carrying n readiness checks and one liveness
// check, all passing. Registration happens outside every timed loop.
func benchRegistry(b *testing.B, n int) health.Health {
	b.Helper()
	registry := health.New(health.Config{})
	for i := range n {
		name := "dep-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := registry.AddReadiness(health.ReadinessCheck{
			Name:  name,
			Check: func(context.Context) error { return nil },
		}); err != nil {
			b.Fatalf("AddReadiness: %v", err)
		}
	}
	if err := registry.AddLiveness(health.LivenessCheck{
		Name:  "self",
		Check: func() error { return nil },
	}); err != nil {
		b.Fatalf("AddLiveness: %v", err)
	}
	return registry
}

// BenchmarkProbeReadiness_1 through _32 are the number an orchestrator polls.
// Kubernetes asks every few seconds per replica, so the absolute cost matters
// less than how it grows: a readiness probe runs EVERY registered check, and a
// service that adds a dependency adds it to every probe from then on.
func BenchmarkProbeReadiness_1(b *testing.B)  { benchProbe(b, 1, health.ProbeReadiness) }
func BenchmarkProbeReadiness_8(b *testing.B)  { benchProbe(b, 8, health.ProbeReadiness) }
func BenchmarkProbeReadiness_32(b *testing.B) { benchProbe(b, 32, health.ProbeReadiness) }

// BenchmarkProbeLiveness_32 is the contrast that carries ADR 0060's argument.
// A liveness check is a SelfCheck — func() error, no context — so it cannot
// reach a dependency, and the registry has only the one. The gap against
// readiness at the same registry size is what "liveness does not ping the
// database" costs, or rather saves.
func BenchmarkProbeLiveness_32(b *testing.B) { benchProbe(b, 32, health.ProbeLiveness) }

func benchProbe(b *testing.B, n int, probe health.Probe) {
	b.Helper()
	registry := benchRegistry(b, n)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reportSink = registry.Probe(ctx, probe)
	}
	statusSink = reportSink.Status
}

// BenchmarkHandler_Readiness_Terse and _Detail are the whole endpoint: probe,
// render, write. Detail decides whether the per-check body is computed at all,
// so the two rows price that switch rather than describe it.
func BenchmarkHandler_Readiness_Terse(b *testing.B)  { benchHandler(b, false) }
func BenchmarkHandler_Readiness_Detail(b *testing.B) { benchHandler(b, true) }

func benchHandler(b *testing.B, detail bool) {
	b.Helper()
	registry := benchRegistry(b, 8)
	handler := health.NewReadinessHandler(registry, health.HandlerConfig{Detail: detail})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		codeSink = recorder.Code
	}
	if codeSink != http.StatusOK {
		b.Fatalf("readiness handler answered %d", codeSink)
	}
}

// BenchmarkProbeReadiness_Cached measures the staleness bound doing its job: a
// check carrying MaxAge is not re-run inside its window, so a registry polled
// faster than its checks can answer must fall back to the cached verdict rather
// than stampeding the dependency.
func BenchmarkProbeReadiness_Cached(b *testing.B) {
	registry := health.New(health.Config{})
	for i := range 8 {
		name := "cached-" + string(rune('a'+i))
		if err := registry.AddReadiness(health.ReadinessCheck{
			Name:   name,
			Check:  func(context.Context) error { return nil },
			MaxAge: 10 * time.Second,
		}); err != nil {
			b.Fatalf("AddReadiness: %v", err)
		}
	}
	ctx := b.Context()
	//: prime the cache outside the timed loop, so every measured probe is a hit.
	reportSink = registry.Probe(ctx, health.ProbeReadiness)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reportSink = registry.Probe(ctx, health.ProbeReadiness)
	}
}

// BenchmarkWorst pins the status fold, which runs once per check per probe.
func BenchmarkWorst(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		statusSink = health.Worst(health.StatusHealthy, health.StatusDegraded)
	}
}
