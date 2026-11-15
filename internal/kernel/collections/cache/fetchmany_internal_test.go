package cache

import "testing"

// Test_UnlockedMap_clearMarks pins what FetchMany's read-once rule rests on
// (#840): the batch marks each entry it records a hit of in freq's top bit,
// and no mark survives the call — freq keeps S3-FIFO's count alone, and the
// scratch holds no entry — so the next batch reads the key again, a hit
// recorded once a batch however many times it names it.
func Test_UnlockedMap_clearMarks(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		policy   Policy
		wantFreq uint8
	}
	tests := []tc{
		{name: "LRU", policy: PolicyLRU, wantFreq: 0},
		{name: "S3-FIFO", policy: PolicyS3FIFO, wantFreq: 2},
		{name: "W-TinyLFU", policy: PolicyTinyLFU, wantFreq: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewUnlockedMap(Config[string, int]{Policy: c.policy, MaxEntries: 8})
		m.Set("a", 1)
		m.Set("b", 2)
		for range 2 {
			hits := m.hits
			m.FetchMany([]string{"a", "a", "b", "absent", "a"}, nil)
			if m.hits != hits+2 {
				t.Fatalf("a batch naming a three times and b once recorded %d hits, want 2", m.hits-hits)
			}
		}
		if len(m.marked) != 0 || m.marked[:cap(m.marked)][0] != nil {
			t.Fatalf("the scratch kept %d entries, its first %v, want none", len(m.marked), m.marked[:cap(m.marked)][0])
		}
		for key, ent := range m.items {
			if ent.freq != c.wantFreq {
				t.Fatalf("%q ended with freq %#x, want %#x: a mark survived the batch", key, ent.freq, c.wantFreq)
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
