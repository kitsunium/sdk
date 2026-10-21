package resilience

import (
	"math"
	"time"
)

// The window's defaults, each a published library's own (BreakerWindowValue's
// doc names them): Polly v8's CircuitBreakerStrategyOptions — SamplingDuration
// 30 s, MinimumThroughput 100, FailureRatio 0.1, and the ten windows its
// rolling health metrics split the sampling duration into — and resilience4j's
// CircuitBreakerConfig — slowCallDurationThreshold 60 s, slowCallRateThreshold
// 100 %.
const (
	// defaultWindowSize is Polly's SamplingDuration.
	defaultWindowSize time.Duration = 30 * time.Second
	// defaultWindowBuckets is the number of windows Polly's rolling health
	// metrics divide the sampling duration into.
	defaultWindowBuckets int = 10
	// defaultMinCalls is Polly's MinimumThroughput.
	defaultMinCalls int = 100
	// defaultFailureRatio is Polly's FailureRatio.
	defaultFailureRatio float64 = 0.1
	// defaultSlowAfter is resilience4j's slowCallDurationThreshold.
	defaultSlowAfter time.Duration = 60 * time.Second
	// defaultSlowRatio is resilience4j's slowCallRateThreshold, 100 %.
	defaultSlowRatio float64 = 1
	// maxWindowBuckets bounds the slots a breaker allocates once, at
	// construction: a Buckets of math.MaxInt would otherwise be a make() that
	// panics or eats the process.
	maxWindowBuckets int = 1000
	// windowPolicy names the breaker in a PolicyMisconfigured refusal.
	windowPolicy string = "breaker"
)

// windowBucket is one slot of the window: the calls that ended in it.
type windowBucket struct {
	calls    int
	failures int
	slow     int
}

// slidingWindow holds the calls of the last len(buckets)×width on the
// breaker's clock. Slot e%len(buckets) holds epoch e — the width-long slice of
// time e×width after origin — for every epoch the window still reaches; head
// is the latest epoch seen, and the totals are the sum of the live slots, kept
// as slots are filled and emptied so that a record costs O(1).
//
// It is guarded by the breaker's mutex and allocates nothing after newWindow.
type slidingWindow struct {
	buckets      []windowBucket
	width        time.Duration
	origin       time.Time
	head         int64
	calls        int
	failures     int
	slow         int
	minCalls     int
	failureRatio float64
	slowAfter    time.Duration
	slowRatio    float64
}

// windowSettings is a BreakerWindowValue with every default applied, or the
// name of the field that cannot be honoured.
type windowSettings struct {
	size         time.Duration
	buckets      int
	minCalls     int
	failureRatio float64
	slowAfter    time.Duration
	slowRatio    float64
}

// resolveWindow applies the defaults of a BreakerWindowValue and returns the
// field it refuses, if any, spelt as the caller writes it.
func resolveWindow(w BreakerWindowValue) (windowSettings, string) {
	//: each zero takes its documented default.
	s := windowSettings{
		size:         orDuration(w.Size, defaultWindowSize),
		buckets:      orInt(w.Buckets, defaultWindowBuckets),
		minCalls:     orInt(w.MinCalls, defaultMinCalls),
		failureRatio: orRatio(w.FailureRatio, defaultFailureRatio),
		slowAfter:    orDuration(w.SlowAfter, defaultSlowAfter),
		slowRatio:    orRatio(w.SlowRatio, defaultSlowRatio),
	}
	//: the first field out of range is the one named.
	switch {
	//: a negative reach is no window.
	case s.size < 0:
		//: refused.
		return s, "Window.Size"
	//: no slot, or more slots than a breaker should hold.
	case s.buckets < 0 || s.buckets > maxWindowBuckets:
		//: refused.
		return s, "Window.Buckets"
	//: a slot narrower than a nanosecond cannot be told from the next.
	case s.size/time.Duration(s.buckets) <= 0:
		//: refused, naming the reach that cannot be split.
		return s, "Window.Size"
	//: a negative count.
	case s.minCalls < 0:
		//: refused.
		return s, "Window.MinCalls"
	//: outside (0, 1], or not a number.
	case !validRatio(s.failureRatio):
		//: refused.
		return s, "Window.FailureRatio"
	//: a negative duration.
	case s.slowAfter < 0:
		//: refused.
		return s, "Window.SlowAfter"
	//: outside (0, 1], or not a number.
	case !validRatio(s.slowRatio):
		//: refused.
		return s, "Window.SlowRatio"
	//: every field honoured.
	default:
		//: no refusal.
		return s, ""
	}
}

