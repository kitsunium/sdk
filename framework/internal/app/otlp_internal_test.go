package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
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
}
