// Package cache — the L1/L2 chain.
package cache

import (
	"context"
	"errors"
	"slices"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/singleflight"
)

// minChainTiers is the smallest tier count that makes a chain mean anything.
// One tier is the tier itself wearing an extra layer of indirection, so
// building it is far more likely to be a mistake than an intention.
const minChainTiers int = 2

// chainStore reads near-to-far and writes far-to-near.
//
// # Nothing here is atomic, and that is stated rather than papered over
//
// A chain is two independent stores. There is no transaction across them, so
// every multi-tier operation has a window in which they disagree. The design
// choice is not whether that window exists but which side of it is safer:
//
//   - Set writes FAR first. A failure then leaves the near tier without the
//     new value, so the next read falls through and finds the old one — stale,
//     but consistent with what the far tier holds. Writing near first would
//     instead leave the near tier holding a value the far tier never accepted.
//   - Delete removes FAR first, and attempts EVERY tier even after a failure.
//     Near-first is strictly worse: if the far removal then failed, the next
//     read would miss in the near tier, hit in the far one, and PROMOTE the
//     value back — undoing the delete completely and permanently. Far-first
//     bounds the damage to one near-tier TTL, and the caller has an error.
//   - Fetch promotes on a far hit and never fails the read because the
//     promotion failed: the caller already has the value.
type chainStore[V any] struct {
	tiers          []tier[V]
	onPromoteError func(key string, err error)
	group          singleflight.Group[string, V]
}

// NewChain puts each store in front of the next: tiers[0] is the nearest and
// is consulted first, the last is the authority.
//
// Every tier MUST implement [corecache.EntryFetcher] and [corecache.Tagger].
// That is checked here, once, and refused with [CacheChainMisconfigured]
// rather than discovered per call — see that sentinel for the reasoning.
//
// The returned value is a [corecache.Store] and also implements
// [corecache.EntryFetcher], [corecache.Tagger] and [corecache.Loader].
//
// IFACE-PLUGIN: the concrete chain stays unexported behind this constructor.
func NewChain[V any](cfg ChainConfig, stores ...corecache.Store[V]) (chain corecache.Store[V], err error) {
	//: a chain of one is a store with extra indirection, and the caller almost
	//: certainly meant to pass a second tier.
	if len(stores) < minChainTiers {
		//: refuse rather than build a pointless wrapper.
		return nil, kerrs.Wrap(CacheChainMisconfigured, kerrs.WrapParams{},
			kerrs.Int("tiers", len(stores)),
			kerrs.String("problem", "fewer than two tiers"))
	}
	tiers := make([]tier[V], 0, len(stores))
	//: resolve every tier's capabilities up front; one that cannot answer the
	//: whole contract refuses the chain rather than degrading a later call.
	for position, store := range stores {
		built, tierErr := newTier(position, store)
		//: one unusable tier refuses the whole chain.
		if tierErr != nil {
			//: the typed refusal, naming the position.
			return nil, tierErr
		}
		tiers = append(tiers, built)
	}
	//: ready.
	return &chainStore[V]{tiers: tiers, onPromoteError: cfg.OnPromoteError}, nil
}

// newTier checks that store answers the whole tier contract.
func newTier[V any](position int, store corecache.Store[V]) (built tier[V], err error) {
	//: a nil tier would panic on the first read; refuse it while there is
	//: still a call stack that names the mistake.
	if store == nil {
		//: refuse.
		return tier[V]{}, chainRefusal(position, "nil tier")
	}
	fetcher, ok := store.(corecache.EntryFetcher[V])
	//: without FetchEntry a promotion loses TTL and tags — see the sentinel.
	if !ok {
		//: refuse.
		return tier[V]{}, chainRefusal(position, "does not implement EntryFetcher")
	}
	tagger, ok := store.(corecache.Tagger)
	//: without Tagger an invalidation cannot reach this tier at all.
	if !ok {
		//: refuse.
		return tier[V]{}, chainRefusal(position, "does not implement Tagger")
	}
	//: a complete tier.
	return tier[V]{store: store, entry: fetcher, tags: tagger}, nil
}

