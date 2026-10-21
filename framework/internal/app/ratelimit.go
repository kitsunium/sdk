package app

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The per-client buckets' bounds.
const (
	// maxClientBuckets is how many clients an endpoint tracks at once.
	maxClientBuckets int = 10_000
	// clientBucketIdle is how long a client's bucket outlives its last call.
	clientBucketIdle time.Duration = 10 * time.Minute
)

// RateLimitPerClient gives every client of an endpoint a token bucket of its
// own: perSecond tokens a second, up to burst at once. A client is the
// authenticated user when there is one, and the client's address otherwise
// ([ClientIP]); in-process calls through [EndpointService.Call] share one bucket. One
// client emptying its bucket is answered 429, with Retry-After; the others
// are not — which is what a sign-in endpoint needs, where one shared bucket
// would let a single client lock everybody out.
//
// Buckets unused for ten minutes are forgotten, and at most 10 000 are kept:
// the least recently used goes first. It is the SDK's keyed rate limiter.
//
// The values are the defaults of the operation's settings
// <service>-<kind>-<operation>-client-rate and -client-burst, which config/<env>.*
// and <PRODUCT>_<NAME> override; a rate that is not above zero or a burst
// below one stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RateLimitPerClient(perSecond float64, burst int) OperationOption {
	return policyOption(func() policyGuard { return &clientLimitGuard{rate: perSecond, burst: burst} })
}

// newClientLimiter is a bucket per client, at most keys of them, each
// forgotten after idle without a call, on clk — the system's when nil.
func newClientLimiter(rate float64, burst, keys int, idle time.Duration, clk clock.Clock) resilience.Runner {
	return resilience.NewKeyedRateLimiter(resilience.KeyedRateLimiterConfig{
		Key: clientKey, Clock: clk, Rate: rate, Burst: burst, MaxKeys: keys, IdleTimeout: idle,
	})
}

// clientKey names the client a request or a call comes from.
func clientKey(ctx context.Context) string {
	if uid, ok := UserID(ctx); ok {
		return "user:" + string(uid)
	}
	if ip := ClientIP(ctx); ip != "" {
		return "ip:" + ip
	}
	return "in-process"
}
