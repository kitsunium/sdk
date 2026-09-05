// Package metrics — white-box tests for the exporter registry's publish path.
// Both helpers below are reachable only from inside the package, and both
// encode decisions that RegisterExporter's panic-on-conflict surface cannot
// distinguish from the outside.
package metrics

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : the stub must satisfy Exporter at compile time, exactly as a real backend
// : does when it binds its singleton.
var _ Exporter = (*fakeExporter)(nil)

// fakeExporter is a comparable in-package Exporter for publish-path checks.
type fakeExporter struct {
	name ExporterName
	tag  int
}

// Name keys the fake in the registry.
func (f fakeExporter) Name() ExporterName { return f.name }

// Export ships nothing and succeeds.
func (fakeExporter) Export(SnapshotValue) error { return nil }

// Test_publishExporter pins the three outcomes RegisterExporter collapses into
// one panic: a clean publish, an idempotent republish of the identical value,
// and a conflict. Only the middle one is invisible from outside — a backend
// imported through two paths registers twice, and turning that into a boot
// panic would make the SDK unusable in a diamond dependency.
func Test_publishExporter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		first   Exporter
		second  Exporter
		key     ExporterName
		wantErr bool
	}
	tests := []tc{
		{
			name:  "a fresh name publishes",
			first: fakeExporter{name: "pubexp-fresh", tag: 1},
			key:   "pubexp-fresh",
		},
		{
			name:   "the identical exporter republishes as a no-op",
			first:  fakeExporter{name: "pubexp-same", tag: 1},
			second: fakeExporter{name: "pubexp-same", tag: 1},
			key:    "pubexp-same",
		},
		{
			name:    "a distinct exporter conflicts",
			first:   fakeExporter{name: "pubexp-conflict", tag: 1},
			second:  fakeExporter{name: "pubexp-conflict", tag: 2},
			key:     "pubexp-conflict",
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if err := publishExporter(c.first.Name(), c.first); err != nil {
			t.Fatalf("the first publish of %q = %v, want nil", c.key, err)
		}
		//: a single-publish case has nothing left to assert beyond resolution.
		if c.second != nil {
			err := publishExporter(c.second.Name(), c.second)
			if (err != nil) != c.wantErr {
				t.Fatalf("the second publish of %q = %v, want an error: %v", c.key, err, c.wantErr)
			}
			if c.wantErr && !errs.HasCode(err, CodeDuplicateRegistration) {
				t.Fatalf("the conflict on %q = %v, want DUPLICATE_REGISTRATION", c.key, err)
			}
		}
		//: whatever happened, the first registration must still resolve — a
		//: refused publish may not disturb what was already there.
		got, ok := LookupExporter(c.key)
		if !ok {
			t.Fatalf("LookupExporter(%q) missed after publishing", c.key)
		}
		if got != c.first {
			t.Errorf("LookupExporter(%q) = %v, want the first-registered %v", c.key, got, c.first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cloneExporterMap pins that publishing copies rather than mutates. The
// registry hands snapshots to lock-free readers, so a writer editing the live
// map in place would let a reader observe a half-built one.
func Test_cloneExporterMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		src     map[ExporterName]Exporter
		insert  ExporterName
		wantLen int
	}
	tests := []tc{
		{"a nil source starts a map of one", nil, "cloneexp-a", 1},
		{"an empty source starts a map of one", map[ExporterName]Exporter{}, "cloneexp-a", 1},
		{
			"an existing source grows by one",
			map[ExporterName]Exporter{"cloneexp-x": fakeExporter{name: "cloneexp-x"}},
			"cloneexp-a", 2,
		},
		{
			"inserting a present key does not grow it",
			map[ExporterName]Exporter{"cloneexp-a": fakeExporter{name: "cloneexp-a", tag: 1}},
			"cloneexp-a", 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		e := fakeExporter{name: c.insert, tag: 9}
		//: a nil map and a nil *map are different inputs; the helper takes the
		//: pointer form the snapshot hands it.
		var src *map[ExporterName]Exporter
		if c.src != nil {
			src = &c.src
		}
		before := len(c.src)

		next := cloneExporterMap(src, c.insert, e)

		if len(next) != c.wantLen {
			t.Fatalf("clone has %d entries, want %d", len(next), c.wantLen)
		}
		if next[c.insert] != e {
			t.Errorf("clone[%q] = %v, want the inserted exporter", c.insert, next[c.insert])
		}
		//: the source must be untouched — a reader may be walking it.
		if len(c.src) != before {
			t.Errorf("the source grew to %d entries, want %d", len(c.src), before)
		}
		for k, v := range c.src {
			//: every pre-existing entry must survive the copy unchanged, except
			//: the one deliberately overwritten.
			if k != c.insert && next[k] != v {
				t.Errorf("clone lost the entry %q", k)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
