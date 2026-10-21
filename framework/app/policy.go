package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// MaxWait lets a call wait, at most d on the app's clock, instead of being
// refused at once: given to [RateLimit], for a token due within d — the
// calls after it queue behind it —; given to [Bulkhead], for a slot to free,
// in the queue [BulkheadQueue] sizes. A call still refused answers 429 or
// 503 as before; a caller that leaves while it waits leaves the queue. Its
// default is no wait, and a negative one stops the start. It is the default
// of the operation's setting <service>-<kind>-<operation>-rate-wait or
// -bulkhead-wait, which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its methods are unexported — so a
// caller only hands it to the declaration it configures.
func MaxWait(d time.Duration) WaitOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.MaxWait(d)
}

// BulkheadQueue lets up to n calls beyond those a [Bulkhead] runs wait for a
// slot, each for its [MaxWait]: without a wait, or with n at 0, the overflow
// is refused at once, and a negative n stops the start. A call that finds the
// queue full answers 503. It is the default of the operation's setting
// <service>-<kind>-<operation>-bulkhead-queue, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func BulkheadQueue(n int) BulkheadOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.BulkheadQueue(n)
}
