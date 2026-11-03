//go:build integration

// The Valkey cache tier (third-party/db/redis/cache) against a real Valkey:
// the cache domain's contract — Store, EntryFetcher's remaining TTL,
// Tagger's count, Loader's single fill within a process and, under a fill
// lease, across two replicas — and the L1/L2 chain with a memory store as
// the near tier. Run from the e2e module:
//
//	cd e2e && GOWORK=off go test -tags integration -race ./integration/cache/valkey/
//
// VALKEY_ADDR names a running server instead of a container.
package valkey_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
	svcjson "github.com/kitsunium/sdk/internal/service/data/codec/json"

	"github.com/kitsunium/sdk/e2e/integration/internal/servers"
	rdscache "github.com/kitsunium/sdk/third-party/db/redis/cache"
)

// prefixSeq names one key space per test.
var prefixSeq atomic.Int64

// user is the value the stores keep.
type user struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func newStore(t *testing.T, rdb goredis.UniversalClient, prefix string, edit func(c *rdscache.Config)) corecache.Store[user] {
	t.Helper()
	cfg := rdscache.Config{Client: rdb, Codec: svcjson.New(), Prefix: prefix}
	if edit != nil {
		edit(&cfg)
	}
	store, err := rdscache.NewStore[user](cfg)
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	return store
}

func setup(t *testing.T) (goredis.UniversalClient, string) {
	t.Helper()
	rdb := goredis.NewClient(&goredis.Options{Addr: servers.Valkey(t)})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, "app" + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(prefixSeq.Add(1), 10)
}

func TestIntegration_ValkeyCache_storeAndEntry(t *testing.T) {
	rdb, prefix := setup(t)
	store := newStore(t, rdb, prefix, func(c *rdscache.Config) { c.DefaultTTL = time.Minute })
	ctx := t.Context()
	if _, found, err := store.Fetch(ctx, "u1"); found || err != nil {
		t.Fatalf("Fetch(absent) = (%v, %v), want a plain miss", found, err)
	}
	if err := store.Set(ctx, "u1", corecache.EntryValue[user]{Value: user{"ada", 36}, TTL: 10 * time.Second, Tags: []string{"team", "team"}}); err != nil {
		t.Fatalf("Set = %v", err)
	}
	got, found, err := store.Fetch(ctx, "u1")
	if err != nil || !found || got.Name != "ada" {
		t.Fatalf("Fetch = (%+v, %v, %v), want ada", got, found, err)
	}
	entry, found, err := store.(corecache.EntryFetcher[user]).FetchEntry(ctx, "u1")
	if err != nil || !found || entry.TTL <= 8*time.Second || entry.TTL > 10*time.Second || len(entry.Tags) != 1 {
		t.Fatalf("FetchEntry = (%+v, %v, %v), want the REMAINING TTL and one tag", entry, found, err)
	}
	if err := store.Set(ctx, "u2", corecache.EntryValue[user]{Value: user{"bob", 1}, TTL: corecache.NoExpiry}); err != nil {
		t.Fatalf("Set(NoExpiry) = %v", err)
	}
	if entry, _, _ := store.(corecache.EntryFetcher[user]).FetchEntry(ctx, "u2"); entry.TTL != corecache.NoExpiry {
		t.Fatalf("FetchEntry(NoExpiry).TTL = %v, want NoExpiry", entry.TTL)
	}
	if err := store.Set(ctx, "u3", corecache.EntryValue[user]{Value: user{"cy", 2}}); err != nil {
		t.Fatalf("Set(default TTL) = %v", err)
	}
	if entry, _, _ := store.(corecache.EntryFetcher[user]).FetchEntry(ctx, "u3"); entry.TTL <= 0 || entry.TTL > time.Minute {
		t.Fatalf("FetchEntry(default).TTL = %v, want the store's minute", entry.TTL)
	}
	if err := store.Set(ctx, "short", corecache.EntryValue[user]{Value: user{"x", 0}, TTL: 200 * time.Millisecond}); err != nil {
		t.Fatalf("Set(short) = %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, found, _ := store.Fetch(ctx, "short"); found {
		t.Fatal("an entry outlived its TTL")
	}
	if err := store.Delete(ctx, "u1"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if err := store.Delete(ctx, "u1"); err != nil {
		t.Fatalf("Delete(again) = %v, want nil", err)
	}
	if _, found, _ := store.Fetch(ctx, "u1"); found {
		t.Fatal("a deleted entry is still fetched")
	}
	if err := rdb.Set(ctx, "{"+prefix+"}:v:junk", "not json", 0).Err(); err != nil {
		t.Fatalf("raw SET = %v", err)
	}
	if _, _, err := store.Fetch(ctx, "junk"); !errs.HasCode(err, rdscache.CodeRedisCacheValueUndecodable) {
		t.Fatalf("Fetch(junk) = %v, want VALUE_UNDECODABLE", err)
	}
}

func TestIntegration_ValkeyCache_tags(t *testing.T) {
	rdb, prefix := setup(t)
	store := newStore(t, rdb, prefix, nil)
	ctx := t.Context()
	for i, tags := range [][]string{{"a"}, {"a", "b"}, {"a"}, {"b"}} {
		if err := store.Set(ctx, "k"+strconv.Itoa(i), corecache.EntryValue[user]{Value: user{Age: i}, Tags: tags}); err != nil {
			t.Fatalf("Set = %v", err)
		}
	}
	if err := store.Set(ctx, "k2", corecache.EntryValue[user]{Value: user{Age: 2}, Tags: []string{"c"}}); err != nil {
		t.Fatalf("Set(retag) = %v", err)
	}
	removed, err := store.(corecache.Tagger).InvalidateTag(ctx, "a")
	if err != nil || removed != 2 {
		t.Fatalf("InvalidateTag(a) = (%d, %v), want 2: k2 was retagged", removed, err)
	}
	for key, want := range map[string]bool{"k0": false, "k1": false, "k2": true, "k3": true} {
		if _, found, _ := store.Fetch(ctx, key); found != want {
			t.Errorf("Fetch(%s) found = %v, want %v", key, found, want)
		}
	}
	if removed, err := store.(corecache.Tagger).InvalidateTag(ctx, "b"); err != nil || removed != 1 {
		t.Fatalf("InvalidateTag(b) = (%d, %v), want 1: k1 is gone already", removed, err)
	}
	if removed, err := store.(corecache.Tagger).InvalidateTag(ctx, "nothing"); err != nil || removed != 0 {
		t.Fatalf("InvalidateTag(nothing) = (%d, %v), want (0, nil)", removed, err)
	}
	for i := range 300 {
		if err := store.Set(ctx, "bulk"+strconv.Itoa(i), corecache.EntryValue[user]{Tags: []string{"bulk"}}); err != nil {
			t.Fatalf("Set(bulk) = %v", err)
		}
	}
	if removed, err := store.(corecache.Tagger).InvalidateTag(ctx, "bulk"); err != nil || removed != 300 {
		t.Fatalf("InvalidateTag(bulk) = (%d, %v), want 300 over several pages", removed, err)
	}
}

func TestIntegration_ValkeyCache_loadFillsOnceAcrossReplicas(t *testing.T) {
	rdb, prefix := setup(t)
	lease := func(c *rdscache.Config) { c.FillLease, c.FillPoll = 2*time.Second, 20*time.Millisecond }
	replicas := []corecache.Store[user]{newStore(t, rdb, prefix, lease), newStore(t, rdb, prefix, lease)}
	var fills atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[user], error) {
		fills.Add(1)
		time.Sleep(200 * time.Millisecond)
		return corecache.EntryValue[user]{Value: user{"filled", 1}, TTL: time.Minute}, nil
	}
	var callers sync.WaitGroup
	for i := range 40 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			got, err := replicas[i%2].(corecache.Loader[user]).Load(t.Context(), "hot", fill)
			if err != nil || got.Name != "filled" {
				t.Errorf("Load = (%+v, %v), want the filled value", got, err)
			}
		}()
	}
	callers.Wait()
	if n := fills.Load(); n != 1 {
		t.Fatalf("fill ran %d times across two replicas, want 1", n)
	}
	failing := func(context.Context) (corecache.EntryValue[user], error) {
		return corecache.EntryValue[user]{}, errors.New("origin down")
	}
	if _, err := replicas[0].(corecache.Loader[user]).Load(t.Context(), "cold", failing); !errors.Is(err, corecache.CacheFillFailed) {
		t.Fatalf("Load(failing) = %v, want CACHE_FILL_FAILED", err)
	}
}