// chainRefusal builds the construction-time refusal for one tier.
func chainRefusal(position int, problem string) error {
	//: origin-wins keeps the sentinel's code; the fields say which tier.
	return kerrs.Wrap(CacheChainMisconfigured, kerrs.WrapParams{},
		kerrs.Int("position", position),
		kerrs.String("problem", problem))
}

// Fetch reads near-to-far and promotes a far hit into every nearer tier.
func (c *chainStore[V]) Fetch(ctx context.Context, key string) (value V, found bool, err error) {
	entry, ok, err := c.FetchEntry(ctx, key)
	//: a tier failure is the caller's problem; a miss is not.
	if err != nil || !ok {
		var zero V
		//: the miss, or the tier failure.
		return zero, false, err
	}
	//: the value the nearest holding tier had.
	return entry.Value, true, nil
}

// FetchEntry is Fetch with the entry, and is where promotion happens.
func (c *chainStore[V]) FetchEntry(ctx context.Context, key string) (entry corecache.EntryValue[V], found bool, err error) {
	//: walk near to far; the first tier that holds the key answers.
	for position, level := range c.tiers {
		stored, ok, fetchErr := level.entry.FetchEntry(ctx, key)
		//: a broken tier stops the walk: continuing would silently serve a
		//: staler answer from behind it and call that a hit.
		if fetchErr != nil {
			//: name the tier — a chain's caller holds one Store.
			return corecache.EntryValue[V]{}, false, tierFailure(position, "fetch", key, fetchErr)
		}
		//: keep looking further out.
		if !ok {
			//: this tier does not hold it.
			continue
		}
		//: fill every tier we walked past, with the entry's REMAINING TTL and
		//: its tags — so the promoted copy expires when the original would and
		//: stays reachable by InvalidateTag.
		c.promote(ctx, key, stored, position)
		//: the entry as the holding tier had it.
		return stored, true, nil
	}
	//: no tier held it.
	return corecache.EntryValue[V]{}, false, nil
}

// promote writes entry into every tier nearer than found.
//
// A failure here never fails the read: the caller already has the value, and
// turning a degraded cache into a degraded service would be a worse trade. It
// is reported through ChainConfig.OnPromoteError instead — which is nil by
// default, and a nil hook is exactly how a near tier that rejects everything
// stays invisible.
func (c *chainStore[V]) promote(ctx context.Context, key string, entry corecache.EntryValue[V], found int) {
	//: outward-in, the same direction Set writes: the nearest tier is filled
	//: LAST, so a reader racing the promotion never finds a near hit backed by
	//: a tier that has not been written yet.
	for position, level := range slices.Backward(c.tiers[:found]) {
		//: one write per tier we walked past to reach the holder.
		if err := level.store.Set(ctx, key, entry); err != nil {
			//: observable, never fatal.
			c.reportPromoteError(key, tierFailure(position, "promote", key, err))
		}
	}
}

// reportPromoteError hands a failed promotion to the configured observer.
func (c *chainStore[V]) reportPromoteError(key string, err error) {
	//: no observer means the caller chose not to watch; say so in the docs,
	//: not by inventing a destination.
	if c.onPromoteError == nil {
		//: dropped.
		return
	}
	//: caller code; a panic propagates unchanged.
	c.onPromoteError(key, err)
}

// Set writes far-to-near and stops at the first failure. See the type comment.
func (c *chainStore[V]) Set(ctx context.Context, key string, entry corecache.EntryValue[V]) error {
	//: the authority first: a near tier holding a value the far tier refused is
	//: a lie that outlives the error.
	for position, level := range slices.Backward(c.tiers) {
		//: one write per tier, farthest to nearest.
		if err := level.store.Set(ctx, key, entry); err != nil {
			//: stop — the nearer tiers keep whatever they had.
			return tierFailure(position, "set", key, err)
		}
	}
	//: written everywhere.
	return nil
}

// Delete removes key from EVERY tier, far-to-near, and does not stop at the
// first failure. See the type comment for why near-first would be worse.
func (c *chainStore[V]) Delete(ctx context.Context, key string) error {
	var failures []error
	//: attempt every tier: skipping the rest after one failure leaves the value
	//: fetchable from a tier nothing ever tried.
	for position, level := range slices.Backward(c.tiers) {
		//: one delete per tier, farthest to nearest.
		if err := level.store.Delete(ctx, key); err != nil {
			//: collect and keep going.
			failures = append(failures, tierFailure(position, "delete", key, err))
		}
	}
	//: errors.Join yields a genuine nil for an empty slice.
	return errors.Join(failures...)
}

