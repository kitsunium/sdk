package cache

import (
	"context"
	"hash/maphash"
	"slices"
	"strconv"
	"sync"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
	"github.com/kitsunium/sdk/internal/kernel/concur/singleflight"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// memoryStore is the in-process store: one or more stripes, each the kernel
// primitive and a tag index under a mutex of its own, and a singleflight group
// in front of the fill path.
//
// # One mutex per stripe, taken for reads too
//
// A key lives in exactly one stripe, picked by a seeded hash of the key, and
// everything that stripe holds is under its own sync.Mutex, which Fetch takes
// exclusively. That is not an oversight: the primitive's own Fetch already
// takes a WRITE lock, because a hit moves the entry and counters, so there was
// never a shared-read path to lose. Serialising the stripe's tag index on the
// SAME lock is what makes the index and the entries agree at every observable
// moment — with two locks, a key would be briefly live but unindexed, and an
// InvalidateTag crossing that window would report success while leaving the
// entry fetchable. The argument holds per stripe because a key is in one.
// Operations on keys of different stripes never wait for each other (#808).
//
// # onEvict must never lock
//
// The primitive calls OnEvict after releasing its own lock, but still on the
// caller's goroutine — the goroutine that is inside one of the methods below,
// holding the stripe's mutex. A callback that took it would deadlock outright,
// sync.Mutex not being reentrant. So onEvict only APPENDS to the stripe's
// evicted, a field that is by construction reachable solely while that mutex
// is held, and the surrounding method drains it. The invariant is short and
// must stay that way: no path into a primitive runs without its stripe's
// mutex, and onEvict takes no lock.
type memoryStore[V any] struct {
	stripes []*memoryStripe[V]
	// seed keys the hash that picks a key's stripe.
	seed maphash.Seed
	clk  clock.Clock
	//: defaultTTL is applied to an entry whose own TTL is zero.
	defaultTTL time.Duration
	// weigh measures a value against MaxBytes; nil when the store bounds no
	// bytes.
	weigh func(value V) int64
	// maxWeight is one stripe's share of MaxBytes: an entry heavier is refused.
	maxWeight int64
	//: group collapses concurrent Load misses on one key into one fill.
	group singleflight.Group[string, V]
	// waiter is what LoadLeased waits on between two reads (#830): clk when
	// it can wait, the system clock otherwise.
	waiter clock.Waiter
	// leasePoll is LoadLeased's step while another process holds the lease.
	leasePoll time.Duration
}

// memoryStripe is one stripe: a primitive, its tag index, and their mutex.
type memoryStripe[V any] struct {
	mu   sync.Mutex
	lru  *kcache.Cache[string, record[V]]
	tags *tagIndex
	//: evicted collects the keys the primitive dropped during the call in
	//: progress. Guarded by mu; see the memoryStore type comment.
	evicted []string
	//: refreshing holds the keys a background refresh is running for (#838).
	//: Guarded by mu.
	refreshing map[string]struct{}
}

// NewMemory builds an in-process cache store from cfg.
//
// The returned value is a [corecache.Store]. It also implements
// [corecache.EntryFetcher], [corecache.Tagger], [corecache.Loader],
// [corecache.Expirer], [corecache.BulkFetcher], [corecache.BulkSetter],
// [corecache.Refresher] and [corecache.Leaser]; reach them by type
// assertion, which is the ADR 0039 way a port grows without breaking anyone
// who implemented it.
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
	//: a byte bound needs a weigher, which only NewWeighedMemory takes.
	if cfg.MaxBytes > 0 {
		//: refuse rather than bound nothing.
		return nil, misconfigured("MaxBytes", kerrs.Int64("value", cfg.MaxBytes), kerrs.String("problem", "no weigher: use NewWeighedMemory"))
	}
	//: the store, with no byte bound.
	return newMemory[V](cfg, nil)
}

// NewWeighedMemory builds an in-process cache store bounded by MaxBytes as
// well as MaxEntries, each value measured by weigh when it is stored (#839).
// It is [NewMemory] in every other respect. A nil weigh, or a MaxBytes that is
// not positive, is refused with [corecache.CacheMisconfigured]: a weigher with
// no budget, or a budget with nothing to measure it, is a half-stated bound.
func NewWeighedMemory[V any](cfg MemoryConfig, weigh func(value V) int64) (store corecache.Store[V], err error) {
	//: both halves of the bound, or neither.
	if weigh == nil || cfg.MaxBytes <= 0 {
		//: refuse rather than guess the missing half.
		return nil, misconfigured("MaxBytes", kerrs.Int64("value", cfg.MaxBytes), kerrs.Bool("weigh", weigh != nil))
	}
	//: the store, bounded in bytes too.
	return newMemory(cfg, weigh)
}

