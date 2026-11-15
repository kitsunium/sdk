package cache

import "testing"

// Test_frequencySketch pins the counting: a key counted k times reads k up
// to the four-bit ceiling, a sketch without a table counts nothing, the
// table is a power of two of words with one word a key, and a weight-bound
// cache's sketch grows with its live count, the counts starting over.
func Test_frequencySketch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		entries int
		adds    int
		want    uint64
	}
	tests := []tc{
		{name: "never counted", entries: 64, adds: 0, want: 0},
		{name: "counted once", entries: 64, adds: 1, want: 1},
		{name: "counted seven times", entries: 64, adds: 7, want: 7},
		{name: "past the ceiling", entries: 64, adds: 40, want: 15},
		{name: "no table", entries: 0, adds: 3, want: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var s frequencySketch
		if c.entries > 0 {
			s.size(c.entries)
		}
		for range c.adds {
			s.increment(0xdecafbad)
		}
		if got := s.frequency(0xdecafbad); got != c.want {
			t.Errorf("frequency = %d after %d increments, want %d", got, c.adds, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_frequencySketch_age pins the aging: once the period's increments are
// in, every counter is halved, so a key counted 15 times reads 7, and the
// period counts on from what the halving left.
func Test_frequencySketch_age(t *testing.T) {
	t.Parallel()
	var s frequencySketch
	s.size(8)
	if s.sampleSize != 80 {
		t.Fatalf("sampleSize = %d, want 80, ten increments an entry", s.sampleSize)
	}
	for range 15 {
		s.increment(7)
	}
	if got := s.frequency(7); got != 15 {
		t.Fatalf("frequency = %d before aging, want 15", got)
	}
	//: other keys fill the period; 7 is aged with them.
	for k := uint32(1000); s.additions != 0 && s.additions < s.sampleSize-1; k++ {
		s.increment(k * 2654435761)
	}
	s.increment(0x9e3779b9)
	if s.additions >= s.sampleSize {
		t.Fatalf("additions = %d, the period %d did not end", s.additions, s.sampleSize)
	}
	if got := s.frequency(7); got > 8 || got < 7 {
		t.Errorf("frequency = %d after aging, want 7 (8 if a counter it shares was odd)", got)
	}
}

// Test_frequencySketch_fit pins the growth of a weight-bound cache's sketch:
// a live count past the table's words takes the power of two at or above
// it, the counts and the period starting over; a smaller one keeps it.
func Test_frequencySketch_fit(t *testing.T) {
	t.Parallel()
	var s frequencySketch
	s.size(blockWords)
	s.increment(42)
	s.fit(8)
	if len(s.blocks)*blockWords != 8 || s.frequency(42) != 1 {
		t.Fatalf("fit(8) changed an 8-word table: %d words, frequency %d", len(s.blocks)*blockWords, s.frequency(42))
	}
	s.fit(9)
	if len(s.blocks)*blockWords != 16 || s.frequency(42) != 0 || s.sampleSize != 90 {
		t.Errorf("fit(9) = %d words, frequency %d, period %d; want 16, 0, 90", len(s.blocks)*blockWords, s.frequency(42), s.sampleSize)
	}
}

// Test_frequencySketch_doorkeeper pins TinyLFU's doorkeeper: a key's first
// use in a period sets its bits and no counter, so a key used once reaches
// no counter and is estimated at one; a held key's later uses in the period
// count without asking the doorkeeper; an aging empties the doorkeeper and
// opens a period, whose first use goes through it again; and a key evicted
// and admitted again within a period is counted on its return.
func Test_frequencySketch_doorkeeper(t *testing.T) {
	t.Parallel()
	var s frequencySketch
	s.size(64)
	const key, back uint32 = 0x5eed, 0xb4c
	var seen, seenBack uint8
	s.record(key, &seen)
	if s.frequency(key) != 0 || s.estimateHeld(key, seen) != 1 {
		t.Fatalf("after one use: counters %d, estimate %d; want 0, 1", s.frequency(key), s.estimateHeld(key, seen))
	}
	s.touch(key, &seen)
	s.touch(key, &seen)
	if s.frequency(key) != 2 || s.estimateHeld(key, seen) != 3 {
		t.Fatalf("after three uses: counters %d, estimate %d; want 2, 3", s.frequency(key), s.estimateHeld(key, seen))
	}
	s.age()
	if s.frequency(key) != 1 || s.estimateHeld(key, seen) != 1 {
		t.Fatalf("after aging: counters %d, estimate %d; want 1, 1", s.frequency(key), s.estimateHeld(key, seen))
	}
	s.touch(key, &seen)
	if s.frequency(key) != 1 || s.estimateHeld(key, seen) != 2 {
		t.Errorf("a first use after aging: counters %d, estimate %d; want 1, 2", s.frequency(key), s.estimateHeld(key, seen))
	}
	s.record(back, &seenBack)
	s.record(back, &seenBack)
	if s.frequency(back) != 1 || s.estimateHeld(back, seenBack) != 2 {
		t.Errorf("a key admitted twice in a period: counters %d, estimate %d; want 1, 2", s.frequency(back), s.estimateHeld(back, seenBack))
	}
	var none frequencySketch
	none.record(key, &seen)
	if none.estimateHeld(key, seen) != 0 {
		t.Errorf("a sketch without a table estimates %d, want 0", none.estimateHeld(key, seen))
	}
}
