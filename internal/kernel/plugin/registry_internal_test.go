// Package plugin — white-box test of the copy a publish makes. A Registry
// hands snapshots to lock-free readers, so a writer editing the live map in
// place would let a reader observe a half-built one; only the helper itself
// can show that the source is left alone.
package plugin

import "testing"

// Test_cloneWith pins that publishing copies rather than mutates: the copy
// carries every existing entry plus the new one, and the source a reader may
// still be walking is untouched.
func Test_cloneWith(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		src     map[string]int
		insert  string
		wantLen int
	}
	tests := []tc{
		{"a nil source starts a map of one", nil, "a", 1},
		{"an empty source starts a map of one", map[string]int{}, "a", 1},
		{"an existing source grows by one", map[string]int{"x": 1}, "a", 2},
		{"inserting a present key does not grow it", map[string]int{"a": 1}, "a", 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a nil map and a nil *map are different inputs; the helper takes the
		//: pointer form the snapshot hands it.
		var src *map[string]int
		if c.src != nil {
			src = &c.src
		}
		before := len(c.src)
		next := cloneWith(src, c.insert, 9)
		if len(next) != c.wantLen {
			t.Fatalf("the copy has %d entries, want %d", len(next), c.wantLen)
		}
		if next[c.insert] != 9 {
			t.Errorf("copy[%q] = %d, want the inserted 9", c.insert, next[c.insert])
		}
		//: the source must be untouched — a reader may be walking it.
		if len(c.src) != before {
			t.Errorf("the source grew to %d entries, want %d", len(c.src), before)
		}
		for key, value := range c.src {
			//: every pre-existing entry survives the copy, except the one
			//: deliberately overwritten.
			if key != c.insert && next[key] != value {
				t.Errorf("the copy lost the entry %q", key)
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