// newMemory builds the store; weigh is nil when no byte bound applies.
func newMemory[V any](cfg MemoryConfig, weigh func(value V) int64) (store corecache.Store[V], err error) {
	//: refuse a configuration that cannot be honoured, before anything exists.
	if cfgErr := cfg.validate(); cfgErr != nil {
		//: the typed ADR 0031 refusal.
		return nil, cfgErr
	}
	count := cfg.stripeCount()
	built := &memoryStore[V]{
		stripes:    make([]*memoryStripe[V], count),
		seed:       maphash.MakeSeed(),
		clk:        cfg.clockOrSystem(),
		defaultTTL: cfg.DefaultTTL,
		weigh:      weigh,
		leasePoll:  pollOrDefault(cfg.LeasePoll),
	}
	built.waiter = waiterOf(built.clk)
	//: every stripe holds its share of the bounds, the first stripes the
	//: remainder; a stripe never gets zero entries, which the primitive reads
	//: as none.
	for at := range built.stripes {
		built.stripes[at] = built.newStripe(cfg, shareOf(cfg.MaxEntries, count, at), shareOf64(cfg.MaxBytes, count, at))
	}
	//: the smallest share: an entry it cannot hold is refused at Set.
	built.maxWeight = shareOf64(cfg.MaxBytes, count, count-1)
	//: ready; every capability is reachable by type assertion.
	return built, nil
}

// newStripe builds one stripe over a primitive bounded by entries and bytes.
func (s *memoryStore[V]) newStripe(cfg MemoryConfig, entries int, bytes int64) *memoryStripe[V] {
	stripe := &memoryStripe[V]{tags: newTagIndex()}
	//: DefaultTTL is applied by this store, not by the primitive: the domain
	//: distinguishes "zero means the default" from "NoExpiry means no
	//: deadline", and the primitive has only one zero.
	primitive := kcache.Config[string, record[V]]{
		MaxEntries: entries,
		Clock:      s.clk,
		OnEvict:    stripe.onEvict,
		Policy:     cfg.Policy,
	}
	//: a byte budget weighs each record; a negative entry weighs one unit,
	//: the least that still counts it.
	if s.weigh != nil {
		primitive.MaxWeight = bytes
		primitive.WeightOf = s.weightOf
	}
	stripe.lru = kcache.NewCache(primitive)
	//: an empty stripe.
	return stripe
}

// absentWeight is what a negative entry counts against MaxBytes: one unit,
// the least that still counts it, when the store bounds bytes.
func (s *memoryStore[V]) absentWeight() int64 {
	//: a store that weighs nothing counts nothing.
	if s.weigh == nil {
		//: weightless.
		return 0
	}
	//: one unit.
	return 1
}

// weightOf is the primitive's WeightOf: the weight Set measured, once.
func (s *memoryStore[V]) weightOf(_ string, rec record[V]) int64 {
	//: measured at Set, so the caller's weigher runs once per write.
	return rec.weight
}

// stripeOf returns the stripe key lives in.
func (s *memoryStore[V]) stripeOf(key string) *memoryStripe[V] {
	//: one stripe needs no hash.
	if len(s.stripes) == 1 {
		//: the only stripe.
		return s.stripes[0]
	}
	//: a seeded hash, so no input can be crafted to land in one stripe.
	return s.stripes[maphashString(s, key)%uint64(len(s.stripes))]
}

// maphashString hashes key with the store's seed.
func maphashString[V any](s *memoryStore[V], key string) uint64 {
	//: the stdlib's seeded string hash.
	return maphash.String(s.seed, key)
}

// onEvict records a key the primitive dropped by capacity or expiry. It takes
// NO lock — see the memoryStore type comment.
func (m *memoryStripe[V]) onEvict(key string, _ record[V]) {
	//: the surrounding method holds m.mu and will drain this.
	m.evicted = append(m.evicted, key)
}

// drainEvicted unlinks every key the primitive just dropped from the tag
// index. Caller holds mu.
func (m *memoryStripe[V]) drainEvicted() {
	//: the overwhelmingly common case is that nothing was evicted.
	if len(m.evicted) == 0 {
		//: nothing to unlink.
		return
	}
	//: unlink every key the primitive just dropped; leaving one behind grows
	//: the reverse index forever and makes it name entries that do not exist.
	for _, key := range m.evicted {
		//: one unlink per evicted key.
		m.tags.remove(key)
	}
	//: keep the backing array; this scratch buffer is reused every call.
	m.evicted = m.evicted[:0]
}

