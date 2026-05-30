package crypto

import (
	"hash"
	"hash/fnv"
	"testing"
)

// stubMAC is a comparable in-package MAC for white-box registry tests.
type stubMAC struct{ name Algorithm }

// : compile-time proof the stub satisfies the MAC port.
var _ MAC = (*stubMAC)(nil)

func (s stubMAC) Algorithm() Algorithm { return s.name }

func (stubMAC) Tag(_ Key, _ []byte) []byte { return nil }

func (stubMAC) Verify(_ Key, _, _ []byte) bool { return false }

func (stubMAC) New(_ Key) hash.Hash { return fnv.New64a() }

func Test_cloneMACMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]MAC
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]MAC{"x": stubMAC{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]MAC
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneMACMap(srcPtr, c.insert, stubMAC{c.insert})
			//: the clone carries every source entry plus the inserted one.
			if len(got) != c.wantLen {
				t.Errorf("len=%d want %d", len(got), c.wantLen)
			}
			//: the inserted entry must resolve in the freshly cloned map.
			if _, ok := got[c.insert]; !ok {
				t.Errorf("inserted %q missing from clone", c.insert)
			}
		})
	}
}

func Test_publishMAC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubm-free", false},
		{"idempotent re-publish of the same MAC is a no-op", "pubm-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishMAC(c.algo, stubMAC{c.algo}); err != nil {
				t.Fatalf("first publishMAC(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME MAC value is an idempotent no-op, no error.
			if err := publishMAC(c.algo, stubMAC{c.algo}); err != nil {
				t.Errorf("idempotent re-publishMAC(%q): %v", c.algo, err)
			}
		})
	}
}
