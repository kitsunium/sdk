// Package cache — the tagged, stampede-protected in-memory store.
package cache

import (
	"context"
	"slices"
	"sync"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	kcache "github.com/kitsunium/sdk/internal/kernel/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/singleflight"
)

// memoryStore is the in-process store: the kernel LRU+TTL primitive, a tag
// index beside it, and a singleflight group in front of the fill path.
//
// # One mutex, taken for reads too
//
// Everything is under a single sync.Mutex, and Fetch takes it exclusively.
// That is not an oversight: the primitive's own Fetch already takes a WRITE
// lock, because a hit promotes the entry and moves counters, so there was
// never a shared-read path to lose. Serialising the tag index on the SAME lock
// is what makes the index and the entries agree at every observable moment —
// with two locks, a key would be briefly live but unindexed, and an
// InvalidateTag crossing that window would report success while leaving the
// entry fetchable.
//
// # onEvict must never lock
//
// The primitive calls OnEvict after releasing its own lock, but still on the
// caller's goroutine — the goroutine that is inside one of the methods below,
// holding s.mu. A callback that took s.mu would deadlock outright, sync.Mutex
// not being reentrant. So onEvict only APPENDS to s.evicted, a field that is
// by construction reachable solely while s.mu is held, and the surrounding
// method drains it. The invariant is short and must stay that way: no path
// into the primitive runs without s.mu, and onEvict takes no lock.
type memoryStore[V any] struct {
	mu   sync.Mutex
	lru  *kcache.Cache[string, record[V]]
	tags *tagIndex
	clk  clock.Clock
	//: defaultTTL is applied to an entry whose own TTL is zero.
	defaultTTL time.Duration
	//: evicted collects the keys the primitive dropped during the call in
	//: progress. Guarded by mu; see the type comment.
	evicted []string
	//: group collapses concurrent Load misses on one key into one fill.
	group singleflight.Group[string, V]
}

// NewMemory builds an in-process cache store from cfg.
//
// The returned value is a [corecache.Store]. It also implements
// [corecache.EntryFetcher], [corecache.Tagger] and [corecache.Loader]; reach
// them by type assertion, which is the ADR 0039 way a port grows without
// breaking anyone who implemented it.
//
// A non-positive MaxEntries is REFUSED, not defaulted. ADR 0031 splits on
// whether the SDK can supply a value without inventing the caller's
// requirement, and a cache capacity is the requirement: it is the whole
// statement of how much memory the caller is willing to spend. Worse, the
// value the primitive gives a zero — unbounded — is the dangerous reading, and
// it is dangerous twice over here, because an unbounded store also grows an
// unbounded tag index.
//
// IFACE-PLUGIN: the concrete store stays unexported behind this constructor.
func NewMemory[V any](cfg MemoryConfig) (store corecache.Store[V], err error) {
	//: refuse a configuration that cannot be honoured, before anything exists.
	if cfgErr := cfg.validate(); cfgErr != nil {
		//: the typed ADR 0031 refusal.
		return nil, cfgErr
	}
	built := &memoryStore[V]{
		tags:       newTagIndex(),
		clk:        cfg.clockOrSystem(),
		defaultTTL: cfg.DefaultTTL,
	}
	//: DefaultTTL is applied by this store, not by the primitive: the domain
	//: distinguishes "zero means the default" from "NoExpiry means no
	//: deadline", and the primitive has only one zero.
	built.lru = kcache.NewCache(kcache.Config[string, record[V]]{
		MaxEntries: cfg.MaxEntries,
		Clock:      built.clk,
		OnEvict:    built.onEvict,
	})
	//: ready; every capability is reachable by type assertion.
	return built, nil
}

// onEvict records a key the primitive dropped by capacity or expiry. It takes
// NO lock — see the memoryStore type comment.
func (s *memoryStore[V]) onEvict(key string, _ record[V]) {
	//: the surrounding method holds s.mu and will drain this.
	s.evicted = append(s.evicted, key)
}

