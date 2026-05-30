package crypto

import (
	"testing"
)

// stubPasswordHasher is a comparable in-package PasswordHasher for white-box
// registry tests.
type stubPasswordHasher struct{ name Algorithm }

// : compile-time proof the stub satisfies the PasswordHasher port.
var _ PasswordHasher = (*stubPasswordHasher)(nil)

func (s stubPasswordHasher) Algorithm() Algorithm { return s.name }

func (stubPasswordHasher) Hash(_ []byte) (phc string, err error) { return "", nil }

func (stubPasswordHasher) Verify(_ []byte, _ string) (ok bool, err error) { return false, nil }

func (stubPasswordHasher) NeedsRehash(_ string) (stale bool) { return false }

func Test_clonePasswordHasherMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]PasswordHasher
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]PasswordHasher{"x": stubPasswordHasher{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]PasswordHasher
			if c.src != nil {
				srcPtr = &c.src
			}
			got := clonePasswordHasherMap(srcPtr, c.insert, stubPasswordHasher{c.insert})
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

func Test_publishPasswordHasher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "pubp-free", false},
		{"idempotent re-publish of the same hasher is a no-op", "pubp-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishPasswordHasher(c.algo, stubPasswordHasher{c.algo}); err != nil {
				t.Fatalf("first publishPasswordHasher(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME hasher value is an idempotent no-op, no error.
			if err := publishPasswordHasher(c.algo, stubPasswordHasher{c.algo}); err != nil {
				t.Errorf("idempotent re-publishPasswordHasher(%q): %v", c.algo, err)
			}
		})
	}
}

func Test_phcID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		phc    string
		wantID string
		wantOK bool
	}{
		{"well-formed PHC yields its id", "$pbkdf2-sha256$i=1$s$d", "pbkdf2-sha256", true},
		{"id with an empty trailing field still parses", "$id$", "id", true},
		{"no leading dollar is not a PHC", "pbkdf2-sha256$x", "", false},
		{"empty id segment is rejected", "$$rest", "", false},
		{"a bare dollar is rejected", "$", "", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			id, ok := phcID(c.phc)
			//: id + ok must both match the expected parse outcome.
			if id != c.wantID || ok != c.wantOK {
				t.Errorf("phcID(%q)=(%q,%v) want (%q,%v)", c.phc, id, ok, c.wantID, c.wantOK)
			}
		})
	}
}
