package crypto

import (
	"crypto/sha256"
	"hash"
	"testing"
)

// stubHasher is a comparable in-package Hasher for white-box registry tests.
type stubHasher struct{ name Algorithm }

// : compile-time proof the stub satisfies the Hasher port.
var _ Hasher = (*stubHasher)(nil)

func (s stubHasher) Algorithm() Algorithm { return s.name }

func (stubHasher) New() hash.Hash { return sha256.New() }

func Test_cloneHasherMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]Hasher
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]Hasher{"x": stubHasher{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]Hasher
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneHasherMap(srcPtr, c.insert, stubHasher{c.insert})
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

func Test_publishHasher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubh-free", false},
		{"idempotent re-publish of the same hasher is a no-op", "pubh-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishHasher(c.algo, stubHasher{c.algo}); err != nil {
				t.Fatalf("first publishHasher(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME hasher value is an idempotent no-op, no error.
			if err := publishHasher(c.algo, stubHasher{c.algo}); err != nil {
				t.Errorf("idempotent re-publishHasher(%q): %v", c.algo, err)
			}
		})
	}
}
