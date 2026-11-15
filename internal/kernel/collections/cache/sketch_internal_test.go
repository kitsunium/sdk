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
	type tc struct {
		name string
		adds int
		// low and high bound the count after aging: a counter the key shares
		// with another may have been odd, and reads one more.
		low, high uint64
	}
	tests := []tc{
		{name: "a key at the ceiling reads 7", adds: 15, low: 7, high: 8},
		{name: "a key counted six times reads 3", adds: 6, low: 3, high: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var s frequencySketch
		s.size(8)
		if s.sampleSize != 80 {
			t.Fatalf("sampleSize = %d, want 80, ten increments an entry", s.sampleSize)
		}
		for range c.adds {
			s.increment(7)
		}
		if got := s.frequency(7); got != uint64(c.adds) {
			t.Fatalf("frequency = %d before aging, want %d", got, c.adds)
		}
		//: other keys fill the period; 7 is aged with them.
		for k := uint32(1000); s.additions != 0 && s.additions < s.sampleSize-1; k++ {
			s.increment(k * 2654435761)
		}
		s.increment(0x9e3779b9)
		if s.additions >= s.sampleSize {
			t.Fatalf("additions = %d, the period %d did not end", s.additions, s.sampleSize)
		}
		if got := s.frequency(7); got > c.high || got < c.low {
			t.Errorf("frequency = %d after aging, want %d (%d if a counter it shares was odd)", got, c.low, c.high)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_frequencySketch_fit pins the growth of a weight-bound cache's sketch:
// a live count past the table's words takes the power of two at or above
// it, the counts and the doorkeeper starting over — a key held across the
// growth goes through the new doorkeeper again, its mark of the old one
// worth nothing —; a smaller one keeps all of it.
func Test_frequencySketch_fit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// fit is the live count the table is fitted to.
		fit int
		// words and sample are the table's words and aging period after it.
		words, sample int
		// counted is the count of a key counted before the growth.
		counted uint64
		// held is the estimate of a key held across it, after one more use.
		held uint64
	}
	tests := []tc{
		{name: "room enough keeps the table, the counts and the marks", fit: 8, words: 8, sample: 80, counted: 1, held: 2},
		{name: "growth starts the counts and the marks over", fit: 9, words: 16, sample: 90, counted: 0, held: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var s frequencySketch
		s.size(blockWords)
		s.increment(42)
		seen := s.period
		s.record(7, &seen)
		s.fit(c.fit)
		s.touch(7, &seen)
		if got := len(s.blocks) * blockWords; got != c.words || s.sampleSize != c.sample {
			t.Errorf("fit(%d) = %d words aging every %d; want %d every %d", c.fit, got, s.sampleSize, c.words, c.sample)
		}
		if got := s.frequency(42); got != c.counted {
			t.Errorf("fit(%d) leaves a key counted once at %d, want %d", c.fit, got, c.counted)
		}
		if got := s.estimateHeld(7, seen); got != c.held {
			t.Errorf("a key held across fit(%d) is estimated at %d after one more use, want %d", c.fit, got, c.held)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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
	type tc struct {
		name    string
		entries int
		// ops is the key's history: r records a use of a key not held, t
		// touches a held one, a ages the sketch.
		ops string
		// counters and estimate are what the sketch answers after it.
		counters, estimate uint64
	}
	tests := []tc{
		{name: "one use", entries: 64, ops: "r", counters: 0, estimate: 1},
		{name: "three uses", entries: 64, ops: "rtt", counters: 2, estimate: 3},
		{name: "aged", entries: 64, ops: "rtta", counters: 1, estimate: 1},
		{name: "a first use after aging", entries: 64, ops: "rttat", counters: 1, estimate: 2},
		{name: "admitted twice in a period", entries: 64, ops: "rr", counters: 1, estimate: 2},
		{name: "admitted again after an aging", entries: 64, ops: "rar", counters: 0, estimate: 1},
		{name: "no table", entries: 0, ops: "r", counters: 0, estimate: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		const key uint32 = 0x5eed
		var s frequencySketch
		if c.entries > 0 {
			s.size(c.entries)
		}
		seen := s.period
		for _, op := range c.ops {
			switch op {
			case 'r':
				s.record(key, &seen)
			case 't':
				s.touch(key, &seen)
			default:
				s.age()
			}
		}
		if got := s.frequency(key); got != c.counters {
			t.Errorf("after %q: counters %d, want %d", c.ops, got, c.counters)
		}
		if got := s.estimateHeld(key, seen); got != c.estimate {
			t.Errorf("after %q: estimate %d, want %d", c.ops, got, c.estimate)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
