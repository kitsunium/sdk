package crypto

import (
	"testing"
)

// stubSigner is a comparable in-package Signer for white-box registry tests.
type stubSigner struct{ name Algorithm }

// : compile-time proof the stub satisfies the Signer port.
var _ Signer = (*stubSigner)(nil)

func (s stubSigner) Algorithm() Algorithm { return s.name }

func (stubSigner) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (stubSigner) Sign(_, _ []byte) (sig []byte, err error) { return nil, nil }

func (stubSigner) Verify(_, _, _ []byte) (ok bool) { return false }

func Test_cloneSignerMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]Signer
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]Signer{"x": stubSigner{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]Signer
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneSignerMap(srcPtr, c.insert, stubSigner{c.insert})
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

func Test_publishSigner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubs-free", false},
		{"idempotent re-publish of the same signer is a no-op", "pubs-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishSigner(c.algo, stubSigner{c.algo}); err != nil {
				t.Fatalf("first publishSigner(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME signer value is an idempotent no-op, no error.
			if err := publishSigner(c.algo, stubSigner{c.algo}); err != nil {
				t.Errorf("idempotent re-publishSigner(%q): %v", c.algo, err)
			}
		})
	}
}
