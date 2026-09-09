package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/cache"
)

// The domain surface is aliases plus two constructors, so what needs pinning
// through the public name is that a Store reached from pkg/v1 still carries
// its siblings — the ADR 0039 type assertions ARE the ergonomics of the port,
// and an alias that lost one would compile and then fail at runtime.
func TestDomainFacadeCarriesEverySibling(t *testing.T) {
	t.Parallel()
	store, err := cache.NewMemory[string](cache.MemoryConfig{MaxEntries: 8, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	if _, ok := store.(cache.EntryFetcher[string]); !ok {
		t.Error("the store reached through pkg/v1 does not implement EntryFetcher")
	}
	tagger, ok := store.(cache.Tagger)
	if !ok {
		t.Fatal("the store reached through pkg/v1 does not implement Tagger")
	}
	loader, ok := store.(cache.Loader[string])
	if !ok {
		t.Fatal("the store reached through pkg/v1 does not implement Loader")
	}

	value, err := loader.Load(t.Context(), "k", func(context.Context) (cache.Entry[string], error) {
		return cache.Entry[string]{Value: "v", TTL: cache.NoExpiry, Tags: []string{"t"}}, nil
	})
	if err != nil || value != "v" {
		t.Fatalf("Load = (%q, %v), want (\"v\", nil)", value, err)
	}
	if removed, invalidateErr := tagger.InvalidateTag(t.Context(), "t"); invalidateErr != nil || removed != 1 {
		t.Fatalf("InvalidateTag = (%d, %v), want (1, nil)", removed, invalidateErr)
	}
}

// A capacity is the caller's decision, so the facade must refuse a zero rather
// than hand back a cache that never evicts (ADR 0031).
func TestDomainFacadeRefusesAZeroCapacity(t *testing.T) {
	t.Parallel()
	store, err := cache.NewMemory[string](cache.MemoryConfig{})
	if err == nil || store != nil {
		t.Fatalf("NewMemory(zero config) = (%v, %v), want a refusal", store, err)
	}
	if !errs.HasReason(err, "CACHE_MISCONFIGURED") {
		t.Fatalf("NewMemory returned %v, want CACHE_MISCONFIGURED", err)
	}
}

// A chain refuses a tier set it cannot honour, because a promoted copy without
// its tags is one InvalidateTag can never reach again.
func TestDomainFacadeChain(t *testing.T) {
	t.Parallel()
	near, err := cache.NewMemory[string](cache.MemoryConfig{MaxEntries: 4})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	if _, chainErr := cache.NewChain(cache.ChainConfig{}, near); chainErr == nil {
		t.Fatal("NewChain accepted a single tier")
	}
	far, err := cache.NewMemory[string](cache.MemoryConfig{MaxEntries: 4})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	chain, err := cache.NewChain(cache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	if setErr := chain.Set(t.Context(), "k", cache.Entry[string]{Value: "v"}); setErr != nil {
		t.Fatalf("chain Set: %v", setErr)
	}
	_, found, fetchErr := far.Fetch(t.Context(), "k")
	if fetchErr != nil || !found {
		t.Fatalf("the far tier was not written through the facade: (%t, %v)", found, fetchErr)
	}
}

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
