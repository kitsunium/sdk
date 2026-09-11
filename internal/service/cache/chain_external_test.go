package cache_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svccache "github.com/kitsunium/sdk/internal/service/cache"
)

// failOn names the one operation a flakyStore refuses. A single field rather
// than three booleans: the states are mutually exclusive by construction, and
// three independent flags would let a test express combinations the fixture
// does not model.
type failOn string

const (
	failNothing failOn = ""
	failGet     failOn = "get"
	failSet     failOn = "set"
	failDelete  failOn = "delete"
)

// bareStore implements Store and nothing else. It is what NewChain must
// refuse: a tier that cannot return its entry loses TTL and tags on
// promotion, and a promoted copy without tags is one InvalidateTag can never
// reach again.
type bareStore struct{}

func (bareStore) Fetch(context.Context, string) (string, bool, error) { return "", false, nil }

func (bareStore) Set(context.Context, string, corecache.EntryValue[string]) error { return nil }

func (bareStore) Delete(context.Context, string) error { return nil }

// flakyStore is a full tier that can be told to fail one operation. Used to
// pin which side of a two-store write each failure leaves inconsistent.
type flakyStore struct {
	inner corecache.Store[string]
	fails failOn
	// cause is what a failing operation returns; nil means the typed
	// CacheBackendFailed every other test expects.
	cause error
}

// failure is the error a failing operation returns.
func (f *flakyStore) failure() error {
	if f.cause != nil {
		return f.cause
	}
	return corecache.CacheBackendFailed
}

func newFlaky(t *testing.T) *flakyStore {
	t.Helper()
	return &flakyStore{inner: newStore(t, svccache.MemoryConfig{MaxEntries: 16}), fails: failNothing}
}

func (f *flakyStore) Fetch(ctx context.Context, key string) (string, bool, error) {
	if f.fails == failGet {
		return "", false, f.failure()
	}
	return f.inner.Fetch(ctx, key)
}

func (f *flakyStore) FetchEntry(ctx context.Context, key string) (corecache.EntryValue[string], bool, error) {
	if f.fails == failGet {
		return corecache.EntryValue[string]{}, false, f.failure()
	}
	fetcher, _ := f.inner.(corecache.EntryFetcher[string])
	return fetcher.FetchEntry(ctx, key)
}

func (f *flakyStore) Set(ctx context.Context, key string, value corecache.EntryValue[string]) error {
	if f.fails == failSet {
		return corecache.CacheBackendFailed
	}
	return f.inner.Set(ctx, key, value)
}

func (f *flakyStore) Delete(ctx context.Context, key string) error {
	if f.fails == failDelete {
		return corecache.CacheBackendFailed
	}
	return f.inner.Delete(ctx, key)
}

func (f *flakyStore) InvalidateTag(ctx context.Context, tag string) (int, error) {
	tagger, _ := f.inner.(corecache.Tagger)
	return tagger.InvalidateTag(ctx, tag)
}

func TestNewChainRefusesAnIncompleteTierSet(t *testing.T) {
	t.Parallel()
	full := newStore(t, svccache.MemoryConfig{MaxEntries: 4})
	cases := []struct {
		name   string
		stores []corecache.Store[string]
	}{
		{"no tier", nil},
		{"one tier", []corecache.Store[string]{full}},
		{"nil tier", []corecache.Store[string]{full, nil}},
		{"tier without EntryFetcher", []corecache.Store[string]{full, bareStore{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			chain, err := svccache.NewChain(svccache.ChainConfig{}, tc.stores...)
			if err == nil {
				t.Fatal("NewChain accepted an incomplete tier set")
			}
			if chain != nil {
				t.Fatal("NewChain returned a chain alongside its error")
			}
			if !errs.HasCode(err, svccache.CodeCacheChainMisconfigured) {
				t.Fatalf("NewChain returned %v, want CACHE_CHAIN_MISCONFIGURED", err)
			}
		})
	}
}

func TestChainPromotesAFarHitWithItsTTLAndTags(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}

	//: seed the FAR tier only, behind the chain's back.
	mustSet(t, far, "k", entry("v", time.Minute, "tag"))
	manual.Advance(30 * time.Second)

	value, found, err := chain.Fetch(t.Context(), "k")
	if err != nil || !found || value != "v" {
		t.Fatalf("chain Fetch = (%q, %t, %v), want a hit", value, found, err)
	}

	nearFetcher, _ := near.(corecache.EntryFetcher[string])
	promoted, found, err := nearFetcher.FetchEntry(t.Context(), "k")
	if err != nil || !found {
		t.Fatalf("the near tier was not promoted: (%t, %v)", found, err)
	}
	if promoted.TTL != 30*time.Second {
		t.Fatalf("the promoted copy carries TTL %v, want the remaining 30s — promoting with the original TTL makes an entry immortal by being popular", promoted.TTL)
	}
	//: the whole reason FetchEntry exists: a promoted copy stays reachable.
	tagger, _ := near.(corecache.Tagger)
	if removed := invalidated(t, tagger, "tag"); removed != 1 {
		t.Fatalf("InvalidateTag removed %d in the near tier, want 1 — the promoted copy lost its tags", removed)
	}
}