// drainEvicted unlinks every key the primitive just dropped from the tag
// index. Caller holds mu.
func (s *memoryStore[V]) drainEvicted() {
	//: the overwhelmingly common case is that nothing was evicted.
	if len(s.evicted) == 0 {
		//: nothing to unlink.
		return
	}
	//: unlink every key the primitive just dropped; leaving one behind grows
	//: the reverse index forever and makes it name entries that do not exist.
	for _, key := range s.evicted {
		//: one unlink per evicted key.
		s.tags.remove(key)
	}
	//: keep the backing array; this scratch buffer is reused every call.
	s.evicted = s.evicted[:0]
}

// Fetch returns the value under key. It MUTATES the store — recency, counters,
// lazy expiry — which is why the verb is Fetch.
//
// ctx is unnamed on purpose: this store never blocks, so there is no point at
// which a cancellation could be honoured, and consulting ctx would be a check
// that can only ever pass. The parameter stays because the PORT needs it for
// tiers that do block.
func (s *memoryStore[V]) Fetch(_ context.Context, key string) (value V, found bool, err error) {
	rec, ok := s.fetchRecord(key)
	//: a miss is not an error — a cache that does not hold something has done
	//: nothing wrong.
	if !ok {
		var zero V
		//: the miss.
		return zero, false, nil
	}
	//: the live value.
	return rec.value, true, nil
}

// FetchEntry returns the whole entry, with the REMAINING lifetime rather than
// the one originally supplied. See [record] for why that distinction matters.
func (s *memoryStore[V]) FetchEntry(_ context.Context, key string) (entry corecache.EntryValue[V], found bool, err error) {
	rec, ok := s.fetchRecord(key)
	//: a miss yields the zero entry.
	if !ok {
		//: nothing stored under this key.
		return corecache.EntryValue[V]{}, false, nil
	}
	//: tags are copied out: the index owns the slice the store holds, and a
	//: caller free to append to it could corrupt another entry's tag list.
	tags := slices.Clone(rec.tags)
	//: the entry as it stands NOW.
	return corecache.EntryValue[V]{Value: rec.value, TTL: s.remaining(rec), Tags: tags}, true, nil
}

// remaining converts a stored deadline into a TTL a caller can hand back to
// Set. Caller holds nothing — it only reads the clock.
func (s *memoryStore[V]) remaining(rec record[V]) time.Duration {
	//: an entry with no deadline keeps having none wherever it is promoted.
	if rec.expireAt.IsZero() {
		//: the explicit "no deadline" spelling.
		return corecache.NoExpiry
	}
	left := rec.expireAt.Sub(s.clk.Now())
	//: a deadline that has passed but has not yet been reaped must not become
	//: a zero TTL, which Set would read as "use the default" and so REVIVE.
	if left <= 0 {
		//: the smallest lifetime that still means "expiring".
		return time.Nanosecond
	}
	//: what is actually left.
	return left
}

// fetchRecord is the one place that touches the primitive on a read path.
func (s *memoryStore[V]) fetchRecord(key string) (rec record[V], found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a hit promotes and a miss may reap an expired entry — both mutate, and
	//: a reap fires onEvict, which is why the drain follows.
	stored, ok := s.lru.Fetch(key)
	s.drainEvicted()
	//: the record as the primitive holds it.
	return stored, ok
}

// Set stores entry under key. An entry the store cannot represent is REFUSED
// rather than reinterpreted — see [validateEntry].
func (s *memoryStore[V]) Set(_ context.Context, key string, entry corecache.EntryValue[V]) error {
	//: refuse before anything is written.
	if refusal := validateEntry(key, entry); refusal != nil {
		//: the typed rejection.
		return refusal
	}
	ttl := primitiveTTL(entry.TTL, s.defaultTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the deadline is computed once, here, so FetchEntry can report what is
	//: left without asking the primitive for something it does not expose.
	var expireAt time.Time
	//: only a positive lifetime produces a deadline; a zero one has already
	//: been resolved by primitiveTTL and means "no deadline".
	if ttl > 0 {
		//: an absolute deadline against the injected clock.
		expireAt = s.clk.Now().Add(ttl)
	}
	//: index first: add replaces this key's previous tags, so an overwrite
	//: cannot leave the old ones pointing here. It hands back the copy IT
	//: owns, which the record then shares — one slice, so the entry and the
	//: index can never disagree, and the caller's own slice never reaches in.
	owned := s.tags.add(key, entry.Tags)
	s.lru.SetTTL(key, record[V]{value: entry.Value, expireAt: expireAt, tags: owned}, ttl)
	//: the insert may have evicted OTHER keys (never this one — a fresh entry
	//: is the most-recently-used, and the victim is the least). Unlink them.
	s.drainEvicted()
	//: stored.
	return nil
}

// Delete removes key. Idempotent: a key that names nothing reports nil.
func (s *memoryStore[V]) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the primitive's Delete deliberately does NOT fire OnEvict, so the index
	//: is unlinked here rather than through the drain.
	s.lru.Delete(key)
	s.tags.remove(key)
	//: removed, or was never there.
	return nil
}

