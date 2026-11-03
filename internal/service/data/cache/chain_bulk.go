package cache

import (
	"context"
	"errors"
	"slices"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// Expire sweeps every tier that implements [corecache.Expirer], far to near,
// and reports the total of removals — an entry expired in two tiers counts
// twice, as InvalidateTag counts it (#1059). A tier without the sibling is
// skipped: its expiry stays lazy. Every tier is attempted even after a
// failure.
func (c *chainStore[V]) Expire(ctx context.Context) (removed int, err error) {
	var failures []error
	//: far first, the order every other chain write takes.
	for position, level := range slices.Backward(c.tiers) {
		expirer, ok := level.store.(corecache.Expirer)
		//: a tier that cannot sweep expires lazily, as before.
		if !ok {
			continue
		}
		dropped, expireErr := expirer.Expire(ctx)
		removed += dropped
		//: attempt every tier — the same reason Delete does.
		if expireErr != nil {
			//: collect and keep going.
			failures = append(failures, tierFailure(position, "expire", "", expireErr))
		}
	}
	//: the count is real even when a tier failed.
	return removed, errors.Join(failures...)
}

// FetchMany reads near to far with the missing set shrinking (#840): each
// tier gets ONE pass, over the keys no nearer tier held, and the hits of a far
// tier are promoted into every nearer one as a batch, under the same write
// guard a single promotion takes (#460). A broken tier stops the walk, as it
// stops Fetch's.
func (c *chainStore[V]) FetchMany(ctx context.Context, keys []string) (map[string]V, error) {
	// read before any tier is: a write announced after this read makes the
	// promotion of what the walk finds stale.
	seen := c.generation.Load()
	found := make(map[string]V, len(keys))
	missing := distinct(keys)
	//: near to far, until every key is found or the tiers run out.
	for position, level := range c.tiers {
		//: nothing left to look for.
		if len(missing) == 0 {
			break
		}
		hits, still, fetchErr := fetchPass(ctx, position, level, missing)
		//: a broken tier: serving what lies behind it would be a staler answer.
		if fetchErr != nil {
			//: the tier failure, naming its position.
			return nil, fetchErr
		}
		//: the values, and the promotion of this tier's hits.
		for key, entry := range hits {
			found[key] = entry.Value
		}
		//: the nearest tier has nothing nearer to fill.
		if position > 0 && len(hits) > 0 {
			c.promoteMany(ctx, hits, position, seen)
		}
		missing = still
	}
	//: the hits of every tier.
	return found, nil
}

// fetchPass is one tier's pass over the missing keys: the entries it holds,
// and the keys it does not.
func fetchPass[V any](ctx context.Context, position int, level tier[V], missing []string) (hits map[string]corecache.EntryValue[V], still []string, err error) {
	hits = make(map[string]corecache.EntryValue[V], len(missing))
	//: one FetchEntry per missing key, on this tier only.
	for _, key := range missing {
		stored, ok, fetchErr := level.entry.FetchEntry(ctx, key)
		//: a broken tier stops the pass.
		if fetchErr != nil {
			//: name the tier.
			return nil, nil, tierFailure(position, "fetch", key, fetchErr)
		}
		//: further out, maybe.
		if !ok {
			still = append(still, key)
			continue
		}
		hits[key] = stored
	}
	//: this tier's answer.
	return hits, still, nil
}

// promoteMany writes a far tier's hits into every nearer tier, as promote
// writes one: nothing when a write is running or began since seen was read.
func (c *chainStore[V]) promoteMany(ctx context.Context, hits map[string]corecache.EntryValue[V], found int, seen uint64) {
	c.promoting.RLock()
	defer c.promoting.RUnlock()
	//: a write ran or runs since the far reads: what was read may be gone.
	if c.writing.Load() != 0 || c.generation.Load() != seen {
		//: served, not promoted; the next read falls through.
		return
	}
	//: outward-in, the direction Set writes.
	for position, level := range slices.Backward(c.tiers[:found]) {
		//: one batch per tier; a failure is observable, never fatal.
		for key, err := range setBatch(ctx, level.store, hits) {
			c.reportPromoteError(key, tierFailure(position, "promote", key, err))
		}
	}
}

// setBatch stores entries into store — through its BulkSetter when it has
// one, one Set per entry otherwise — and returns the failures by key.
func setBatch[V any](ctx context.Context, store corecache.Store[V], entries map[string]corecache.EntryValue[V]) map[string]error {
	var failures map[string]error
	//: one call for the batch.
	if bulk, ok := store.(corecache.BulkSetter[V]); ok {
		//: all or nothing on the tier: a refusal fails every key.
		if err := bulk.SetMany(ctx, entries); err != nil {
			failures = make(map[string]error, len(entries))
			for key := range entries {
				failures[key] = err
			}
		}
		//: the batch's outcome.
		return failures
	}
	//: a tier without the sibling, one write per entry.
	for key, entry := range entries {
		//: collect and keep going.
		if err := store.Set(ctx, key, entry); err != nil {
			//: built on the first failure.
			if failures == nil {
				failures = make(map[string]error, len(entries))
			}
			failures[key] = err
		}
	}
	//: the failures, if any.
	return failures
}

// SetMany writes every entry far to near (#840). Every entry is validated
// first: one any store would refuse fails the call and nothing is written. A
// tier's failure stops the walk, as Set's does, and the nearer tiers keep
// what they had.
func (c *chainStore[V]) SetMany(ctx context.Context, entries map[string]corecache.EntryValue[V]) error {
	//: all or nothing: every refusal is found before the first write.
	for key, entry := range entries {
		//: one unstorable entry refuses the batch.
		if refusal := validateEntry(key, entry); refusal != nil {
			//: the typed rejection; nothing was written.
			return refusal
		}
	}
	c.announce()
	defer c.finish()
	//: the authority first.
	for position, level := range slices.Backward(c.tiers) {
		//: the first failing key names the tier's failure.
		for key, err := range setBatch(ctx, level.store, entries) {
			//: stop — the nearer tiers keep whatever they had.
			return tierFailure(position, "set", key, err)
		}
	}
	//: written everywhere.
	return nil
}

// LoadRefreshing is Load with refresh-ahead (#838): a hit whose remaining
// lifetime is below before is returned at once and ONE background fill per
// key writes through every tier. A chain keeps no negative entry — a tier has
// no way to store one — so a fill answering corecache.Absent is returned and
// nothing is stored.
func (c *chainStore[V]) LoadRefreshing(ctx context.Context, key string, fill corecache.Fill[V], before time.Duration) (value V, err error) {
	//: a window that is not positive has two readings — never, or always.
	if before <= 0 {
		var zero V
		//: refuse rather than pick one.
		return zero, refusedWindow(before)
	}
	entry, ok, fetchErr := c.FetchEntry(ctx, key)
	//: a miss, or a tier that failed, fills on the caller's path as Load does.
	if fetchErr != nil || !ok {
		//: the deduplicated fill.
		return c.Load(ctx, key, fill)
	}
	//: a deadline inside the window, and no refresh of this key running.
	if entry.TTL > 0 && entry.TTL < before && c.claimRefresh(key) {
		go c.refresh(ctx, key, fill, entry.TTL)
	}
	//: the cached value.
	return entry.Value, nil
}

// claimRefresh records that a refresh of key is starting and reports whether
// none was running.
func (c *chainStore[V]) claimRefresh(key string) bool {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	//: one refresh per key at a time.
	if _, running := c.refreshing[key]; running {
		//: already started.
		return false
	}
	//: built on the first refresh.
	if c.refreshing == nil {
		c.refreshing = make(map[string]struct{})
	}
	c.refreshing[key] = struct{}{}
	//: this caller starts it.
	return true
}

// refresh is the chain's background fill: bounded by the entry's remaining
// lifetime, written through every tier as Set writes, nothing stored on a
// failure.
func (c *chainStore[V]) refresh(ctx context.Context, key string, fill corecache.Fill[V], left time.Duration) {
	//: released whatever the fill does.
	defer c.releaseRefresh(key)
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), left)
	defer cancel()
	entry, fillErr := fill(bounded)
	//: a failure leaves the entry to expire.
	if fillErr != nil {
		//: nothing stored.
		return
	}
	//: write-through. A tier that refuses it leaves the nearer tiers what
	//: they had, as a failed Set does, and the entry to expire; the caller
	//: was answered before the refresh began, so nobody is left to tell.
	if setErr := c.Set(bounded, key, entry); setErr != nil {
		//: the old entry serves until its deadline.
		return
	}
}

// releaseRefresh forgets the running refresh of key.
func (c *chainStore[V]) releaseRefresh(key string) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	//: the next hit inside the window may start another.
	delete(c.refreshing, key)
}

// distinct returns keys without duplicates, in their first order.
func distinct(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	//: the first occurrence of each.
	for _, key := range keys {
		//: a repeat.
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	//: the keys, once each.
	return out
}