func TestChainWritesEveryTierAndReadsTheNearest(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	ctx := t.Context()

	mustSet(t, chain, "k", entry("v", time.Minute))
	//: a write reaches every tier, not only the nearest.
	for name, level := range map[string]corecache.Store[string]{"near": near, "far": far} {
		if _, found := held(t, level, "k"); !found {
			t.Errorf("the %s tier was not written", name)
		}
	}
	if err := chain.Delete(ctx, "k"); err != nil {
		t.Fatalf("chain removal: %v", err)
	}
	//: and so does a removal.
	for name, level := range map[string]corecache.Store[string]{"near": near, "far": far} {
		if _, found := held(t, level, "k"); found {
			t.Errorf("the %s tier still holds the deleted key", name)
		}
	}
}

// TestChainSetWritesTheAuthorityFirst pins the ordering rule: a near tier
// holding a value the far tier refused is a lie that outlives the error.
func TestChainSetWritesTheAuthorityFirst(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newFlaky(t)
	far.fails = failSet
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}

	setErr := chain.Set(t.Context(), "k", entry("v", time.Minute))
	if !errs.HasCode(setErr, svccache.CodeCacheTierFailed) {
		t.Fatalf("chain Set returned %v, want CACHE_TIER_FAILED", setErr)
	}
	if _, found := held(t, near, "k"); found {
		t.Fatal("the near tier holds a value the far tier refused")
	}
}

// TestChainDeleteAttemptsEveryTier pins the other half: skipping the remaining
// tiers after one failure leaves the value fetchable from a tier nothing tried.
func TestChainDeleteAttemptsEveryTier(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newFlaky(t)
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	mustSet(t, chain, "k", entry("v", time.Minute))

	far.fails = failDelete
	deleteErr := chain.Delete(t.Context(), "k")
	if !errs.HasCode(deleteErr, svccache.CodeCacheTierFailed) {
		t.Fatalf("chain Delete returned %v, want CACHE_TIER_FAILED", deleteErr)
	}
	if _, found := held(t, near, "k"); found {
		t.Fatal("the near tier was skipped after the far tier failed — the value is still servable")
	}
}

// TestAFailedPromotionDoesNotFailTheRead: the caller already has the value, so
// a degraded cache must not become a degraded service. The failure is still
// reported, through the hook.
func TestAFailedPromotionDoesNotFailTheRead(t *testing.T) {
	t.Parallel()
	near := newFlaky(t)
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	var reported []string
	chain, err := svccache.NewChain(svccache.ChainConfig{
		OnPromoteError: func(key string, _ error) { reported = append(reported, key) },
	}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}

	mustSet(t, far, "k", entry("v", time.Minute))
	near.fails = failSet

	value, found, fetchErr := chain.Fetch(t.Context(), "k")
	if fetchErr != nil || !found || value != "v" {
		t.Fatalf("chain Fetch = (%q, %t, %v), want the value despite the failed promotion", value, found, fetchErr)
	}
	if len(reported) != 1 || reported[0] != "k" {
		t.Fatalf("OnPromoteError saw %v, want [k] — a near tier that rejects every promotion must not be invisible", reported)
	}
}