// fetch is the one place a stripe's primitive is read. Caller holds mu.
func (m *memoryStripe[V]) fetch(key string) (rec record[V], found bool) {
	//: a hit records itself and a miss may reap an expired entry — both
	//: mutate, and a reap fires onEvict, which is why the drain follows.
	stored, ok := m.lru.Fetch(key)
	m.drainEvicted()
	//: the record as the primitive holds it.
	return stored, ok
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
	//: nothing wrong — and a negative entry holds no value.
	if !ok || rec.absent != nil {
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
	//: a miss, or a negative entry, yields the zero entry.
	if !ok || rec.absent != nil {
		//: nothing stored under this key.
		return corecache.EntryValue[V]{}, false, nil
	}
	//: the entry as it stands NOW.
	return s.entryOf(rec), true, nil
}

// entryOf turns a record into the entry a caller may hand back to Set.
func (s *memoryStore[V]) entryOf(rec record[V]) corecache.EntryValue[V] {
	//: tags are copied out: the index owns the slice the store holds, and a
	//: caller free to append to it could corrupt another entry's tag list.
	return corecache.EntryValue[V]{Value: rec.value, TTL: s.remaining(rec), Tags: slices.Clone(rec.tags)}
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

// fetchRecord reads one key under its stripe's lock. The lock is released
// by hand rather than deferred: this is every read's path, and nothing
// between the two calls can panic short of a broken primitive.
func (s *memoryStore[V]) fetchRecord(key string) (rec record[V], found bool) {
	stripe := s.stripeOf(key)
	stripe.mu.Lock()
	rec, found = stripe.fetch(key)
	stripe.mu.Unlock()
	//: the record as the primitive holds it.
	return rec, found
}

// Set stores entry under key. An entry the store cannot represent is REFUSED
// rather than reinterpreted — see [validateEntry] — and so is one heavier than
// a stripe's share of MaxBytes.
func (s *memoryStore[V]) Set(_ context.Context, key string, entry corecache.EntryValue[V]) error {
	weight, refusal := s.validate(key, entry)
	//: refuse before anything is written.
	if refusal != nil {
		//: the typed rejection.
		return refusal
	}
	stripe := s.stripeOf(key)
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	s.store(stripe, key, record[V]{value: entry.Value, tags: entry.Tags, weight: weight}, entry.TTL)
	//: stored.
	return nil
}

// validate is validateEntry plus the byte bound, and returns the entry's
// weight: an entry heavier than one stripe's share of MaxBytes would evict
// every other entry of its stripe and still overrun the bound, so it is
// refused. The weigher runs once per write, here.
func (s *memoryStore[V]) validate(key string, entry corecache.EntryValue[V]) (weight int64, err error) {
	//: the three refusals every store shares.
	if refusal := validateEntry(key, entry); refusal != nil {
		//: the typed rejection.
		return 0, refusal
	}
	//: no byte bound, nothing to weigh against.
	if s.weigh == nil {
		//: storable, weightless.
		return 0, nil
	}
	//: a negative measure would let an entry pay for others.
	weight = max(s.weigh(entry.Value), 0)
	//: heavier than the bound it would sit under.
	if weight > s.maxWeight {
		//: CACHE_ENTRY_REJECTED, naming the weight and the share.
		return 0, rejected("value", "weighs "+strconv.FormatInt(weight, 10)+", over the "+strconv.FormatInt(s.maxWeight, 10)+" a stripe holds", key)
	}
	//: storable.
	return weight, nil
}

// store writes rec under key into stripe, its deadline from ttl. Caller holds
// stripe.mu and has validated the entry; rec.tags is the caller's slice.
func (s *memoryStore[V]) store(stripe *memoryStripe[V], key string, rec record[V], ttl time.Duration) {
	lifetime := primitiveTTL(ttl, s.defaultTTL)
	//: the deadline is computed once, here, so FetchEntry can report what is
	//: left without asking the primitive for something it does not expose.
	rec.expireAt = time.Time{}
	//: only a positive lifetime produces a deadline; a zero one has already
	//: been resolved by primitiveTTL and means "no deadline".
	if lifetime > 0 {
		//: an absolute deadline against the injected clock.
		rec.expireAt = s.clk.Now().Add(lifetime)
	}
	//: index first: add replaces this key's previous tags, so an overwrite
	//: cannot leave the old ones pointing here. It hands back the copy IT
	//: owns, which the record then shares — one slice, so the entry and the
	//: index can never disagree, and the caller's own slice never reaches in.
	rec.tags = stripe.tags.add(key, rec.tags)
	stripe.lru.SetTTL(key, rec, lifetime)
	//: the insert may have evicted OTHER keys (never this one — the entry
	//: just written is never its own victim). Unlink them.
	stripe.drainEvicted()
}

// Delete removes key. Idempotent: a key that names nothing reports nil.
func (s *memoryStore[V]) Delete(_ context.Context, key string) error {
	stripe := s.stripeOf(key)
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	//: the primitive's Delete deliberately does NOT fire OnEvict, so the index
	//: is unlinked here rather than through the drain.
	stripe.lru.Delete(key)
	stripe.tags.remove(key)
	//: removed, or was never there.
	return nil
}

// InvalidateTag removes every entry carrying tag.
//
// The cost is O(k) in the entries carrying the tag plus O(T) per entry in that
// entry's own tags — NOT O(N) in the store. See `tagindex.go` for why the
// reverse index exists and `BENCH.md` for the measurement at two store sizes.
// A striped store walks every stripe, one lock at a time.
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
	//: every stripe may hold keys carrying the tag.
	for _, stripe := range s.stripes {
		removed += stripe.invalidate(tag)
	}
	//: every indexed key was in the store, so the count is exact.
	return removed, nil
}

// invalidate drops the stripe's keys carrying tag and reports how many.
func (m *memoryStripe[V]) invalidate(tag string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a COPY of the bucket: the loop below deletes from the very map it would
	//: otherwise be ranging over.
	keys := m.tags.keysFor(tag)
	//: O(k) in the keys carrying the tag — never O(N) in the store.
	for _, key := range keys {
		//: Delete does not fire OnEvict, so unlink explicitly.
		m.lru.Delete(key)
		m.tags.remove(key)
	}
	//: the stripe's share of the removals.
	return len(keys)
}

// Expire removes every entry whose deadline has passed, negative entries
// included, from every stripe, one lock at a time, and reports how many
// (#1059). The store starts no goroutine: a caller schedules it on its own
// ticker or scheduler. The tag index is drained of each removed key as
// eviction drains it.
func (s *memoryStore[V]) Expire(_ context.Context) (removed int, err error) {
	//: every stripe, never two locks at once.
	for _, stripe := range s.stripes {
		removed += stripe.expire()
	}
	//: the total; this store cannot fail.
	return removed, nil
}

// expire sweeps one stripe.
func (m *memoryStripe[V]) expire() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	reaped := m.lru.Expire()
	//: the sweep fired onEvict for each removal; unlink them all.
	m.drainEvicted()
	//: the stripe's removals.
	return reaped
}