// InvalidateTag invalidates in every tier, far-to-near, and reports the total.
//
// The total counts REMOVALS, not distinct keys: an entry present in two tiers
// counts twice. Deduplicating would require the chain to know which keys each
// tier dropped, which is a list no tier returns — and a number that is quietly
// approximate is worse than one whose unit is stated.
func (c *chainStore[V]) InvalidateTag(ctx context.Context, tag string) (removed int, err error) {
	var failures []error
	total := 0
	//: far first: a near tier cleared before the far one could repopulate itself
	//: from the far tier on the very next read.
	for position, level := range slices.Backward(c.tiers) {
		//: one invalidation per tier, farthest to nearest.
		dropped, invalidateErr := level.tags.InvalidateTag(ctx, tag)
		total += dropped
		//: attempt every tier — the same reason Delete does.
		if invalidateErr != nil {
			//: collect and keep going.
			failures = append(failures, tierFailure(position, "invalidate", tag, invalidateErr))
		}
	}
	//: the count is real even when a tier failed; it says what WAS removed.
	return total, errors.Join(failures...)
}

// Load fills a chain miss once per key per process, writing through every
// tier. It does NOT coordinate across replicas — see [corecache.Loader].
func (c *chainStore[V]) Load(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: a hit must not enter the group: joining costs a mutex and a channel.
	if hit, ok, fetchErr := c.Fetch(ctx, key); fetchErr == nil && ok {
		//: served from a tier.
		return hit, nil
	}
	filled, _, groupErr := c.group.Do(ctx, key, func(callCtx context.Context) (V, error) {
		//: exactly one goroutine per key reaches here.
		return c.fillOnce(callCtx, key, fill)
	})
	//: the shared outcome, or this caller's own cancellation.
	return filled, groupErr
}

// fillOnce is the body of the deduplicated chain fill.
func (c *chainStore[V]) fillOnce(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: a caller arriving just after a previous fill published would lead a NEW
	//: call for a key the chain now holds — re-checking turns that into a hit.
	if hit, ok, fetchErr := c.Fetch(ctx, key); fetchErr == nil && ok {
		//: someone else's fill already answered this.
		return hit, nil
	}
	entry, fillErr := fill(ctx)
	//: a failed fill stores nothing, in any tier.
	if fillErr != nil {
		var zero V
		//: the typed refusal, or the origin's own error.
		return zero, fillFailure(key, fillErr)
	}
	//: write-through: the fill's cost is paid once, so every tier gets it.
	if setErr := c.Set(ctx, key, entry); setErr != nil {
		var zero V
		//: the tier failure, unmodified.
		return zero, setErr
	}
	//: the freshly filled value.
	return entry.Value, nil
}

// tierFailure labels a tier's error with its position and the operation.
//
// An untyped cause is labelled AND kept, the rule fillFailure follows: it used
// to survive only as a string field, so a tier returning a backend's own
// sentinel came back as an error for which errors.Is on that sentinel was
// false. A typed cause still yields CACHE_TIER_FAILED as the code — the
// position is the information the caller cannot reconstruct — which wrapping
// it would lose to origin-wins, so it keeps travelling as fields.
func tierFailure(position int, operation, key string, cause error) error {
	fields := []kerrs.FieldValue{
		kerrs.Int("position", position),
		kerrs.String("operation", operation),
		kerrs.String("key", key),
		kerrs.String("cause", cause.Error()),
	}
	//: a typed cause would take over the code if wrapped; label it instead.
	if _, typed := cause.(*kerrs.Error); typed {
		//: CACHE_TIER_FAILED, the cause's text in a field.
		return kerrs.Wrap(CacheTierFailed, kerrs.WrapParams{}, fields...)
	}
	//: an untyped cause, IN the chain: HasCode sees CACHE_TIER_FAILED and
	//: errors.Is sees the cause. The identity is read from the sentinel so it
	//: cannot drift from it.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:    CacheTierFailed.Code(),
		Reason:  CacheTierFailed.Reason(),
		Public:  CacheTierFailed.Public(),
		Private: CacheTierFailed.Private(),
	}, fields...)
}