// TestAFailedTierStopsTheWalk: continuing past a broken tier would serve a
// staler answer from behind it and call that a hit.
func TestAFailedTierStopsTheWalk(t *testing.T) {
	t.Parallel()
	near := newFlaky(t)
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	mustSet(t, far, "k", entry("stale", time.Minute))
	near.fails = failGet

	_, found, fetchErr := chain.Fetch(t.Context(), "k")
	if found {
		t.Fatal("the chain served a far value past a broken near tier")
	}
	if !errs.HasCode(fetchErr, svccache.CodeCacheTierFailed) {
		t.Fatalf("chain Fetch returned %v, want CACHE_TIER_FAILED", fetchErr)
	}
}

// TestAnUntypedTierErrorKeepsItsIdentity pins that a tier failing with a
// backend's own untyped sentinel stays matchable through the chain: HasCode
// sees CACHE_TIER_FAILED, and errors.Is still finds the sentinel. It used to
// travel only as a string field — seen failing so, with the untyped branch
// removed: "errors.Is(err, the tier's own error) = false".
func TestAnUntypedTierErrorKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	reset := errors.New("backend: connection reset")
	near := newFlaky(t)
	near.fails, near.cause = failGet, reset
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	_, _, fetchErr := chain.Fetch(t.Context(), "k")
	if !errs.HasCode(fetchErr, svccache.CodeCacheTierFailed) {
		t.Fatalf("chain Fetch returned %v, want CACHE_TIER_FAILED", fetchErr)
	}
	if !errors.Is(fetchErr, reset) {
		t.Fatalf("errors.Is(err, the tier's own error) = false; err = %v", fetchErr)
	}
}

// TestATypedTierErrorBehindAWrapperKeepsTheTierVerdict pins the typed branch
// against a cause that is typed one layer down. errs.Wrap looks through a
// stdlib wrapper for an *errs.Error, while the branch used a direct type
// assertion, so a tier returning fmt.Errorf("backend: %w", sentinel) was
// wrapped as untyped and origin-wins gave the verdict to the sentinel.
// Seen failing with the direct assertion restored: CodeOf read the backend's
// CACHE_BACKEND_FAILED.
func TestATypedTierErrorBehindAWrapperKeepsTheTierVerdict(t *testing.T) {
	t.Parallel()
	near := newFlaky(t)
	near.fails, near.cause = failGet, fmt.Errorf("backend: %w", corecache.CacheBackendFailed)
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	_, _, fetchErr := chain.Fetch(t.Context(), "k")
	if code, _ := errs.CodeOf(fetchErr); code != svccache.CodeCacheTierFailed {
		t.Fatalf("chain Fetch returned %v (code %v), want CACHE_TIER_FAILED as the verdict", fetchErr, code)
	}
}

func TestChainInvalidateTagReachesEveryTier(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	mustSet(t, chain, "k", entry("v", time.Minute, "t"))

	tagger, ok := chain.(corecache.Tagger)
	if !ok {
		t.Fatal("the chain does not implement Tagger")
	}
	//: the entry lives in BOTH tiers, so the total counts two removals — the
	//: unit is removals, not distinct keys, and the doc says so.
	if removed := invalidated(t, tagger, "t"); removed != 2 {
		t.Fatalf("InvalidateTag removed %d, want 2", removed)
	}
	//: the invalidation must have reached BOTH tiers.
	for name, level := range map[string]corecache.Store[string]{"near": near, "far": far} {
		if _, found := held(t, level, "k"); found {
			t.Errorf("the %s tier still holds the invalidated entry", name)
		}
	}
}

func TestChainLoadWritesThroughEveryTier(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	loader, ok := chain.(corecache.Loader[string])
	if !ok {
		t.Fatal("the chain does not implement Loader")
	}

	value, err := loader.Load(t.Context(), "k", func(context.Context) (corecache.EntryValue[string], error) {
		return entry("filled", time.Minute, "t"), nil
	})
	if err != nil || value != "filled" {
		t.Fatalf("chain Load = (%q, %v), want (\"filled\", nil)", value, err)
	}
	//: write-through: the fill's cost is paid once, so every tier gets it.
	for name, level := range map[string]corecache.Store[string]{"near": near, "far": far} {
		if _, found := held(t, level, "k"); !found {
			t.Errorf("the %s tier was not written by Load", name)
		}
	}
}
