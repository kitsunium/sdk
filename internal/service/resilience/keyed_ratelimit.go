// Package resilience — the keyed rate limiter: one token bucket per caller,
// with the set of callers bounded and idle ones forgotten.
package resilience

import (
	"context"
	"sync"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// keyedPolicy names this policy in a PolicyMisconfigured refusal.
const keyedPolicy string = "keyed-ratelimit"

// keyedLimiter holds one token bucket per key in a recency list, most
// recently used at the head.
//
// The list is intrusive — each keyedBucket carries its own links, as the
// kernel cache's entries do — so moving a key to the front is two pointer
// swaps and nothing is boxed in an interface.
//
// The bound and the idleness are enforced on the calling goroutine, when a
// key is looked up: there is no sweeper goroutine and no timer, so an idle
// limiter costs nothing and a stopped one leaks nothing.
type keyedLimiter struct {
	clk   clock.Clock
	key   func(ctx context.Context) string
	byKey map[string]*keyedBucket
	head  *keyedBucket // most recently used
	tail  *keyedBucket // least recently used
	rate  float64
	burst float64
	idle  time.Duration
	limit int
	mu    sync.Mutex
}

// keyedBucket is one key's bucket, the instant it was last charged, and its
// place in the recency list.
type keyedBucket struct {
	used   time.Time
	bucket *tokenBucket
	prev   *keyedBucket
	next   *keyedBucket
	key    string
}

// NewKeyedRateLimiter returns a Runner admitting at most cfg.Rate calls per
// second, with a cfg.Burst allowance, PER KEY — the key being whatever cfg.Key
// names for the call's context. A call whose bucket is empty is rejected with
// RateLimited; the others are not.
//
// At most cfg.MaxKeys keys are held: a new key beyond that forgets the least
// recently used one. A key unused for cfg.IdleTimeout is forgotten too. A
// forgotten key's next call starts over with a full bucket.
//
// A Rate that is not a finite positive number, a nil Key, and a non-positive
// MaxKeys or IdleTimeout are each refused: every call returns
// PolicyMisconfigured, naming the field, without running the operation.
func NewKeyedRateLimiter(cfg KeyedRateLimiterConfig) coreres.Runner {
	//: the same refusal NewRateLimiter makes, for the same reasons.
	if !usableRate(cfg.Rate) {
		//: fail closed, and say which knob.
		return newMisconfigured(keyedPolicy, "Rate")
	}
	//: nothing to key on is a single shared bucket, which is a different
	//: policy the caller can already ask for by name.
	if cfg.Key == nil {
		//: fail closed.
		return newMisconfigured(keyedPolicy, "Key")
	}
	//: "unbounded" and "none" are both harmful readings of zero.
	if cfg.MaxKeys <= 0 {
		//: fail closed.
		return newMisconfigured(keyedPolicy, "MaxKeys")
	}
	//: "forget at once" limits nothing; "never" is not what zero says.
	if cfg.IdleTimeout <= 0 {
		//: fail closed.
		return newMisconfigured(keyedPolicy, "IdleTimeout")
	}
	clk := cfg.Clock
	//: nil is the caller with no opinion.
	if clk == nil {
		//: production default.
		clk = clock.System
	}
	//: empty, and bounded from the first call.
	return &keyedLimiter{
		clk: clk, key: cfg.Key, rate: cfg.Rate, burst: bucketSize(cfg.Burst),
		idle: cfg.IdleTimeout, limit: cfg.MaxKeys, byKey: make(map[string]*keyedBucket),
	}
}

// Run charges op to its key's bucket, rejecting it with RateLimited when the
// bucket is empty.
func (l *keyedLimiter) Run(ctx context.Context, op coreres.Operation) error {
	//: the key is resolved on the caller's context, once.
	if !l.bucketOf(l.key(ctx)).take() {
		//: this key's bucket is empty; the others are untouched.
		return wrapAs(coreres.RateLimited, nil)
	}
	//: token consumed — run the operation.
	return op(ctx)
}

// bucketOf returns key's bucket, creating it when the key is new or was
// forgotten, and forgetting whatever the bound or the idleness says to.
//
// The bucket is returned and charged OUTSIDE the lock: each bucket has its
// own, so two keys never contend beyond this lookup.
func (l *keyedLimiter) bucketOf(key string) *tokenBucket {
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	//: a key already held keeps its bucket — unless it went idle, in which
	//: case it is forgotten here rather than whenever the next NEW key
	//: happens to trigger the eviction scan.
	if held, found := l.byKey[key]; found {
		//: still within its idle window: the window slides.
		if now.Sub(held.used) < l.idle {
			held.used = now
			l.moveFront(held)
			//: the key's own bucket, with whatever credit it has left.
			return held.bucket
		}
		l.forget(held)
	}
	l.evict(now)
	fresh := &keyedBucket{key: key, used: now, bucket: newTokenBucket(l.clk, l.rate, l.burst)}
	l.byKey[key] = fresh
	l.pushFront(fresh)
	//: a new key starts with a full bucket.
	return fresh.bucket
}

// evict forgets, from the least recently used end, every key that went idle,
// and as many more as it takes to make room for one. Caller holds mu.
func (l *keyedLimiter) evict(now time.Time) {
	//: the list is in recency order, so the first key that is both recent
	//: and within the bound means every key before it is too.
	for l.tail != nil {
		//: room for one more, and the oldest key is still in use.
		if len(l.byKey) < l.limit && now.Sub(l.tail.used) < l.idle {
			//: nothing further to forget.
			return
		}
		l.forget(l.tail)
	}
}

// forget drops one key. Caller holds mu.
func (l *keyedLimiter) forget(entry *keyedBucket) {
	l.unlink(entry)
	delete(l.byKey, entry.key)
}

// pushFront links a detached entry at the head. Caller holds mu.
func (l *keyedLimiter) pushFront(entry *keyedBucket) {
	entry.prev = nil
	entry.next = l.head
	//: the old head, if any, links back to the new one.
	if l.head != nil {
		l.head.prev = entry
	}
	l.head = entry
	//: an empty list also gains its tail.
	if l.tail == nil {
		l.tail = entry
	}
}

// moveFront makes an entry the most recently used. Caller holds mu.
func (l *keyedLimiter) moveFront(entry *keyedBucket) {
	//: already there.
	if l.head == entry {
		return
	}
	l.unlink(entry)
	l.pushFront(entry)
}

// unlink detaches an entry from the list. Caller holds mu.
func (l *keyedLimiter) unlink(entry *keyedBucket) {
	//: the previous entry, or the head, skips it.
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else {
		l.head = entry.next
	}
	//: the next entry, or the tail, skips it.
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		l.tail = entry.prev
	}
	entry.prev, entry.next = nil, nil
}
