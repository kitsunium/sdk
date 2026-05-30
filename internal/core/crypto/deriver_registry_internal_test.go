package crypto

import (
	"testing"
)

// stubDeriver is a comparable in-package Deriver for white-box registry tests.
type stubDeriver struct{ name Algorithm }

// : compile-time proof the stub satisfies the Deriver port.
var _ Deriver = (*stubDeriver)(nil)

func (s stubDeriver) Algorithm() Algorithm { return s.name }

func (stubDeriver) Derive(_, _ []byte, _ string, _ int) (subkey []byte, err error) {
	return nil, nil
}

func Test_cloneDeriverMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]Deriver
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]Deriver{"x": stubDeriver{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]Deriver
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneDeriverMap(srcPtr, c.insert, stubDeriver{c.insert})
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

func Test_publishDeriver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubd-free", false},
		{"idempotent re-publish of the same deriver is a no-op", "pubd-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishDeriver(c.algo, stubDeriver{c.algo}); err != nil {
				t.Fatalf("first publishDeriver(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME deriver value is an idempotent no-op, no error.
			if err := publishDeriver(c.algo, stubDeriver{c.algo}); err != nil {
				t.Errorf("idempotent re-publishDeriver(%q): %v", c.algo, err)
			}
		})
	}
}
