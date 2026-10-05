package kit

import (
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Empty is the request or the response of an endpoint that has none. An
// endpoint answering EmptyValue replies 204 No Content.
type Empty = ikit.EmptyValue

// Endpoint is an HTTP endpoint with a typed request and a typed response.
// Declare it with [Service].Endpoint; call it from another service with
// [Endpoint].Call, which is how the diagram sees service-to-service calls.
type Endpoint[Req, Resp any] = ikit.EndpointService[Req, Resp]

// EndpointOption configures an endpoint.
type EndpointOption = ikit.EndpointConfigurer

// OperationOption configures an endpoint, a command or a query alike: the
// authentication it asks for ([Auth], [AuthOptional]) and the policies in
// front of its handler ([RateLimit], [RateLimitPerClient], [Timeout],
// [Bulkhead]).
type OperationOption = ikit.OperationOption

// Name overrides the endpoint's name, which otherwise is its handler's
// function name, or its route when the handler is a function literal.
func Name(name string) EndpointOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Name(name)
}

// MaxBody bounds the request body the endpoint reads. The default is
// [DefaultMaxBody].
func MaxBody(bytes int64) EndpointOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MaxBody(bytes)
}

// RateLimit puts a token bucket in front of the handler: perSecond tokens a
// second, up to burst at once. An empty bucket answers 429 at once. It is the
// SDK's resilience rate limiter.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RateLimit(perSecond float64, burst int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RateLimit(perSecond, burst)
}

// Timeout cancels the handler's context after d and answers 504. It is the
// SDK's resilience timeout.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Timeout(d time.Duration) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Timeout(d)
}

// Bulkhead caps how many requests the handler serves at once; the overflow
// answers 503. It is the SDK's resilience bulkhead.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Bulkhead(maxConcurrent int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Bulkhead(maxConcurrent)
}
