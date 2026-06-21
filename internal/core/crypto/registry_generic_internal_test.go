package crypto

import "testing"

// stubScheme is a comparable value for white-box schemeRegistry tests. It does
// not implement any capability port — the generic only constrains V to
// comparable, so a bare struct exercises the publish/clone/lookup paths.
type stubScheme struct{ id Algorithm }

// Test_cloneSchemeMap proves the generic clone copies the source and inserts the
// new entry, on both a nil and a populated source.
func Test_cloneSchemeMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]stubScheme
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]stubScheme{"x": {"x"}}, "y", 2},
	}
	runCase := func(t *testing.T, c struct {
		name    string
		src     map[Algorithm]stubScheme
		insert  Algorithm
		wantLen int
	},
	) {
		t.Helper()
		//: a nil source must stay a nil pointer; a populated one is addressed.
		var srcPtr *map[Algorithm]stubScheme
		if c.src != nil {
			//: address the populated source so the clone copies it.
			srcPtr = &c.src
		}
		got := cloneSchemeMap(srcPtr, c.insert, stubScheme{c.insert})
		//: the clone carries every source entry plus the inserted one.
		if len(got) != c.wantLen {
			t.Errorf("len=%d want %d", len(got), c.wantLen)
		}
		//: the inserted entry must resolve in the freshly cloned map.
		if _, ok := got[c.insert]; !ok {
			t.Errorf("inserted %q missing from clone", c.insert)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// Test_schemeRegistry_publish proves the generic publish: first publish under a
// free name succeeds, idempotent re-publish of the SAME value is a no-op, and a
// DISTINCT value under a taken name is a hard conflict.
func Test_schemeRegistry_publish(t *testing.T) {
	t.Parallel()
	//: a fresh local registry isolates this test from the package globals.
	var r schemeRegistry[stubScheme]
	r.verb = "RegisterStub"
	//: the first publish of a free name must succeed.
	if err := r.publish("free", stubScheme{"free"}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	//: re-publishing the SAME value is an idempotent no-op (no error).
	if err := r.publish("free", stubScheme{"free"}); err != nil {
		t.Errorf("idempotent re-publish: %v", err)
	}
	//: a DISTINCT value under the taken name is a hard conflict.
	if err := r.publish("free", stubScheme{"OTHER"}); err == nil {
		t.Error("distinct value under taken name: want conflict error, got nil")
	}
	//: lookup resolves the registered value; available lists the name.
	if _, ok := r.lookup("free"); !ok {
		t.Error("lookup(free) missed after publish")
	}
	//: storing a nil snapshot clears the registry back to empty.
	r.store.Store(nil)
	if _, ok := r.lookup("free"); ok {
		t.Error("lookup(free) hit after reset")
	}
}
