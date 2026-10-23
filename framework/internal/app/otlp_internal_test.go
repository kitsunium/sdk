package app

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// withoutOTLP runs the test as a program that does not import
// framework/app/otlp: the hook its package sets is nil, as the core sees it
// in such a program; the test binary plugs the subsystem in
// (subsystems_test.go), and the test takes it out until it ends.
func withoutOTLP(t *testing.T) {
	t.Helper()
	saved := plug.OTLP.Load()
	plug.OTLP.Store(nil)
	t.Cleanup(func() { plug.OTLP.Store(saved) })
}

// otlpProduct declares an export, its endpoint on the loopback, in a
// daemon.
func otlpProduct() *App {
	return NewApp("shop", NewService("shop", "Sells.")).With(Profile("daemon"), InMemory(), Logs(io.Discard),
		OTLP(OTLPEndpoint("http://127.0.0.1:4318")))
}

// A product that declares an export without importing framework/app/otlp is
// refused at the start (ADR 0179 §6): the export, at its position, naming
// the package, with CodeSubsystemMissing. With the subsystem plugged in —
// framework/app/otlp's, as the test binary has it —, the same product
// starts, its export a component. Its open, which the start's check makes
// unreachable without the subsystem, refuses with the import it needs.
func TestAnOTLPExportWithoutItsSubsystemIsRefused(t *testing.T) {
	t.Run("with the subsystem", func(t *testing.T) {
		a := otlpProduct()
		must(t, a.Start(context.Background()))
		defer func() { must(t, a.Stop(context.Background())) }()
		if a.otlpRun.Load() == nil {
			t.Error("the export does not run")
		}
	})
	t.Run("without the subsystem", func(t *testing.T) {
		withoutOTLP(t)
		err := otlpProduct().Start(context.Background())
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			t.Fatalf("the start: %v", err)
		}
		var refused []string
		for _, d := range de.Diagnostics {
			if strings.Contains(d.Message, otlpPackage) {
				refused = append(refused, d.Node)
				if d.Source == nil || d.Source.Line == 0 || !strings.HasSuffix(d.Source.File, "otlp_internal_test.go") {
					t.Errorf("the refusal names no declaration at its position: %+v", d)
				}
			}
		}
		if !slices.Equal(refused, []string{otlpNode}) {
			t.Errorf("refused for want of %s: %q\n%v", otlpPackage, refused, de)
		}
		if !slices.Equal(de.Missing, []string{otlpPackage}) || !errs.HasCode(err, CodeSubsystemMissing) {
			t.Errorf("the refusal: Missing %q, %v", de.Missing, err)
		}
		a := otlpProduct()
		must(t, a.resolve())
		if err := a.startOTLP(context.Background()); !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), otlpPackage) {
			t.Errorf("an export started without framework/app/otlp: %v", err)
		}
	})
}

// An export with no endpoint and no sender of the product's is off: it
// opens nothing, and the app runs as one without it; the tracer of an app
// that declares no export records nothing.
func TestAnOTLPExportWithoutAnEndpointIsOff(t *testing.T) {
	a := NewApp("shop", NewService("shop", "Sells.")).With(Profile("daemon"), InMemory(), Logs(io.Discard), OTLP())
	must(t, a.Start(context.Background()))
	defer func() { must(t, a.Stop(context.Background())) }()
	if a.otlpRun.Load() != nil {
		t.Error("an export with no endpoint runs")
	}
	if NewApp("plain").otlpSink() != nil {
		t.Error("an app that declares no export gives its tracer a sink")
	}
}

// The app's per-component stop budget covers the export's stop — its two
// flushes, its drain and its acknowledgement's settle, and the margin —, ten
// seconds at least.
func TestTheStopBudgetCoversTheOTLPExport(t *testing.T) {
	a := NewApp("shop", NewService("shop", "Sells.")).With(Profile("daemon"), InMemory(), Logs(io.Discard), Clock(manualAt()),
		OTLP(), Set("otlp-drain", 7*time.Second), Set("otlp-settle", 2*time.Second))
	must(t, a.Start(context.Background()))
	defer func() { must(t, a.Stop(context.Background())) }()
	if got := a.componentStopBudget(); got != 14*time.Second {
		t.Errorf("a drain of 7s and a settle of 2s: a budget of %v, want 14s", got)
	}
}