// Load returns the value for key, running fill at most once across all
// concurrent callers of this key IN THIS PROCESS. It does NOT coordinate
// across replicas — see [corecache.Loader]. A negative entry (#838) answers
// with the error its fill marked [corecache.Absent], and no fill runs.
func (s *memoryStore[V]) Load(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	//: the hit path must not enter the group at all: joining costs a mutex and
	//: a channel, and a hit needs neither.
	if rec, ok := s.fetchRecord(key); ok {
		//: a negative entry is an answer too.
		return rec.value, rec.absent
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
	if rec, ok := s.fetchRecord(key); ok {
		//: someone else's fill already answered this.
		return rec.value, rec.absent
	}
	//: the fill, its answer stored as a value or as a negative entry.
	return s.fillAndStore(ctx, key, fill)
}

// fillAndStore runs fill and stores what it answered: a value, or — for a
// failure marked corecache.Absent with a positive lifetime — a negative
// entry. Any other failure stores NOTHING: caching a failure turns one bad
// minute at the origin into a full TTL of confident wrong answers.
func (s *memoryStore[V]) fillAndStore(ctx context.Context, key string, fill corecache.Fill[V]) (value V, err error) {
	entry, fillErr := fill(ctx)
	//: a failed fill: an answer to keep, or nothing to keep.
	if fillErr != nil {
		var zero V
		labelled := fillFailure(key, fillErr)
		//: a negative answer with a lifetime and storable tags is stored, never
		//: a plain failure.
		if ttl, absent := corecache.AbsentTTLOf(fillErr); absent && ttl > 0 && !slices.Contains(entry.Tags, "") {
			stripe := s.stripeOf(key)
			stripe.mu.Lock()
			s.store(stripe, key, record[V]{tags: entry.Tags, absent: labelled, weight: s.absentWeight()}, ttl)
			stripe.mu.Unlock()
		}
		//: the typed refusal, or the origin's own error.
		return zero, labelled
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
