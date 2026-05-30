package crypto

import "testing"

// stubAgreement is a comparable in-package Agreement for white-box registry tests.
type stubAgreement struct{ name Algorithm }

// : compile-time proof the stub satisfies the Agreement port.
var _ Agreement = (*stubAgreement)(nil)

func (s stubAgreement) Algorithm() Algorithm { return s.name }

func (stubAgreement) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (stubAgreement) Shared(_, _ []byte) (secret []byte, err error) { return nil, nil }

func Test_cloneAgreementMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]Agreement
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]Agreement{"x": stubAgreement{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]Agreement
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneAgreementMap(srcPtr, c.insert, stubAgreement{c.insert})
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

func Test_publishAgreement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "puba-free", false},
		{"idempotent re-publish of the same scheme is a no-op", "puba-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishAgreement(c.algo, stubAgreement{c.algo}); err != nil {
				t.Fatalf("first publishAgreement(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME scheme value is an idempotent no-op, no error.
			if err := publishAgreement(c.algo, stubAgreement{c.algo}); err != nil {
				t.Errorf("idempotent re-publishAgreement(%q): %v", c.algo, err)
			}
		})
	}
}
