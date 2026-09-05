// Package id — white-box tests for the registry's publish path. Both helpers
// below are reachable only from inside the package, and both encode decisions
// (idempotent republish, snapshot cloning) that Register's panic-on-conflict
// surface cannot distinguish from the outside.
package id

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : the registry stores plug-in instances behind the Generator interface, so
// : the stub must satisfy it at compile time like any real scheme would.
var _ Generator = (*fakeGen)(nil)

// fakeGen is a comparable in-package Generator for publish-path checks.
type fakeGen struct {
	name Scheme
	tag  int
}

// Scheme names the fake so the registry can key on it.
func (f fakeGen) Scheme() Scheme { return f.name }

// New returns a fixed value; the identifier is not what these tests pin.
func (fakeGen) New() (string, error) { return "fake", nil }

// Test_publishGenerator pins the three outcomes Register collapses into one
// panic: a clean publish, an idempotent republish of the identical value, and a
// conflict. Only the middle one is invisible from outside — a package imported
// through two paths registers twice, and turning that into a boot panic would
// make the SDK unusable in a diamond dependency.
func Test_publishGenerator(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		first   Generator
		second  Generator
		scheme  Scheme
		wantErr bool
	}
	tests := []tc{
		{
			name:   "a fresh scheme publishes",
			first:  fakeGen{name: "pub-fresh", tag: 1},
			scheme: "pub-fresh",
		},
		{
			name:   "the identical generator republishes as a no-op",
			first:  fakeGen{name: "pub-same", tag: 1},
			second: fakeGen{name: "pub-same", tag: 1},
			scheme: "pub-same",
		},
		{
			name:    "a distinct generator conflicts",
			first:   fakeGen{name: "pub-conflict", tag: 1},
			second:  fakeGen{name: "pub-conflict", tag: 2},
			scheme:  "pub-conflict",
			wantErr: true,
		},
		{
			//: the empty scheme is rejected at the boundary, before it can
			//: create an entry Known() would then deny.
			name:    "the reserved empty scheme is refused",
			first:   fakeGen{name: "", tag: 1},
			scheme:  "",
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := publishGenerator(c.first.Scheme(), c.first)
		//: a single-publish case decides here.
		if c.second == nil {
			if (err != nil) != c.wantErr {
				t.Fatalf("publishGenerator = %v, want an error: %v", err, c.wantErr)
			}
			if c.wantErr && !errs.HasCode(err, CodeDuplicateRegistration) {
				t.Fatalf("publishGenerator = %v, want DUPLICATE_REGISTRATION", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("the first publish of %q = %v, want nil", c.scheme, err)
		}

		err = publishGenerator(c.second.Scheme(), c.second)
		if (err != nil) != c.wantErr {
			t.Fatalf("the second publish of %q = %v, want an error: %v", c.scheme, err, c.wantErr)
		}
		//: whatever happened, the first registration must still resolve — a
		//: refused publish may not disturb what was already there.
		got, ok := Lookup(c.scheme)
		if !ok {
			t.Fatalf("Lookup(%q) missed after publishing", c.scheme)
		}
		if got != c.first {
			t.Errorf("Lookup(%q) = %v, want the first-registered %v", c.scheme, got, c.first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cloneGeneratorMap pins that publishing copies rather than mutates. The
// registry hands out snapshots to lock-free readers, so a writer that edited
// the live map in place would let a reader observe a half-built one.
func Test_cloneGeneratorMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		src     map[Scheme]Generator
		insert  Scheme
		wantLen int
	}
	tests := []tc{
		{"a nil source starts a map of one", nil, "clone-a", 1},
		{"an empty source starts a map of one", map[Scheme]Generator{}, "clone-a", 1},
		{
			"an existing source grows by one",
			map[Scheme]Generator{"clone-x": fakeGen{name: "clone-x"}},
			"clone-a", 2,
		},
		{
			"inserting a present key does not grow it",
			map[Scheme]Generator{"clone-a": fakeGen{name: "clone-a", tag: 1}},
			"clone-a", 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGen{name: c.insert, tag: 9}
		//: a nil map and a nil *map are different inputs; the helper takes the
		//: pointer form the snapshot hands it.
		var src *map[Scheme]Generator
		if c.src != nil {
			src = &c.src
		}
		before := len(c.src)

		next := cloneGeneratorMap(src, c.insert, g)

		if len(next) != c.wantLen {
			t.Fatalf("clone has %d entries, want %d", len(next), c.wantLen)
		}
		if next[c.insert] != g {
			t.Errorf("clone[%q] = %v, want the inserted generator", c.insert, next[c.insert])
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