// orDuration is d, or def when d is zero.
func orDuration(d, def time.Duration) time.Duration {
	//: zero is the field left unset.
	if d == 0 {
		//: its default.
		return def
	}
	//: the caller's value, checked by the caller.
	return d
}

// orInt is n, or def when n is zero.
func orInt(n, def int) int {
	//: zero is the field left unset.
	if n == 0 {
		//: its default.
		return def
	}
	//: the caller's value, checked by the caller.
	return n
}

// orRatio is r, or def when r is zero.
func orRatio(r, def float64) float64 {
	//: zero is the field left unset; NaN is not zero and stays to be refused.
	if r == 0 {
		//: its default.
		return def
	}
	//: the caller's value, checked by the caller.
	return r
}

// validRatio reports whether r is a share in (0, 1].
func validRatio(r float64) bool {
	//: NaN fails both comparisons; an infinity fails the second.
	return r > 0 && r <= 1 && !math.IsNaN(r)
}

// newWindow allocates the slots of a window starting at origin.
func newWindow(s windowSettings, origin time.Time) *slidingWindow {
	//: one allocation, at construction; nothing after it.
	return &slidingWindow{
		buckets:      make([]windowBucket, s.buckets),
		width:        s.size / time.Duration(s.buckets),
		origin:       origin,
		minCalls:     s.minCalls,
		failureRatio: s.failureRatio,
		slowAfter:    s.slowAfter,
		slowRatio:    s.slowRatio,
	}
}

// advance moves the window to the epoch of now, emptying every slot time has
// moved past, and returns the slot now falls in. A now before the latest epoch
// seen — a clock set back — is counted in that latest slot.
func (w *slidingWindow) advance(now time.Time) *windowBucket {
	n := int64(len(w.buckets))
	epoch := max(int64(now.Sub(w.origin)/w.width), w.head)
	//: the same slot as the last record: nothing has expired.
	if epoch == w.head {
		//: the current slot.
		return &w.buckets[epoch%n]
	}
	//: time moved past the whole window: every slot is stale.
	if epoch-w.head >= n {
		w.clear()
	} else {
		//: empty each slot an epoch between the last one and now reuses.
		for e := w.head + 1; e <= epoch; e++ {
			w.drop(&w.buckets[e%n])
		}
	}
	w.head = epoch
	//: the slot the call lands in, emptied above if it held an older epoch.
	return &w.buckets[epoch%n]
}

// drop takes one slot out of the totals and empties it.
func (w *slidingWindow) drop(b *windowBucket) {
	w.calls -= b.calls
	w.failures -= b.failures
	w.slow -= b.slow
	*b = windowBucket{}
}

// clear empties every slot and the totals; head is kept, so the epochs only
// move forward.
func (w *slidingWindow) clear() {
	clear(w.buckets)
	w.calls, w.failures, w.slow = 0, 0, 0
}

// add records one call ending at now and reports whether the window now opens
// the breaker.
func (w *slidingWindow) add(now time.Time, failed, slow bool) bool {
	b := w.advance(now)
	b.calls++
	w.calls++
	//: a failure counts in both the slot and the totals.
	if failed {
		b.failures++
		w.failures++
	}
	//: so does a slow call, failed or not.
	if slow {
		b.slow++
		w.slow++
	}
	//: the ratios are read once there are enough calls for them to mean one.
	return w.tripped()
}

// tripped reports whether the window holds enough calls and either ratio is
// reached.
func (w *slidingWindow) tripped() bool {
	//: below MinCalls (at least one once resolved), one failure would be a
	//: rate of 100 %.
	if w.calls < w.minCalls {
		//: not enough calls to read a rate.
		return false
	}
	total := float64(w.calls)
	//: the failure rate, or the slow-call rate, at or above its threshold.
	return float64(w.failures)/total >= w.failureRatio || float64(w.slow)/total >= w.slowRatio
}
