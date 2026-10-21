package resilience

import (
	"math"
	"time"
)

// The adaptive throttle's defaults, each the SRE book's own ("Handling
// Overload", client-side throttling): K = 2 and a two-minute history. The ten
// slots are the breaker window's, so the two windows reach back alike.
const (
	// defaultThrottleK is the SRE book's multiplier.
	defaultThrottleK float64 = 2
	// minThrottleK is the smallest multiplier that never refuses a call to a
	// dependency accepting every one of them.
	minThrottleK float64 = 1
	// defaultThrottleWindow is the SRE book's two minutes of history.
	defaultThrottleWindow time.Duration = 2 * time.Minute
	// throttlePolicy names the throttle in a PolicyMisconfigured refusal.
	throttlePolicy string = "throttle"
)

// throttleBucket is one slot of the throttle's window: the requests counted
// in it and the accepts.
type throttleBucket struct {
	requests int64
	accepts  int64
}

// throttleWindow holds the requests and accepts of the last
// len(buckets)×width on the throttle's clock, slot e%len(buckets) holding
// epoch e, as the breaker's slidingWindow does — head the latest epoch seen,
// headEnd where it ends, and the totals kept as slots fill and empty. It is
// guarded by the throttle's mutex and allocates nothing after
// newThrottleWindow.
type throttleWindow struct {
	buckets  []throttleBucket
	width    time.Duration
	origin   time.Time
	head     int64
	headEnd  time.Duration
	requests int64
	accepts  int64
}

// throttleSettings is an AdaptiveThrottleConfig's numbers with every default
// applied.
type throttleSettings struct {
	k       float64
	window  time.Duration
	buckets int
}

// resolveThrottle applies the defaults of cfg's numbers and returns the field
// it refuses, if any.
func resolveThrottle(cfg AdaptiveThrottleConfig) (throttleSettings, string) {
	settings := throttleSettings{
		k:       orRatio(cfg.K, defaultThrottleK),
		window:  orDuration(cfg.Window, defaultThrottleWindow),
		buckets: orInt(cfg.Buckets, defaultWindowBuckets),
	}
	//: the first field that cannot be honoured, in the order the config
	//: declares them.
	switch {
	//: below 1 refuses calls to a healthy dependency; NaN and ±Inf compare
	//: their way past every bound.
	case math.IsNaN(settings.k) || math.IsInf(settings.k, 0) || settings.k < minThrottleK:
		//: refused.
		return settings, "K"
	//: a slot count the throttle could not allocate once.
	case settings.buckets < 0 || settings.buckets > maxWindowBuckets:
		//: refused.
		return settings, "Buckets"
	//: a window reaching back a negative span, or too short to split.
	case settings.window < 0 || settings.window/time.Duration(settings.buckets) <= 0:
		//: refused.
		return settings, "Window"
	//: usable.
	default:
		//: none refused.
		return settings, ""
	}
}

// newThrottleWindow allocates the slots of a window starting at origin.
func newThrottleWindow(s throttleSettings, origin time.Time) throttleWindow {
	width := s.window / time.Duration(s.buckets)
	//: one allocation, at construction.
	return throttleWindow{buckets: make([]throttleBucket, s.buckets), width: width, origin: origin, headEnd: width}
}

// advance moves the window to the epoch of now, emptying every slot time has
// moved past, and returns the slot now falls in. A now before the latest epoch
// seen — a clock set back — is counted in that latest slot.
func (w *throttleWindow) advance(now time.Time) *throttleBucket {
	n := int64(len(w.buckets))
	elapsed := now.Sub(w.origin)
	//: still in the latest slot: no division.
	if elapsed < w.headEnd {
		//: the current slot.
		return &w.buckets[w.head%n]
	}
	epoch := int64(elapsed / w.width)
	//: every slot is older than the window: empty them all.
	if epoch-w.head >= n {
		clear(w.buckets)
		w.requests, w.accepts = 0, 0
	} else {
		//: drop the slots between the last epoch seen and this one.
		for e := w.head + 1; e <= epoch; e++ {
			w.drop(&w.buckets[e%n])
		}
	}
	w.head = epoch
	w.headEnd = epochEnd(epoch, w.width)
	//: the slot of now.
	return &w.buckets[epoch%n]
}

// drop takes one slot out of the totals and empties it.
func (w *throttleWindow) drop(b *throttleBucket) {
	w.requests -= b.requests
	w.accepts -= b.accepts
	*b = throttleBucket{}
}

// refusal is the probability, in [0, 1), that the next request is refused:
// max(0, (requests − k × accepts) / (requests + 1)).
func (w *throttleWindow) refusal(k float64) float64 {
	requests := float64(w.requests)
	//: the excess of requests over what the accepts vouch for.
	excess := requests - k*float64(w.accepts)
	//: the dependency accepts at least one call in k: nothing is refused.
	if excess <= 0 {
		//: no refusal.
		return 0
	}
	//: the SRE book's formula; the +1 keeps it below 1.
	return excess / (requests + 1)
}
