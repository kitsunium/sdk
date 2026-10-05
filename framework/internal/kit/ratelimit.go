package kit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
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
	rate := strconv.FormatFloat(perSecond, 'f', -1, 64)
	return callOption(func(o *callOptions) {
		o.policies = append(o.policies, policy{
			mech: model.Mechanic{
				Kind:    "ratelimit",
				Label:   fmt.Sprintf("%s/s burst %d per client", rate, burst),
				Package: "github.com/kitsunium/sdk/pkg/v1/app/resilience",
				Config:  map[string]string{"rate": rate, "burst": strconv.Itoa(burst), "per": "client"},
			},
			runner: newClientLimiter(perSecond, burst, maxClientBuckets, clientBucketIdle, nil),
		})
	})
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
