package health

import "time"

// Age returns how stale this answer is as of now, which is zero for anything
// measured during the probe that reported it.
//
// It takes the instant rather than reading a clock so that the value type
// keeps no time source of its own: the engine already has an injected one, and
// a second, hidden, wall-clock read inside a domain value is exactly the thing
// that makes a staleness assertion untestable.
func (r ResultValue) Age(now time.Time) time.Duration {
	//: an unstamped result has no age to report; claiming one would invent a
	//: staleness out of the zero time.
	if r.At.IsZero() {
		//: nothing measured, nothing stale.
		return 0
	}
	age := now.Sub(r.At)
	//: a clock that moved backwards (NTP, a manual clock in a test) must not
	//: produce a negative age that reads as "measured in the future".
	if age < 0 {
		//: floor at zero rather than propagate the anomaly.
		return 0
	}
	//: how old the answer is.
	return age
}