// The secret otlp-headers is read as OTEL_EXPORTER_OTLP_HEADERS spells it:
// key=value pairs joined by commas, each value percent-decoded; anything
// else is refused.
func TestTheOTLPHeadersAreParsed(t *testing.T) {
	got, ok := parseOTLPHeaders(" Authorization=Bearer%20abc , x-tenant=a=b,")
	if !ok || got["Authorization"] != "Bearer abc" || got["x-tenant"] != "a=b" || len(got) != 2 {
		t.Errorf("parsed %v, %v", got, ok)
	}
	for _, bad := range []string{"novalue", "=x", "k=%zz"} {
		if _, ok := parseOTLPHeaders(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// otlpRetryable classifies a failure of the product's own sender: a refusal
// the caller must fix is permanent, a timeout, a throttle, a failure of the
// server and anything unknown are worth another attempt.
func TestTheProductsSenderFailuresAreClassified(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{NewError(400, WireInvalid, "bad"), false},
		{NewError(404, WireNotFound, "gone"), false},
		{NewError(408, WireTimeout, "slow"), true},
		{NewError(429, "rate_limited", "later"), true},
		{NewError(503, WireUnavailable, "down"), true},
		{errors.New("broken pipe"), true},
		{context.Canceled, true},
	} {
		if got := otlpRetryable(tc.err); got != tc.want {
			t.Errorf("otlpRetryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// failingExport is an export whose loop ends at once with a storage
// failure, as one whose queue's directory went away.
type failingExport struct{}

// SpanSink is no sink.
func (failingExport) SpanSink() plug.OTLPSinkValue { return plug.OTLPSinkValue{} }

// Meter is no meter.
func (failingExport) Meter() plug.OTLPMeterValue { return plug.OTLPMeterValue{} }

// Record records nothing.
func (failingExport) Record(string, string, time.Duration, bool) {}

// Run fails at once.
func (failingExport) Run(context.Context) error { return errors.New("the queue's directory is gone") }

// Stats are none.
func (failingExport) Stats() []plug.OTLPStatsValue { return nil }

// An export whose loop ended while the app runs — its queue's storage
// failed — degrades the readiness, and keeps it degraded: nothing is sent
// until the next start.
func TestAnOTLPExportWhoseLoopEndedDegradesTheReadiness(t *testing.T) {
	saved := plug.OTLP.Load()
	plug.OTLP.Store(&plug.OTLPDriverConfig{Open: func(context.Context, *plug.OTLPConfig) (plug.OTLPExporter, error) {
		return failingExport{}, nil
	}})
	t.Cleanup(func() { plug.OTLP.Store(saved) })
	a := otlpProduct()
	must(t, a.Start(context.Background()))
	defer func() { must(t, a.Stop(context.Background())) }()
	r := a.otlpRun.Load()
	if r == nil {
		t.Fatal("the export does not run")
	}
	<-r.done
	for range 2 {
		if err := a.otlpReady(context.Background()); err == nil || !strings.Contains(err.Error(), "stopped sending until the next start") {
			t.Errorf("the readiness of an export whose loop ended: %v", err)
		}
	}
	// An export that hands no meter leaves the product's own meter one that
	// keeps nothing.
	if _, discards := Meter(withNode(context.Background(), "shop", a)).(discardMeter); !discards {
		t.Error("an export with no meter hands the product a meter that keeps something")
	}
}

// An export whose queues otlp-database places on a database needs the
// queue subsystem, whose SQL broker keeps them: without framework/app/queue
// the start is refused with CodeSubsystemMissing, naming the import — the
// database's start, whose migrations make the export's tables, first —; the
// same app with the subsystem starts, its queues on the database.
func TestAnOTLPExportOnADatabaseNeedsTheQueueSubsystem(t *testing.T) {
	t.Setenv("SHOP_SECRETS", "memory")
	t.Setenv("SHOP_SMTP_URL", "")
	// Built, never written out: a URL holding its credentials is what a
	// scanner looks for.
	t.Setenv("SHOP_DATABASE_URL", (&url.URL{Scheme: "fake", User: url.UserPassword("shop", "pw"), Host: "db.internal:5432", Path: "/shop", RawQuery: "tls=verify-full"}).String())
	product := func() *App {
		return NewApp("shop", NewService("shop", "Sells.")).With(Profile("daemon"), Env(EnvProduction), Logs(io.Discard),
			Database("database", NewFakeDB(sql.DialectPostgres).Engine()),
			OTLP(OTLPSender(func(context.Context, OTLPBatchValue) error { return nil }), OTLPDatabase("database")))
	}
	t.Run("with the subsystem", func(t *testing.T) {
		a := product()
		must(t, a.Start(context.Background()))
		defer func() { must(t, a.Stop(context.Background())) }()
		r := a.otlpRun.Load()
		if r == nil {
			t.Fatal("the export does not run")
		}
		for _, st := range r.ex.Stats() {
			if !st.Durable {
				t.Errorf("the %s queue on the database is not durable", st.Signal)
			}
		}
	})
	t.Run("without the subsystem", func(t *testing.T) {
		withoutQueues(t)
		err := product().Start(context.Background())
		if !errs.HasCode(err, CodeSubsystemMissing) || !strings.Contains(errs.PrivateOf(err), queuePackage) {
			t.Errorf("an export on a database without framework/app/queue: %v", err)
		}
		a := product()
		must(t, a.resolve())
		cfg := &plug.OTLPConfig{}
		if err := a.otlpOnDatabase(context.Background(), cfg); !errs.HasCode(err, CodeSubsystemMissing) || cfg.Database != nil {
			t.Errorf("the export's placement without framework/app/queue: %v, %+v", err, cfg)
		}
	})
}

// otlpRootSampler keeps every trace at 1 — no sampler, the SDK's own —,
// none at 0, and between the share it names of the traces' identifiers,
// deciding alike for the same identifier: a seeded draw of 4000 identifiers
// keeps 50% ± 5 at 0.5 and 10% ± 3 at 0.1.
func TestTheOTLPRootSamplerKeepsItsRatio(t *testing.T) {
	if otlpRootSampler(1) != nil {
		t.Error("a ratio of 1 samples")
	}
	ids := make([]trace.TraceID, 4000)
	rng := rand.New(rand.NewPCG(538, 1))
	for i := range ids {
		binary.BigEndian.PutUint64(ids[i][:8], rng.Uint64())
		binary.BigEndian.PutUint64(ids[i][8:], rng.Uint64())
	}
	for _, tc := range []struct {
		ratio    float64
		min, max int
	}{{0, 0, 0}, {0.1, 280, 520}, {0.5, 1800, 2200}} {
		s := otlpRootSampler(tc.ratio)
		kept := 0
		for _, id := range ids {
			p := trace.SamplingParams{TraceID: id}
			if s(p) {
				kept++
			}
			if s(p) != s(p) {
				t.Fatalf("ratio %v decides twice differently on %v", tc.ratio, id)
			}
		}
		if kept < tc.min || kept > tc.max {
			t.Errorf("ratio %v kept %d of %d, want %d to %d", tc.ratio, kept, len(ids), tc.min, tc.max)
		}
	}
}

// The tracer's sampler is the SDK's default for an app that declares no
// export; for one that does, it keeps a root while no export runs and
// decides as the running export's ratio says once one does.
func TestTheTracersSamplerFollowsTheRunningExport(t *testing.T) {
	if NewApp("plain").otlpSampler() != nil {
		t.Error("an app that declares no export gives its tracer a sampler")
	}
	a := otlpProduct()
	root := trace.SamplingParams{TraceID: trace.TraceID{1}}
	if !a.otlpSampler()(root) {
		t.Error("a root is not kept while no export runs")
	}
	a.otlpRun.Store(&otlpRunning{sample: otlpRootSampler(0)})
	if a.otlpSampler()(root) {
		t.Error("a root is kept by an export whose ratio is 0")
	}
}

// Outside an app, the product's meter keeps nothing — every instrument it
// makes is usable, and a collection is empty.
func TestTheProductsMeterOutsideAnExportKeepsNothing(t *testing.T) {
	m := Meter(context.Background())
	m.Counter("orders").Inc()
	m.Counter("orders").Add(2)
	m.UpDownCounter("carts").Dec()
	m.Gauge("stock").Set(3)
	m.Gauge("stock").Add(1)
	m.Histogram("latency", []float64{1}).Record(0.5)
	m.Histogram("latency", []float64{1}).RecordDuration(time.Second)
	m.ObservableCounter("seen", func(metrics.ObserveInt64) { t.Error("an observable counter was called") })
	m.ObservableUpDownCounter("open", func(metrics.ObserveInt64) { t.Error("an observable up-down counter was called") })
	m.ObservableGauge("level", func(metrics.ObserveFloat64) { t.Error("an observable gauge was called") })
	if snap := m.Collect(); len(snap.Sums)+len(snap.Gauges)+len(snap.Histograms) != 0 {
		t.Errorf("a collection outside an export: %+v", snap)
	}
}
