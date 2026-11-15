package cache

import (
	"math"
	"testing"
)

// Test_UnlockedMap_nextBatch pins the numbering FetchMany's read-once rule
// rests on (#840): each batch takes the next number, and the wrap after 2^32
// batches clears every entry's mark before numbering from 1 again, so an
// entry marked 2^32 batches ago is read, not taken for a repeat.
func Test_UnlockedMap_nextBatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		start     uint32
		want      uint32
		wantMarks uint32
	}
	tests := []tc{
		{name: "the next number", start: 41, want: 42, wantMarks: 1},
		{name: "the wrap", start: math.MaxUint32, want: 1, wantMarks: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewUnlockedMap(Config[string, int]{})
		m.Set("a", 1)
		m.Set("b", 2)
		for _, ent := range m.items {
			ent.batch = 1
		}
		m.batch = c.start
		if got := m.nextBatch(); got != c.want {
			t.Fatalf("nextBatch() = %d, want %d", got, c.want)
		}
		for key, ent := range m.items {
			if ent.batch != c.wantMarks {
				t.Fatalf("%q kept the mark %d, want %d", key, ent.batch, c.wantMarks)
			}
		}
		//: after the wrap, an entry marked 1 is read as a first position.
		hits := m.hits
		m.FetchMany([]string{"a"}, nil)
		if m.hits != hits+1 {
			t.Fatalf("the batch after nextBatch recorded %d hits, want 1", m.hits-hits)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