// InvalidateTag removes every entry carrying tag.
//
// The cost is O(k) in the entries carrying the tag plus O(T) per entry in that
// entry's own tags — NOT O(N) in the store. See `tagindex.go` for why the
// reverse index exists and `BENCH.md` for the measurement at two store sizes.
//
// removed counts entries dropped from the store. An entry whose TTL had passed
// but which nothing had yet touched is counted: expiry is lazy, so such an
// entry still occupies capacity, and removing it is a removal.
func (s *memoryStore[V]) InvalidateTag(_ context.Context, tag string) (removed int, err error) {
	//: an empty tag names a set nobody can ask for.
	if tag == "" {
		//: refuse rather than sweep an accidental bucket.
		return 0, kerrs.Wrap(corecache.CacheEntryRejected, kerrs.WrapParams{},
			kerrs.String("part", "tag"), kerrs.String("problem", "empty"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a COPY of the bucket: the loop below deletes from the very map it would
	//: otherwise be ranging over.
	keys := s.tags.keysFor(tag)
	//: O(k) in the keys carrying the tag — never O(N) in the store.
	for _, key := range keys {
		//: Delete does not fire OnEvict, so unlink explicitly.
		s.lru.Delete(key)
		s.tags.remove(key)
	}
	//: every indexed key was in the store, so the count is exact.
	return len(keys), nil
}

// Load returns the value for key, running fill at most once across all
// concurrent callers of this key IN THIS PROCESS. It does NOT coordinate
// across replicas — see [corecache.Loader].
func (s *memoryStore[V]) Load(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: the hit path must not enter the group at all: joining costs a mutex and
	//: a channel, and a hit needs neither. A tier that FAILED is not a hit and
	//: falls through to the fill, which is what a cache is for.
	if hit, ok, fetchErr := s.Fetch(ctx, key); fetchErr == nil && ok {
		//: served without a fill.
		return hit, nil
	}
	filled, _, groupErr := s.group.Do(ctx, key, func(callCtx context.Context) (V, error) {
		//: exactly one goroutine per key reaches here.
		return s.fillOnce(callCtx, key, fill)
	})
	//: the shared outcome, or this caller's own cancellation.
	return filled, groupErr
}

// fillOnce is the body of the deduplicated call: re-check, fill, store.
func (s *memoryStore[V]) fillOnce(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: a caller that arrived just after a previous fill published would lead a
	//: NEW call for a key the cache now holds. Re-checking turns that second
	//: stampede — the one right after the first fill lands — into a hit.
	if hit, ok, fetchErr := s.Fetch(ctx, key); fetchErr == nil && ok {
		//: someone else's fill already answered this.
		return hit, nil
	}
	entry, fillErr := fill(ctx)
	//: a failed fill stores NOTHING: caching a failure turns one bad minute at
	//: the origin into a full TTL of confident wrong answers.
	if fillErr != nil {
		var zero V
		//: the typed refusal, or the origin's own error.
		return zero, fillFailure(key, fillErr)
	}
	//: a fill that produced an unstorable entry is the caller's bug, and it
	//: surfaces as CACHE_ENTRY_REJECTED rather than as a silent non-cache.
	if setErr := s.Set(ctx, key, entry); setErr != nil {
		var zero V
		//: the rejection, unmodified.
		return zero, setErr
	}
	//: the freshly filled value.
	return entry.Value, nil
}
