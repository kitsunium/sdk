package crypto

import (
	"io"
	"testing"
)

// stubStreamSealer is a comparable in-package StreamSealer for white-box
// registry tests.
type stubStreamSealer struct{ name Algorithm }

// : compile-time proof the stub satisfies the StreamSealer port.
var _ StreamSealer = (*stubStreamSealer)(nil)

func (s stubStreamSealer) Algorithm() Algorithm { return s.name }

func (stubStreamSealer) Writer(_ Key, _ io.Writer, _ []byte) (io.WriteCloser, error) {
	return nil, nil
}

func (stubStreamSealer) Reader(_ Key, _ io.Reader, _ []byte) (io.Reader, error) { return nil, nil }

func Test_cloneStreamSealerMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]StreamSealer
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]StreamSealer{"x": stubStreamSealer{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]StreamSealer
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneStreamSealerMap(srcPtr, c.insert, stubStreamSealer{c.insert})
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

func Test_publishStreamSealer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubs-free", false},
		{"idempotent re-publish of the same sealer is a no-op", "pubs-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishStreamSealer(c.algo, stubStreamSealer{c.algo}); err != nil {
				t.Fatalf("first publishStreamSealer(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME sealer value is an idempotent no-op, no error.
			if err := publishStreamSealer(c.algo, stubStreamSealer{c.algo}); err != nil {
				t.Errorf("idempotent re-publishStreamSealer(%q): %v", c.algo, err)
			}
		})
	}
}
