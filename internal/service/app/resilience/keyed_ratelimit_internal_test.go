// Package resilience — the keyed limiter's bound and recency order.
package resilience

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// keyed builds a keyed limiter whose key is read from the context, for the
// internal tests that inspect its map.
func keyed(t *testing.T, clk clock.Clock, maxKeys int, idle time.Duration) *keyedLimiter {
	t.Helper()
	runner := NewKeyedRateLimiter(KeyedRateLimiterConfig{
		Rate: 1, Burst: 1, MaxKeys: maxKeys, IdleTimeout: idle, Clock: clk,
		Key: func(ctx context.Context) string {
			key, _ := ctx.Value(internalKey{}).(string)
			return key
		},
	})
	limiter, ok := runner.(*keyedLimiter)
	if !ok {
		t.Fatalf("NewKeyedRateLimiter returned %T, want *keyedLimiter", runner)
	}
	return limiter
}

// internalKey is the context key these tests charge calls to.
type internalKey struct{}

// Test_keyedLimiter_bound pins the memory bound: however many distinct keys
// arrive, no more than MaxKeys are held, and the ones kept are the most
// recently used. A stream of distinct keys — every address on the internet,
// or a client minting a new one per request — must not grow the process.
func Test_keyedLimiter_bound(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	limiter := keyed(t, manual, 3, time.Hour)
	for i := range 50 {
		limiter.bucketOf("key-" + strconv.Itoa(i))
	}
	if got := len(limiter.byKey); got != 3 {
		t.Fatalf("%d keys held after 50 distinct ones, want the bound of 3", got)
	}
	//: most recent first: 49, 48, 47, and the links agree both ways.
	want := []string{"key-49", "key-48", "key-47"}
	index := 0
	for entry := limiter.head; entry != nil; entry = entry.next {
		if index >= len(want) || entry.key != want[index] {
			t.Fatalf("position %d holds %s, want %v", index, entry.key, want)
		}
		if entry.next == nil && limiter.tail != entry {
			t.Error("the tail is not the last entry")
		}
		index++
	}
	if index != len(want) {
		t.Errorf("the list holds %d entries, the index %d", index, len(limiter.byKey))
	}
}

// Test_keyedLimiter_recencyProtects pins the LRU half of the bound: a key in
// use is moved to the front on every call, so the key the bound forgets is
// the one nobody is using.
func Test_keyedLimiter_recencyProtects(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	limiter := keyed(t, manual, 2, time.Hour)
	kept := limiter.bucketOf("kept")
	limiter.bucketOf("other")
	//: touching "kept" makes "other" the least recently used.
	if limiter.bucketOf("kept") != kept {
		t.Fatal("a held key was handed a new bucket")
	}
	limiter.bucketOf("newcomer")
	if _, found := limiter.byKey["other"]; found {
		t.Error("the bound forgot a recently used key instead of the idle one")
	}
	if limiter.bucketOf("kept") != kept {
		t.Error("the recently used key lost its bucket")
	}
}

// Test_keyedLimiter_idleSweep pins that idle keys are dropped when a NEW key
// arrives, not only when they are asked for again — otherwise a limiter that
// saw a burst of one-off clients would hold them until the bound pushed them
// out.
func Test_keyedLimiter_idleSweep(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	limiter := keyed(t, manual, 100, time.Minute)
	for i := range 10 {
		limiter.bucketOf("one-off-" + strconv.Itoa(i))
	}
	manual.Advance(time.Minute)
	limiter.bucketOf("fresh")
	if got := len(limiter.byKey); got != 1 {
		t.Errorf("%d keys held after every other went idle, want 1", got)
	}
}
