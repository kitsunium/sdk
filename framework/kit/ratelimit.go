package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// rateLimitPerClient is RateLimitPerClient's body: decl_gen.go writes RateLimitPerClient, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func rateLimitPerClient(perSecond float64, burst int) OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RateLimitPerClient(perSecond, burst)
}
