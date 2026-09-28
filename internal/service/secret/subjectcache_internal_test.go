package secret

import (
	"bytes"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// openedFor returns an opened key whose AEAD key is 32 copies of fill.
func openedFor(fill byte) *openedKey {
	return &openedKey{seal: bytes.Repeat([]byte{fill}, 32)}
}

// TestOpenedKeyHandsOutCopiesAndWipesInPlace pins the two halves of the
// sharing rule: a caller's key is a copy, so wiping the shared one does not
// zero a key a call is still sealing with — and the shared bytes themselves
// are cleared, not merely dropped.
func TestOpenedKeyHandsOutCopiesAndWipesInPlace(t *testing.T) {
	t.Parallel()
	opened := openedFor(0xAB)
	shared := opened.seal
	key, _, live := opened.take()
	if !live {
		t.Fatal("take of a fresh key reports it wiped")
	}
	opened.wipe()
	if !bytes.Equal(shared, make([]byte, 32)) {
		t.Fatalf("wipe left the shared bytes %x, want zeros", shared)
	}
	if !bytes.Equal(key.Bytes(), bytes.Repeat([]byte{0xAB}, 32)) {
		t.Fatal("wiping the shared key zeroed a copy a call was using")
	}
	if _, _, again := opened.take(); again {
		t.Fatal("take after wipe reports a live key")
	}
	opened.wipe()
}

// TestKeyCacheWipesWhatItLetsGo pins that every way a key leaves the cache —
// capacity, expiry, destruction, a crossed epoch, a lost race — clears it.
func TestKeyCacheWipesWhatItLetsGo(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC))
	cache := newKeyCache(1, time.Minute, clk)
	wiped := func(opened *openedKey) bool {
		_, _, live := opened.take()
		return !live
	}
	first, second := openedFor(1), openedFor(2)
	cache.offer("a", cache.current(), first)
	cache.offer("b", cache.current(), second)
	if !wiped(first) {
		t.Error("a key evicted for capacity was not wiped")
	}
	clk.Advance(time.Minute)
	if _, found := cache.fetch("b"); found || !wiped(second) {
		t.Error("an expired key was served, or not wiped")
	}
	destroyed := openedFor(3)
	cache.offer("c", cache.current(), destroyed)
	cache.forget("c")
	if _, found := cache.fetch("c"); found || !wiped(destroyed) {
		t.Error("a destroyed key stayed cached, or was not wiped")
	}
	epoch := cache.current()
	crossed := openedFor(4)
	cache.forget("d")
	cache.offer("d", epoch, crossed)
	if _, found := cache.fetch("d"); found || !wiped(crossed) {
		t.Error("a fill that crossed a destruction was cached, or not wiped")
	}
	winner, loser := openedFor(5), openedFor(6)
	cache.offer("e", cache.current(), winner)
	cache.offer("e", cache.current(), loser)
	if held, found := cache.fetch("e"); !found || held != winner || !wiped(loser) || wiped(winner) {
		t.Error("a concurrent fill replaced the cached key, or the loser was not wiped")
	}
}

// TestKeyCacheDisabledHoldsNothing pins the zero-size cache: every lookup
// misses and every offer is wiped, so no opened key outlives its call.
func TestKeyCacheDisabledHoldsNothing(t *testing.T) {
	t.Parallel()
	cache := newKeyCache(0, 0, clock.System)
	opened := openedFor(7)
	cache.offer("a", cache.current(), opened)
	if _, found := cache.fetch("a"); found {
		t.Fatal("a disabled cache served a key")
	}
	if _, _, live := opened.take(); live {
		t.Fatal("a disabled cache left an offered key unwiped")
	}
	cache.forget("a")
}
