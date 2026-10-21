package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Empty is the request or the response of an endpoint that has none. An
// endpoint answering EmptyValue replies 204 No Content.
type Empty = iapp.EmptyValue

// Endpoint is an HTTP endpoint with a typed request and a typed response.
// Declare it with [Service].Endpoint; call it from another service with
// [Endpoint].Call, which is how the diagram sees service-to-service calls.
type Endpoint[Req, Resp any] = iapp.EndpointService[Req, Resp]

// EndpointOption configures an endpoint.
type EndpointOption = iapp.EndpointConfigurer

// OperationOption configures an endpoint, a command or a query alike: the
// authentication it asks for ([Auth], [AuthOptional]) and the policies in
// front of its handler ([RateLimit], [RateLimitPerClient], [Timeout],
// [Bulkhead]).
type OperationOption = iapp.OperationOption

// Name overrides the endpoint's name, which otherwise is its handler's
// function name, or its route when the handler is a function literal.
func Name(name string) EndpointOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Name(name)
}

// MaxBody bounds the request body the endpoint reads. The default is
// [DefaultMaxBody].
func MaxBody(bytes int64) EndpointOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.MaxBody(bytes)
}

// RateLimit puts a token bucket in front of the handler: perSecond tokens a
// second, up to burst at once. An empty bucket answers 429 at once — with
// Retry-After, the seconds the bucket needs to earn its next token —, or,
// given [MaxWait], once the token due within it did not come. It is the
// SDK's resilience rate limiter.
//
// The values are the defaults of the operation's settings
// <service>-<kind>-<operation>-rate, -burst and -rate-wait, which config/<env>.* and
// <PRODUCT>_<NAME> override; a rate that is not above zero or a burst below
// one stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RateLimit(perSecond float64, burst int, opts ...RateLimitOption) OperationOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.RateLimit(perSecond, burst, opts...)
}

// Timeout cancels the handler's context after d and answers 504. A caller
// that leaves first is no timeout: its request is recorded as 499, logged as
// information, and nothing it could read is written. It is the SDK's
// resilience timeout, on the app's clock.
//
// Given to a port's [Port].Guard, it is a deadline on each attempt of the port's
// call, the innermost of its policies: an attempt that outruns it fails with
// the SDK's TimeoutExceeded, which the port's breaker counts and its retry
// replays.
//
// d is the default of the operation's setting <service>-<kind>-<operation>-timeout
// — <service>-port-<name>-timeout for a port —, which config/<env>.* and
// <PRODUCT>_<NAME> override; one that is not positive stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Timeout(d time.Duration) TimeoutOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Timeout(d)
}

// Bulkhead caps how many requests the handler serves at once; the overflow
// answers 503 — at once, or, given [BulkheadQueue] and [MaxWait], once the
// queue is full or the wait ran out. It is the SDK's resilience bulkhead.
//
// The values are the defaults of the operation's settings
// <service>-<kind>-<operation>-bulkhead, -bulkhead-queue and -bulkhead-wait, which
// config/<env>.* and <PRODUCT>_<NAME> override; a bulkhead below one stops
// the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Bulkhead(maxConcurrent int, opts ...BulkheadOption) OperationOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Bulkhead(maxConcurrent, opts...)
}
