package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// RateLimitPerClient gives every client of an endpoint a token bucket of its
// own: perSecond tokens a second, up to burst at once. A client is the
// authenticated user when there is one, and the client's address otherwise
// ([ClientIP]); in-process calls through [Endpoint].Call share one bucket. One
// client emptying its bucket is answered 429; the others are not — which is
// what a sign-in endpoint needs, where one shared bucket would let a single
// client lock everybody out.
//
// Buckets unused for ten minutes are forgotten, and at most 10 000 are kept:
// the least recently used goes first. It is the SDK's keyed rate limiter.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RateLimitPerClient(perSecond float64, burst int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RateLimitPerClient(perSecond, burst)
}