func TestIntegration_ValkeyCache_isTheFarTierOfAChain(t *testing.T) {
	rdb, prefix := setup(t)
	far := newStore(t, rdb, prefix, nil)
	near, err := svccache.NewMemory[user](svccache.MemoryConfig{MaxEntries: 100})
	if err != nil {
		t.Fatalf("NewMemory = %v", err)
	}
	chain, err := svccache.NewChain(svccache.ChainConfig{OnPromoteError: func(string, error) {}}, near, far)
	if err != nil {
		t.Fatalf("NewChain(memory, valkey) = %v, want the valkey store accepted as the far tier", err)
	}
	ctx := t.Context()
	if err := far.Set(ctx, "k", corecache.EntryValue[user]{Value: user{"far", 1}, TTL: time.Minute, Tags: []string{"t"}}); err != nil {
		t.Fatalf("far Set = %v", err)
	}
	got, found, err := chain.Fetch(ctx, "k")
	if err != nil || !found || got.Name != "far" {
		t.Fatalf("chain Fetch = (%+v, %v, %v), want the far tier's value", got, found, err)
	}
	if _, found, _ := near.Fetch(ctx, "k"); !found {
		t.Fatal("the far tier's value was not promoted into the near tier")
	}
	if removed, err := chain.(corecache.Tagger).InvalidateTag(ctx, "t"); err != nil || removed == 0 {
		t.Fatalf("chain InvalidateTag = (%d, %v), want both tiers reached", removed, err)
	}
	if _, found, _ := far.Fetch(ctx, "k"); found {
		t.Fatal("the far tier kept a tagged entry the chain invalidated")
	}
}
