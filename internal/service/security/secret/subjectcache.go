// Package secret — the bounded cache of opened subject keys, and the order it
// keeps between filling an entry and destroying a key.
package secret

import (
	"sync"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// openedKey is one subject's data key, opened: the AEAD key derived from it
// and its identifier. The data key itself is cleared as soon as both are
// derived, so the opened key is all that stays in memory.
//
// It may be shared by every call that finds it in the cache, so it hands out
// COPIES under its mutex and is wiped under the same mutex: a call never holds
// bytes an eviction or a destruction clears beneath it, which is how a box
// would otherwise come to be sealed under a zeroed key.
type openedKey struct {
	// mu orders take against wipe.
	mu sync.Mutex
	// seal is the derived AEAD key; nil once wiped.
	seal []byte
	// id identifies the data key in every box it seals.
	id [keyIDLen]byte
}

// take returns a copy of the AEAD key, which the caller zeroizes, and the
// identifier. It reports false once the key was wiped — evicted or destroyed
// between the lookup and this call — and the caller then reads the store.
func (o *openedKey) take() (key corecrypto.Key, id [keyIDLen]byte, live bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	//: wiped: an eviction or a destruction got here first.
	if o.seal == nil {
		//: not live; the caller reloads.
		return corecrypto.Key{}, id, false
	}
	key, keyErr := corecrypto.NewKey(o.seal)
	//: seal is always one key long; a failure is treated as a wiped key.
	if keyErr != nil {
		//: not live; the caller reloads, and the reload reports the fault.
		return corecrypto.Key{}, id, false
	}
	//: NewKey copied: the caller's key is its own.
	return key, o.id, true
}

// wipe clears the AEAD key in place. It is idempotent.
func (o *openedKey) wipe() {
	o.mu.Lock()
	defer o.mu.Unlock()
	clear(o.seal)
	o.seal = nil
}

// keyCache keeps opened keys for a bounded time, least recently used evicted
// first, and wipes each one it lets go.
//
// A destruction must reach the cache, and it has one race to beat: a fill that
// read the store BEFORE the destruction deleted the key, and offers what it
// opened AFTER the destruction cleared the cache. The epoch closes it. A fill
// reads the epoch before it reads the store; a destruction deletes the key from
// the store first and only then advances the epoch, under the mutex offer takes
// to compare it; so an offer that crossed a destruction finds a different
// epoch and caches nothing. The key it opened is still used once, by the call
// that opened it, which began before the destruction ended.
type keyCache struct {
	// mu orders offer against forget, and guards epoch.
	mu sync.Mutex
	// epoch advances at every destruction.
	epoch uint64
	// entries holds the opened keys; nil when the cache is disabled.
	entries *kcache.Cache[string, *openedKey]
}

// newKeyCache returns a cache of at most size opened keys, each kept for ttl.
// A size of zero caches nothing: every lookup misses and every offer wipes.
func newKeyCache(size int, ttl time.Duration, clk clock.Clock) *keyCache {
	//: no cache: the store is read on every call.
	if size == 0 {
		//: a cache that holds nothing.
		return &keyCache{}
	}
	//: expiry and eviction both wipe; Delete does not fire it, forget wipes.
	return &keyCache{entries: kcache.NewCache(kcache.Config[string, *openedKey]{
		MaxEntries: size,
		DefaultTTL: ttl,
		Clock:      clk,
		OnEvict:    func(_ string, opened *openedKey) { opened.wipe() },
	})}
}

// fetch returns the opened key cached for subject, if any is live.
func (c *keyCache) fetch(subject string) (opened *openedKey, found bool) {
	//: a disabled cache holds nothing.
	if c.entries == nil {
		//: a miss.
		return nil, false
	}
	//: an expired entry is evicted — and wiped — by the lookup itself.
	return c.entries.Fetch(subject)
}

// current returns the epoch a fill must present to offer what it opens.
func (c *keyCache) current() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: read under the mutex that forget advances it under.
	return c.epoch
}

// offer caches opened under subject, unless a destruction happened since the
// fill read epoch, or a concurrent fill cached subject first; either way the
// key not kept is wiped.
func (c *keyCache) offer(subject string, epoch uint64, opened *openedKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: disabled, or a destruction crossed this fill: keep nothing.
	if c.entries == nil || c.epoch != epoch {
		opened.wipe()
		//: not cached.
		return
	}
	//: a concurrent fill won: its key and this one are the same key.
	if _, present := c.entries.Fetch(subject); present {
		opened.wipe()
		//: the cached one stays.
		return
	}
	c.entries.Set(subject, opened)
}

// forget drops and wipes whatever is cached for subject, and advances the
// epoch so that no fill already under way caches it again.
func (c *keyCache) forget(subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	//: a disabled cache has nothing to drop.
	if c.entries == nil {
		//: done.
		return
	}
	opened, present := c.entries.Fetch(subject)
	//: nothing cached, or already expired and wiped by the lookup.
	if !present {
		//: done.
		return
	}
	c.entries.Delete(subject)
	opened.wipe()
}
