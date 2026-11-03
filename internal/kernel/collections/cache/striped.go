package cache

import (
	"hash/maphash"
	"iter"
	"time"
)

// Striped spreads its keys over independent Caches — stripes — each behind
// its own lock, so goroutines working on keys of different stripes never
// wait for one another. A key always lands in the same stripe, chosen by a
// hash of the key.
//
// Each stripe holds an equal share of MaxEntries and MaxWeight, at least one
// unit when the bound is set: the policy is exact within a stripe and
// approximate across them — the victim is the least-recently-used entry of
// the stripe that overflowed, not of the whole cache. OnEvict fires per
// stripe, outside that stripe's lock, as a Cache fires it. With one stripe a
// Striped behaves exactly as one Cache.
//
// The zero value is NOT usable — construct with NewStriped.
type Striped[K comparable, V any] struct {
	stripes []*Cache[K, V]
	// hash is the caller's, or nil for maphash over seed.
	hash func(key K) uint64
	seed maphash.Seed
}

// NewStriped builds a Striped of the given number of stripes from cfg, whose
// bounds it divides between them. A stripe count below 1 is 1. A nil hash
// uses hash/maphash over a seed drawn for this Striped alone.
func NewStriped[K comparable, V any](cfg Config[K, V], stripes int, hash func(key K) uint64) *Striped[K, V] {
	count := max(stripes, 1)
	built := &Striped[K, V]{stripes: make([]*Cache[K, V], count), hash: hash}
	//: the default hash needs a seed; a caller's hash does not.
	if hash == nil {
		built.seed = maphash.MakeSeed()
	}
	entries := max(cfg.MaxEntries, 0)
	weight := max(cfg.MaxWeight, 0)
	//: every stripe is a whole Cache with its share of the bounds.
	for at := range built.stripes {
		part := cfg
		part.MaxEntries = shareOf(entries, count, at)
		part.MaxWeight = int64(shareOf(int(weight), count, at))
		built.stripes[at] = NewCache(part)
	}
	//: ready.
	return built
}

// shareOf returns stripe at's share of bound over count stripes: the
// remainder goes to the first stripes, and a set bound gives every stripe at
// least one unit, since a zero bound would read as unbounded.
func shareOf(bound, count, at int) int {
	//: an unbounded total stays unbounded in every stripe.
	if bound == 0 {
		//: unbounded.
		return 0
	}
	share := bound / count
	//: the first bound%count stripes take one more each.
	if at < bound%count {
		share++
	}
	//: never zero — that would lift the bound off the stripe.
	return max(share, 1)
}

// stripe returns the Cache key lands in.
func (s *Striped[K, V]) stripe(key K) *Cache[K, V] {
	//: one stripe needs no hash.
	if len(s.stripes) == 1 {
		//: the only stripe.
		return s.stripes[0]
	}
	var sum uint64
	//: the caller's hash, or maphash over this Striped's seed.
	if s.hash != nil {
		sum = s.hash(key)
	} else {
		sum = maphash.Comparable(s.seed, key)
	}
	//: the stripe the hash names.
	return s.stripes[sum%uint64(len(s.stripes))]
}

// Fetch returns the value for key and whether it was present and unexpired,
// from key's stripe, recording the hit as that stripe's policy does.
func (s *Striped[K, V]) Fetch(key K) (value V, ok bool) {
	//: one stripe's lock, not the whole cache's.
	return s.stripe(key).Fetch(key)
}

// Set stores key=val in key's stripe with the default TTL.
func (s *Striped[K, V]) Set(key K, val V) {
	s.stripe(key).Set(key, val)
}

// SetTTL stores key=val in key's stripe with an explicit ttl (0 = no expiry).
func (s *Striped[K, V]) SetTTL(key K, val V, ttl time.Duration) {
	s.stripe(key).SetTTL(key, val, ttl)
}

// Delete removes key from its stripe, without firing OnEvict.
func (s *Striped[K, V]) Delete(key K) {
	s.stripe(key).Delete(key)
}

// Expire sweeps every stripe, one lock at a time, and reports how many entries
// it removed in total.
func (s *Striped[K, V]) Expire() int {
	reaped := 0
	//: each stripe under its own lock; never two at once.
	for _, part := range s.stripes {
		reaped += part.Expire()
	}
	//: the total.
	return reaped
}

// Keys yields the keys of every stripe, a stripe at a time, each stripe's
// copied under its read lock and yielded after it is released, as
// Cache.Keys does, so the loop's body may call s. The stripes are read one
// after another, so under concurrent writes the walk is no single instant's.
func (s *Striped[K, V]) Keys() iter.Seq[K] {
	//: one stripe's snapshot at a time.
	return func(yield func(K) bool) {
		//: in stripe order.
		for _, part := range s.stripes {
			//: that stripe's keys, its lock already released.
			for key := range part.Keys() {
				//: the caller stopped.
				if !yield(key) {
					//: the remaining stripes are not read.
					return
				}
			}
		}
	}
}

// Len returns the live entry count summed over the stripes. The stripes are
// read one after another, so under concurrent writes the sum is no single
// instant's.
func (s *Striped[K, V]) Len() int {
	total := 0
	//: one stripe at a time.
	for _, part := range s.stripes {
		total += part.Len()
	}
	//: the sum.
	return total
}

// Purge drops every entry of every stripe without firing OnEvict.
func (s *Striped[K, V]) Purge() {
	//: one stripe at a time.
	for _, part := range s.stripes {
		part.Purge()
	}
}

// Stats returns the counters summed over the stripes, read one after another.
func (s *Striped[K, V]) Stats() StatsValue {
	var total StatsValue
	//: one stripe at a time; every stripe runs the same policy.
	for _, part := range s.stripes {
		stats := part.Stats()
		total.Hits += stats.Hits
		total.Misses += stats.Misses
		total.Evictions += stats.Evictions
		total.Weight += stats.Weight
		total.Policy = stats.Policy
	}
	//: the sums.
	return total
}

// Stripes reports how many stripes s spreads its keys over.
func (s *Striped[K, V]) Stripes() int {
	//: fixed at construction.
	return len(s.stripes)
}
