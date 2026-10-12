package clock

import "time"

// requirePositivePeriod panics unless d is a usable ticker period. Both
// implementations funnel through it so the refusal is identical whichever
// clock is installed — a test that pins the message pins it for production.
func requirePositivePeriod(op string, d time.Duration) {
	//: a non-positive period is a programmer error, not a runtime condition:
	//: there is no cadence to run and no non-arbitrary value to substitute.
	if d <= 0 {
		//: name the package and the offending value so the stack is enough.
		panic("clock: " + op + " requires a period > 0, got " + d.String())
	}
}
