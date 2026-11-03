package cache

import (
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	svcjson "github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// lazyClient is a client that never connects: NewStore sends no command.
func lazyClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func validConfig(t *testing.T) Config {
	return Config{Client: lazyClient(t), Codec: svcjson.New(), Prefix: "app"}
}

func TestNewStoreRefusesByFieldWhatNoCommandCouldFix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		edit  func(c *Config)
		field string
	}{
		{func(c *Config) { c.Client = nil }, "Client"},
		{func(c *Config) { c.Codec = nil }, "Codec"},
		{func(c *Config) { c.Prefix = "" }, "Prefix"},
		{func(c *Config) { c.Prefix = "a{b" }, "Prefix"},
		{func(c *Config) { c.DefaultTTL = -time.Second }, "DefaultTTL"},
		{func(c *Config) { c.FillLease = -time.Second }, "FillLease"},
		{func(c *Config) { c.FillLease = time.Second }, "FillPoll"},
		{func(c *Config) { c.FillLease, c.FillPoll = time.Second, time.Second }, "FillPoll"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			tc.edit(&cfg)
			store, err := NewStore[string](cfg)
			if store != nil || !errors.Is(err, corecache.CacheMisconfigured) {
				t.Fatalf("NewStore = (%v, %v), want CACHE_MISCONFIGURED", store, err)
			}
			if fields := errs.FieldsOf(err); len(fields) == 0 || fields[0].StringValue() != tc.field {
				t.Fatalf("NewStore = %v, want the field %s named", err, tc.field)
			}
		})
	}
	store, err := NewStore[string](validConfig(t))
	if err != nil || store == nil {
		t.Fatalf("NewStore(valid) = (%v, %v), want a store", store, err)
	}
	for _, sibling := range []any{store.(corecache.EntryFetcher[string]), store.(corecache.Tagger), store.(corecache.Loader[string])} {
		if sibling == nil {
			t.Fatal("a sibling is missing")
		}
	}
}

func TestEveryKeyOfAStoreSharesItsHashTag(t *testing.T) {
	t.Parallel()
	keys := newKeySpace("app")
	for _, key := range []string{keys.value("k"), keys.tags("k"), keys.group("t"), keys.lease("k")} {
		if key[:6] != "{app}:" {
			t.Errorf("key %q does not open with the hash tag {app}:", key)
		}
	}
}

func TestTheThreeTTLMeaningsMapOntoPX(t *testing.T) {
	t.Parallel()
	store := &redisStore[string]{defaultTTL: time.Minute}
	none := &redisStore[string]{}
	tests := []struct {
		store *redisStore[string]
		ttl   time.Duration
		want  int64
	}{
		{store, corecache.NoExpiry, 0},
		{store, 0, 60_000},
		{none, 0, 0},
		{store, 1500 * time.Millisecond, 1500},
		{store, time.Microsecond, 1},
	}
	for _, tc := range tests {
		if got := tc.store.ttlMillis(tc.ttl); got != tc.want {
			t.Errorf("ttlMillis(%v) = %d, want %d", tc.ttl, got, tc.want)
		}
	}
	if remaining(-1) != corecache.NoExpiry || remaining(2500) != 2500*time.Millisecond || remaining(0) != time.Millisecond {
		t.Error("remaining does not map PTTL's answers onto the domain's TTL")
	}
}

func TestAnEntryIsRefusedAsTheMemoryStoreRefusesIt(t *testing.T) {
	t.Parallel()
	store, err := NewStore[string](validConfig(t))
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	for name, call := range map[string]func() error{
		"an empty key":   func() error { return store.Set(t.Context(), "", corecache.EntryValue[string]{}) },
		"an empty tag":   func() error { return store.Set(t.Context(), "k", corecache.EntryValue[string]{Tags: []string{""}}) },
		"a negative TTL": func() error { return store.Set(t.Context(), "k", corecache.EntryValue[string]{TTL: -2}) },
	} {
		if err := call(); !errors.Is(err, corecache.CacheEntryRejected) {
			t.Errorf("Set(%s) = %v, want CACHE_ENTRY_REJECTED before any command", name, err)
		}
	}
	if got := uniqueTags([]string{"b", "a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Errorf("uniqueTags = %v, want [a b]", got)
	}
}

func TestAFailedCommandIsCommandFailedWithTheDomainsCodeInItsTrail(t *testing.T) {
	t.Parallel()
	store, err := NewStore[string](validConfig(t))
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	_, _, ferr := store.Fetch(t.Context(), "k")
	if !errs.HasCode(ferr, CodeRedisCacheCommandFailed) || !errs.HasCode(ferr, corecache.CodeCacheBackendFailed) {
		t.Fatalf("Fetch on an unreachable server = %v, want COMMAND_FAILED with CACHE_BACKEND_FAILED", ferr)
	}
}
