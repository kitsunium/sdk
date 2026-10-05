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

// maxBody is MaxBody's body: decl_gen.go writes MaxBody, from the
// design, as one call of it.
func maxBody(bytes int64) EndpointOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MaxBody(bytes)
}

// rateLimit is RateLimit's body: decl_gen.go writes RateLimit, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func rateLimit(perSecond float64, burst int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RateLimit(perSecond, burst)
}

// timeout is Timeout's body: decl_gen.go writes Timeout, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func timeout(d time.Duration) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Timeout(d)
}

// bulkhead is Bulkhead's body: decl_gen.go writes Bulkhead, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func bulkhead(maxConcurrent int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Bulkhead(maxConcurrent)
}
