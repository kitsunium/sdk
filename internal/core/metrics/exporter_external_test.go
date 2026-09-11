// Package metrics_test — black-box tests for the Exporter contract and the
// process-wide exporter registry.
//
// These tests are NOT parallel with each other: the registry is process-wide by
// design (backends register at import), so AvailableExporters observes every
// name any test has published. Running them serially keeps that stable.
package metrics_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : both stubs must satisfy Exporter at compile time, exactly as a real backend
// : does when it binds its singleton.
var (
	_ metrics.Exporter = (*stubExporter)(nil)
	_ metrics.Exporter = (*stubExporter2)(nil)
)

// stubExporter is a comparable in-test Exporter for registry behaviour checks.
type stubExporter struct {
	name metrics.ExporterName
	//: a non-nil failure makes Export report rather than ship.
	failure error
}

// Name keys the stub in the registry.
func (s stubExporter) Name() metrics.ExporterName { return s.name }

// Export reports whatever failure the case configured.
func (s stubExporter) Export(metrics.SnapshotValue) error { return s.failure }

// stubExporter2 is a distinct comparable type so the dup check sees a conflict.
type stubExporter2 struct{ name metrics.ExporterName }

// Name keys the second stub.
func (s stubExporter2) Name() metrics.ExporterName { return s.name }

// Export ships nothing and succeeds.
func (stubExporter2) Export(metrics.SnapshotValue) error { return nil }

// Test_ExporterName_String pins that a name renders as itself, so an operator
// reading a log line sees the key they configured.
func Test_ExporterName_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  metrics.ExporterName
	}
	tests := []tc{
		{"an ordinary backend", "prometheus"},
		{"the reserved empty name", ""},
		{"a name with a dash", "otlp-http"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.key.String(); got != string(c.key) {
			t.Errorf("ExporterName(%q).String() = %q, want %q", string(c.key), got, string(c.key))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestRegisterExporter pins the boot-time contract: the exporter comes back for
// singleton binding, republishing the identical value is a no-op, and a real
// conflict panics at import rather than at the first Export in production.
func TestRegisterExporter(t *testing.T) {
	type tc struct {
		name      string
		register  func()
		wantPanic bool
	}
	shared := stubExporter{name: "exp-idempotent"}
	tests := []tc{
		{
			name:     "a fresh name",
			register: func() { metrics.RegisterExporter(stubExporter{name: "exp-fresh"}) },
		},
		{
			//: re-publishing the identical value is how a backend imported
			//: through two paths must behave.
			name: "the same exporter twice",
			register: func() {
				metrics.RegisterExporter(shared)
				metrics.RegisterExporter(shared)
			},
		},
		{
			name: "a distinct exporter on a taken name",
			register: func() {
				metrics.RegisterExporter(stubExporter{name: "exp-conflict"})
				metrics.RegisterExporter(stubExporter2{name: "exp-conflict"})
			},
			wantPanic: true,
		},
		{
			name:      "a nil exporter",
			register:  func() { metrics.RegisterExporter(nil) },
			wantPanic: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if c.wantPanic && r == nil {
				t.Errorf("%s did not panic", c.name)
			}
			if !c.wantPanic && r != nil {
				t.Errorf("%s panicked: %v", c.name, r)
			}
		}()
		c.register()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: RegisterExporter hands the exporter back for singleton binding.
	got := metrics.RegisterExporter(stubExporter{name: "exp-returned"})
	if got.Name() != "exp-returned" {
		t.Errorf("RegisterExporter returned name %q, want exp-returned", got.Name())
	}
}

// TestLookupExporter pins that resolution agrees with registration, and that a
// miss hands back nothing at all — a caller checking only the value would
// otherwise call Export on a nil interface.
func TestLookupExporter(t *testing.T) {
	metrics.RegisterExporter(stubExporter{name: "lookup-exp"})

	type tc struct {
		name   string
		key    metrics.ExporterName
		wantOK bool
	}
	tests := []tc{
		{"a registered name", "lookup-exp", true},
		{"a name nobody registered", "lookup-absent", false},
		{"the empty name", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		e, ok := metrics.LookupExporter(c.key)
		if ok != c.wantOK {
			t.Fatalf("LookupExporter(%q) ok = %v, want %v", c.key, ok, c.wantOK)
		}
		if !ok {
			if e != nil {
				t.Errorf("LookupExporter(%q) missed but returned %v", c.key, e)
			}
			return
		}
		if e.Name() != c.key {
			t.Errorf("LookupExporter(%q) returned name %q", c.key, e.Name())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestAvailableExporters pins that the listing is complete and sorted: it goes
// into help text and into test fixtures, so an unstable order is churn.
func TestAvailableExporters(t *testing.T) {
	type tc struct {
		name string
		key  metrics.ExporterName
	}
	tests := []tc{
		{"the first listed name", "avail-exp-a"},
		{"a name sorting after it", "avail-exp-b"},
		{"a name sorting between them", "avail-exp-ab"},
	}
	for _, c := range tests {
		metrics.RegisterExporter(stubExporter{name: c.key})
	}

	got := metrics.AvailableExporters()
	//: ascending order is the documented contract.
	if !slices.IsSorted(got) {
		t.Errorf("AvailableExporters() = %v, want it sorted", got)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !slices.Contains(got, c.key) {
			t.Errorf("AvailableExporters() = %v, missing %q", got, c.key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestExport pins the dispatch: an unregistered name is a typed miss, and a
// backend failure arrives wrapped under EXPORT_FAILED with the cause intact.
// Losing the cause would leave an operator with "the exporter failed" and no
// way to find out how.
func TestExport(t *testing.T) {
	backendErr := errors.New("the backend refused the batch")
	metrics.RegisterExporter(stubExporter{name: "export-ok"})
	metrics.RegisterExporter(stubExporter{name: "export-fails", failure: backendErr})

	type tc struct {
		name     string
		key      metrics.ExporterName
		snap     metrics.SnapshotValue
		wantCode errs.Code
		wantWrap error
	}
	tests := []tc{
		{name: "a registered exporter that ships", key: "export-ok"},
		{
			name: "a registered exporter carrying data",
			key:  "export-ok",
			snap: metrics.SnapshotValue{Counters: map[string][]metrics.CounterValue{
				"requests": {{Value: 3}},
			}},
		},
		{
			name:     "a name nobody registered",
			key:      "export-absent",
			wantCode: metrics.CodeUnknownExporter,
		},
		{
			name:     "the empty name",
			key:      "",
			wantCode: metrics.CodeUnknownExporter,
		},
		{
			name:     "a backend that reports a failure",
			key:      "export-fails",
			wantCode: metrics.CodeExportFailed,
			wantWrap: backendErr,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := metrics.Export(c.key, c.snap)
		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("Export(%q) = %v, want nil", c.key, err)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Export(%q) = %v, want code %v", c.key, err, c.wantCode)
		}
		//: the backend's own error must remain reachable through the wrap.
		if c.wantWrap != nil && !errors.Is(err, c.wantWrap) {
			t.Errorf("Export(%q) = %v, want it to wrap %v", c.key, err, c.wantWrap)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
