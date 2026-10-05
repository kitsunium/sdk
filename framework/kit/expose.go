package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// ExposeConfigurer configures an exposure ([Command].Expose, [Query].Expose):
// every [EndpointOption] — [Name], [MaxBody], [RateLimitPerClient]… — and
// the markers that say who may run an operation declaring no rule of its
// own: [Anyone], [AnyUser].
type ExposeOption = ikit.ExposeConfigurer

// anyone is Anyone's body: decl_gen.go writes Anyone, from the
// design, as one call of it.
func anyone() ExposeOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Anyone()
}

// anyUser is AnyUser's body: decl_gen.go writes AnyUser, from the
// design, as one call of it.
func anyUser() ExposeOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.AnyUser()
}
