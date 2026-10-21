// Package metrics_test — ExportContext: the bound reaches an exporter that can
// honour it, and one that cannot is still called.
package metrics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : the recording stub is a ContextExporter, as a network exporter is.
var _ metrics.ContextExporter = (*contextExporter)(nil)

// The stubs are package-level and registered once each: the registry is
// process-wide, and a second -count run must find the SAME exporter under a
// name, not a distinct one it would refuse.
var (
	defaultBoundStub = new(contextExporter)
	callerBoundStub  = new(contextExporter)
	bareStub         = new(bareExporter)
	failingStub      = new(contextExporter)
)

// contextExporter records the context ExportContext received and how it was
// called. A pointer, so the registry can compare it.
type contextExporter struct {
	name    metrics.ExporterName
	failure error
	gotCtx  context.Context
	viaCtx  bool
	viaBare bool
}

// Name keys the stub in the registry.
func (c *contextExporter) Name() metrics.ExporterName { return c.name }

// Export records a bare call.
func (c *contextExporter) Export(metrics.SnapshotValue) error {
	c.viaBare = true
	return c.failure
}

// ExportContext records the context it was handed.
func (c *contextExporter) ExportContext(ctx context.Context, _ metrics.SnapshotValue) error {
	c.viaCtx, c.gotCtx = true, ctx
	return c.failure
}

// bareExporter implements only Exporter, as a plug-in written before the
// sibling existed does.
type bareExporter struct {
	name   metrics.ExporterName
	called bool
}

// Name keys the stub in the registry.
func (b *bareExporter) Name() metrics.ExporterName { return b.name }

// Export records the call.
func (b *bareExporter) Export(metrics.SnapshotValue) error {
	b.called = true
	return nil
}

// TestExportContextBoundsAContextWithoutADeadline pins the specification's
// "MUST NOT block indefinitely": a context with no deadline reaches the
// exporter carrying DefaultExportTimeout, and Export is not called.
func TestExportContextBoundsAContextWithoutADeadline(t *testing.T) {
	exporter := defaultBoundStub
	*exporter = contextExporter{name: "ctx-export-default-bound"}
	metrics.RegisterExporter(exporter)
	before := time.Now()
	if err := metrics.ExportContext(context.Background(), exporter.name, metrics.SnapshotValue{}); err != nil {
		t.Fatalf("ExportContext = %v, want nil", err)
	}
	if !exporter.viaCtx || exporter.viaBare {
		t.Fatalf("viaCtx=%v viaBare=%v, want the sibling and only it", exporter.viaCtx, exporter.viaBare)
	}
	after := time.Now()
	deadline, ok := exporter.gotCtx.Deadline()
	if !ok {
		t.Fatal("the exporter received a context with no deadline")
	}
	//: the deadline was set between the two readings, DefaultExportTimeout out.
	if deadline.Before(before.Add(metrics.DefaultExportTimeout)) || deadline.After(after.Add(metrics.DefaultExportTimeout)) {
		t.Fatalf("deadline %v is not DefaultExportTimeout (%v) after the call", deadline, metrics.DefaultExportTimeout)
	}
}

// TestExportContextKeepsTheCallersDeadline pins that a caller's own bound is
// passed through as given — never lengthened to the default.
func TestExportContextKeepsTheCallersDeadline(t *testing.T) {
	exporter := callerBoundStub
	*exporter = contextExporter{name: "ctx-export-caller-bound"}
	metrics.RegisterExporter(exporter)
	want := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()
	if err := metrics.ExportContext(ctx, exporter.name, metrics.SnapshotValue{}); err != nil {
		t.Fatalf("ExportContext = %v, want nil", err)
	}
	if got, ok := exporter.gotCtx.Deadline(); !ok || !got.Equal(want) {
		t.Fatalf("deadline = %v (%v), want the caller's %v", got, ok, want)
	}
}

// TestExportContextFallsBackToExport pins that an exporter without the
// sibling is still called, through Export.
func TestExportContextFallsBackToExport(t *testing.T) {
	exporter := bareStub
	*exporter = bareExporter{name: "ctx-export-bare"}
	metrics.RegisterExporter(exporter)
	if err := metrics.ExportContext(context.Background(), exporter.name, metrics.SnapshotValue{}); err != nil {
		t.Fatalf("ExportContext = %v, want nil", err)
	}
	if !exporter.called {
		t.Fatal("a Exporter without ExportContext was not called")
	}
}

// TestExportContextRefusals pins the two typed failures it shares with
// Export: an unknown name, and an exporter failure wrapped as ExportFailed
// with its cause still reachable.
func TestExportContextRefusals(t *testing.T) {
	cause := errors.New("collector down")
	failing := failingStub
	*failing = contextExporter{name: "ctx-export-fails", failure: cause}
	metrics.RegisterExporter(failing)
	tests := []struct {
		name     string
		exporter metrics.ExporterName
		code     errs.Code
		cause    error
	}{
		{"an unregistered name", "ctx-export-absent", metrics.CodeUnknownExporter, nil},
		{"a failing exporter", failing.name, metrics.CodeExportFailed, cause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := metrics.ExportContext(context.Background(), tt.exporter, metrics.SnapshotValue{})
			if !errs.HasCode(err, tt.code) {
				t.Fatalf("ExportContext = %v, want code %v", err, tt.code)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatalf("ExportContext = %v, want the cause on the trail", err)
			}
		})
	}
}
