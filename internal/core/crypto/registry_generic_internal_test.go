// Package crypto — white-box tests for the shared capability registry. Every
// scheme registry (Hasher, Signer, MAC, Deriver, Agreement, PasswordHasher,
// StreamSealer) delegates its publish / clone / lookup / available logic here,
// so a bug in this file is a bug in all seven at once.
package crypto

import (
	"slices"
	"testing"
)

// stubScheme is a comparable value for white-box schemeRegistry tests. It does
// not implement any capability port — the generic only constrains V to
// comparable, so a bare struct exercises the publish/clone/lookup paths.
type stubScheme struct{ id Algorithm }

// Test_cloneSchemeMap proves the clone copies the source rather than mutating
// it. The registry hands snapshots to lock-free readers, so a writer editing
// the live map in place would let a reader observe a half-built one.
func Test_cloneSchemeMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		src     map[Algorithm]stubScheme
		insert  Algorithm
		wantLen int
	}
	tests := []tc{
		{"a nil source yields a singleton map", nil, "a", 1},
		{"an empty source yields a singleton map", map[Algorithm]stubScheme{}, "a", 1},
		{"a populated source is copied plus one", map[Algorithm]stubScheme{"x": {"x"}}, "y", 2},
		{"inserting a present key does not grow it", map[Algorithm]stubScheme{"x": {"x"}}, "x", 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a nil source must stay a nil pointer; a populated one is addressed.
		var srcPtr *map[Algorithm]stubScheme
		if c.src != nil {
			srcPtr = &c.src
		}
		before := len(c.src)

		got := cloneSchemeMap(srcPtr, c.insert, stubScheme{c.insert})

		//: the clone carries every source entry plus the inserted one.
		if len(got) != c.wantLen {
			t.Fatalf("clone has %d entries, want %d", len(got), c.wantLen)
		}
		if got[c.insert] != (stubScheme{c.insert}) {
			t.Errorf("clone[%q] = %v, want the inserted value", c.insert, got[c.insert])
		}
		//: the source must be untouched — a reader may be walking it.
		if len(c.src) != before {
			t.Errorf("the source grew to %d entries, want %d", len(c.src), before)
		}
		for k, v := range c.src {
			//: every pre-existing entry survives, except the one overwritten.
			if k != c.insert && got[k] != v {
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

// Test_schemeRegistry_publish proves the three outcomes every Register* verb
// collapses into one panic: a first publish under a free name succeeds, an
// idempotent re-publish of the SAME value is a no-op, and a DISTINCT value
// under a taken name is a hard conflict. Only the middle one is invisible from
// outside — a scheme package imported through two paths registers twice, and
// turning that into a boot panic would make the SDK unusable in a diamond
// dependency.
func Test_schemeRegistry_publish(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		first   stubScheme
		second  stubScheme
		publish int
		wantErr bool
	}
	tests := []tc{
		{"a free name", stubScheme{"free"}, stubScheme{}, 1, false},
		{"the same value twice", stubScheme{"same"}, stubScheme{"same"}, 2, false},
		{"a distinct value under a taken name", stubScheme{"taken"}, stubScheme{"OTHER"}, 2, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a fresh local registry isolates each case from the package globals.
		r := schemeRegistry[stubScheme]{verb: "RegisterStub"}
		name := c.first.id

		if err := r.publish(name, c.first); err != nil {
			t.Fatalf("the first publish of %q = %v, want nil", name, err)
		}
		if c.publish > 1 {
			err := r.publish(name, c.second)
			if (err != nil) != c.wantErr {
				t.Fatalf("the second publish of %q = %v, want an error: %v", name, err, c.wantErr)
			}
		}

		//: whatever happened, the first registration still resolves — a refused
		//: publish may not disturb what was already there.
		got, ok := r.lookup(name)
		if !ok {
			t.Fatalf("lookup(%q) missed after publishing", name)
		}
		if got != c.first {
			t.Errorf("lookup(%q) = %v, want the first-published %v", name, got, c.first)
		}
		if avail := r.available(); !slices.Contains(avail, name) {
			t.Errorf("available() = %v, missing %q", avail, name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_schemeRegistry_lookup pins the empty-registry path: before the first
// publish the snapshot pointer is nil, and both readers must answer from that
// rather than dereference it.
func Test_schemeRegistry_lookup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		prefill Algorithm
		query   Algorithm
		wantOK  bool
	}
	tests := []tc{
		{"an empty registry", "", "anything", false},
		{"a populated registry, wrong key", "present", "absent", false},
		{"a populated registry, right key", "present", "present", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := schemeRegistry[stubScheme]{verb: "RegisterStub"}
		if c.prefill != "" {
			if err := r.publish(c.prefill, stubScheme{c.prefill}); err != nil {
				t.Fatalf("publish(%q) = %v, want nil", c.prefill, err)
			}
		}

		got, ok := r.lookup(c.query)
		if ok != c.wantOK {
			t.Fatalf("lookup(%q) ok = %v, want %v", c.query, ok, c.wantOK)
		}
		//: a miss must hand back the zero value, never a stale one.
		if !ok && got != (stubScheme{}) {
			t.Errorf("lookup(%q) missed but returned %v", c.query, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_schemeRegistry_available pins that the listing is sorted and complete.
// It reaches operators through Available* help text, so an unstable order is
// churn they have to read past every time.
func Test_schemeRegistry_available(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		publish []Algorithm
		wantLen int
	}
	tests := []tc{
		{"nothing published", nil, 0},
		{"one algorithm", []Algorithm{"only"}, 1},
		{"several, published out of order", []Algorithm{"c", "a", "b"}, 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := schemeRegistry[stubScheme]{verb: "RegisterStub"}
		for _, alg := range c.publish {
			if err := r.publish(alg, stubScheme{alg}); err != nil {
				t.Fatalf("publish(%q) = %v, want nil", alg, err)
			}
		}

		got := r.available()
		if len(got) != c.wantLen {
			t.Fatalf("available() = %v, want %d entries", got, c.wantLen)
		}
		//: ascending order regardless of publish order.
		if !slices.IsSorted(got) {
			t.Errorf("available() = %v, want it sorted", got)
		}
		for _, alg := range c.publish {
			if !slices.Contains(got, alg) {
				t.Errorf("available() = %v, missing %q", got, alg)
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
