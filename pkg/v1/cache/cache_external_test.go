package cache_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/cache"
)

// The facade is a type alias plus a constructor, so what needs pinning is that
// the aliased value behaves as the kernel one does when reached through the
// public name — a Fetch that misses must be distinguishable from one that hits
// a zero value, which is why the second return exists.
func TestFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		key     string
		want    int
		wantHit bool
	}
	tests := []tc{
		{"a key that was set", "answer", 42, true},
		{"a zero value is still a hit", "zero", 0, true},
		{"a key never set", "absent", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := cache.New[string, int](cache.Config[string, int]{MaxEntries: 8, DefaultTTL: time.Minute})
		cch.Set("answer", 42)
		cch.Set("zero", 0)

		got, ok := cch.Fetch(c.key)
		if ok != c.wantHit {
			t.Fatalf("Fetch(%q) hit = %v, want %v", c.key, ok, c.wantHit)
		}
		if got != c.want {
			t.Errorf("Fetch(%q) = %d, want %d", c.key, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Stats is an alias too, and it must count through the facade exactly as it
// does underneath — a hit, a miss, and nothing invented.
func TestFacadeStats(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		fetch    []string
		wantHits uint64
		wantMiss uint64
	}
	tests := []tc{
		{"one hit", []string{"answer"}, 1, 0},
		{"one miss", []string{"absent"}, 0, 1},
		{"both", []string{"answer", "absent"}, 1, 1},
		{"nothing fetched", nil, 0, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cch := cache.New[string, int](cache.Config[string, int]{MaxEntries: 8})
		cch.Set("answer", 42)
		for _, k := range c.fetch {
			cch.Fetch(k)
		}
		st := cch.Stats()
		if st.Hits != c.wantHits || st.Misses != c.wantMiss {
			t.Errorf("stats = %+v, want hits=%d misses=%d", st, c.wantHits, c.wantMiss)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
