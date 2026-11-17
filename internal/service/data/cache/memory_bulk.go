package cache

import (
	"context"
	"slices"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// batchOnStack is the longest batch FetchMany answers with its scratch on the
// stack: past it, the scratch — a reference a key, and on a striped store a
// sort key and a key a key — is allocated for the call.
const batchOnStack int = 64

// stripeShift places a key's stripe above its position in fetchStriped's sort
// keys: a stripe index needs ten bits (maxStripes), a position the 54 below.
const stripeShift int = 54

// positionMask keeps the position of a fetchStriped sort key.
const positionMask uint64 = 1<<stripeShift - 1

// FetchMany appends to values and found one answer for every key of keys, in
// order (#840): the value keys[i] holds and true, or the zero value and false
// for a miss or a negative entry. It mutates the store as Fetch does, and a
// key named twice is read once (the primitive's batch). Each stripe is locked
// once for all of its keys, and the primitive reads the clock once for them;
// no map is built and, for a batch of at most batchOnStack keys whose slices
// have the room, nothing is allocated.
func (s *memoryStore[V]) FetchMany(_ context.Context, keys []string, values []V, found []bool) ([]V, []bool, error) {
	values, answers := extend(values, len(keys))
	found, hits := extend(found, len(keys))
	//: one stripe: one lock for the whole batch, nothing to sort.
	if s.single != nil {
		s.single.fetchInto(keys, answers, hits)
		//: every key answered.
		return values, found, nil
	}
	s.fetchStriped(keys, answers, hits)
	//: every key answered.
	return values, found, nil
}

// fetchInto answers keys in values and found, position for position, under
// one acquisition of the stripe's lock.
func (m *memoryStripe[V]) fetchInto(keys []string, values []V, found []bool) {
	var scratch [batchOnStack]*record[V]
	warmKeys(keys)
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := m.lru.FetchMany(keys, scratch[:0])
	//: read under the lock that found them: a reference is the primitive's.
	for at, rec := range refs {
		values[at], found[at] = answerOf(rec)
	}
}

// fetchStriped answers keys on a striped store: the positions sorted by
// stripe — the caller's order kept within one —, then each stripe locked once
// for its own keys.
func (s *memoryStore[V]) fetchStriped(keys []string, values []V, found []bool) {
	var orderOn [batchOnStack]uint64
	var keysOn [batchOnStack]string
	var refsOn [batchOnStack]*record[V]
	order, sorted, refs := orderOn[:0], keysOn[:0], refsOn[:0]
	//: a long batch takes its scratch from the heap.
	if len(keys) > batchOnStack {
		order, sorted = make([]uint64, 0, len(keys)), make([]string, 0, len(keys))
	}
	//: one hash per key, its stripe above its position.
	for at, key := range keys {
		order = append(order, uint64(s.stripeIndex(key))<<stripeShift|uint64(at))
	}
	slices.Sort(order)
	//: the keys in the order the stripes take them.
	for _, packed := range order {
		sorted = append(sorted, keys[packed&positionMask])
	}
	//: one run of the sorted keys per stripe that holds some.
	for start := 0; start < len(order); {
		at := order[start] >> stripeShift
		end := start + 1
		//: the run's end: the first key of another stripe.
		for end < len(order) && order[end]>>stripeShift == at {
			end++
		}
		refs = s.stripes[at].fetchAt(sorted[start:end], order[start:end], refs[:0], values, found)
		start = end
	}
}

// fetchAt answers keys under one acquisition of the stripe's lock, each at the
// position its sort key in order carries, and returns refs for the next
// stripe's use.
func (m *memoryStripe[V]) fetchAt(keys []string, order []uint64, refs []*record[V], values []V, found []bool) []*record[V] {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs = m.lru.FetchMany(keys, refs)
	//: read under the lock that found them, written where the caller asked.
	for i, rec := range refs {
		at := order[i] & positionMask
		values[at], found[at] = answerOf(rec)
	}
	//: the scratch, its references no longer read.
	return refs
}

// answerOf is what a position answers for the primitive's reference rec: the
// value and true for a live, positive entry; the zero value and false for a
// miss, or a negative entry, which holds no value.
func answerOf[V any](rec *record[V]) (value V, found bool) {
	//: a miss, or a negative entry.
	if rec == nil || rec.absent != nil {
		//: no value.
		return value, false
	}
	//: the live value.
	return rec.value, true
}

// warmKeys reads the first byte of every key before the batch takes its
// stripe's lock, and returns their sum, which nothing reads (#840). At scale a
// key's bytes are a cache miss of their own, which the map's hash would wait
// for one key at a time, under the lock; read together, outside it, the
// misses overlap — on the shared VM, 387 to 353 ns a key out of 262 144
// entries, and nothing measurable on the threshold's 1 024. A striped store
// needs none: the stripe hash it takes of every key first reads the same
// bytes. Kept out of line, so the reads are never dropped as dead code.
//
//go:noinline
func warmKeys(keys []string) byte {
	var sum byte
	//: one byte of each key.
	for _, key := range keys {
		//: an empty key has no byte to read.
		if len(key) > 0 {
			sum += key[0]
		}
	}
	//: the sum, for the reads to be kept.
	return sum
}

// extend lengthens s by n elements, growing it only when its capacity is
// short, and returns it with the n elements added.
func extend[T any](s []T, n int) (all, added []T) {
	all = slices.Grow(s, n)[:len(s)+n]
	//: the slice, and its new tail.
	return all, all[len(s):]
}

// SetMany stores every entry under its key (#840). Every entry is validated
// first: one the store refuses fails the call and stores nothing. Each stripe
// is then locked once for all of its entries.
func (s *memoryStore[V]) SetMany(_ context.Context, entries map[string]corecache.EntryValue[V]) error {
	weights := make(map[string]int64, len(entries))
	//: all or nothing: every refusal is found before the first write.
	for key, entry := range entries {
		weight, refusal := s.validate(key, entry)
		//: one unstorable entry refuses the batch.
		if refusal != nil {
			//: the typed rejection; nothing was written.
			return refusal
		}
		weights[key] = weight
	}
	groups := make([][]string, len(s.stripes))
	//: group the keys by stripe.
	for key := range entries {
		at := s.stripeIndex(key)
		groups[at] = append(groups[at], key)
	}
	//: one acquisition per stripe that has entries.
	for at, group := range groups {
		//: a stripe with nothing to store is not locked.
		if len(group) > 0 {
			s.setInto(s.stripes[at], group, entries, weights)
		}
	}
	//: every entry stored.
	return nil
}

// setInto stores keys' entries into stripe under one acquisition of its lock.
func (s *memoryStore[V]) setInto(stripe *memoryStripe[V], keys []string, entries map[string]corecache.EntryValue[V], weights map[string]int64) {
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	//: the same write Set makes, per key.
	for _, key := range keys {
		entry := entries[key]
		s.store(stripe, key, record[V]{value: entry.Value, tags: entry.Tags}, entry.TTL, weights[key])
	}
}

// stripeIndex returns the index of the stripe key lands in.
func (s *memoryStore[V]) stripeIndex(key string) int {
	//: one stripe needs no hash.
	if len(s.stripes) == 1 {
		//: the only stripe.
		return 0
	}
	//: the same seeded hash stripeOf uses.
	return int(maphashString(s, key) % uint64(len(s.stripes)))
}

// remainingOf is the entry's remaining lifetime at now, its deadline the
// primitive's: 0 for an entry with no deadline, and never negative.
func remainingOf(deadline, now time.Time) time.Duration {
	//: no deadline, nothing remaining to measure.
	if deadline.IsZero() {
		//: unbounded.
		return 0
	}
	//: what is left, floored at zero.
	return max(deadline.Sub(now), 0)
}
