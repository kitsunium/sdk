package cache

import (
	"context"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
)

// FetchMany returns the values of the keys the store holds (#840): the keys
// are grouped by stripe and each stripe is locked once for all of its keys,
// where N Fetches lock N times. It mutates the store as Fetch does; a
// negative entry is a miss; a duplicate key is read once.
func (s *memoryStore[V]) FetchMany(_ context.Context, keys []string) (map[string]V, error) {
	found := make(map[string]V, len(keys))
	//: one stripe: one lock for the whole batch, no grouping needed.
	if len(s.stripes) == 1 {
		s.stripes[0].fetchInto(found, keys)
		//: the hits.
		return found, nil
	}
	//: every stripe locked once, for its own keys only.
	for at, group := range s.groupByStripe(keys) {
		//: a stripe none of the keys lands in is not locked at all.
		if len(group) > 0 {
			s.stripes[at].fetchInto(found, group)
		}
	}
	//: the hits.
	return found, nil
}

// fetchInto reads keys under one acquisition of the stripe's lock, adding each
// live value to found.
func (m *memoryStripe[V]) fetchInto(found map[string]V, keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: one read per distinct key.
	for _, key := range keys {
		//: a duplicate is read once.
		if _, seen := found[key]; seen {
			continue
		}
		//: a live, positive entry is a hit.
		if rec, ok := m.fetch(key); ok && rec.absent == nil {
			found[key] = rec.value
		}
	}
}

// groupByStripe splits keys by the stripe each lands in, indexed like
// s.stripes.
func (s *memoryStore[V]) groupByStripe(keys []string) [][]string {
	groups := make([][]string, len(s.stripes))
	//: one hash per key.
	for _, key := range keys {
		at := s.stripeIndex(key)
		groups[at] = append(groups[at], key)
	}
	//: each stripe's keys, in the caller's order.
	return groups
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
		s.store(stripe, key, record[V]{value: entry.Value, tags: entry.Tags, weight: weights[key]}, entry.TTL)
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

// remainingOf is the entry's remaining lifetime at now: 0 for an entry with
// no deadline, and never negative.
func remainingOf(expireAt, now time.Time) time.Duration {
	//: no deadline, nothing remaining to measure.
	if expireAt.IsZero() {
		//: unbounded.
		return 0
	}
	//: what is left, floored at zero.
	return max(expireAt.Sub(now), 0)
}
