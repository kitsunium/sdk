package kit

import "github.com/kitsunium/sdk/framework/model"

// ExposeConfigurer configures an exposure ([Command.Expose], [Query.Expose]):
// every [EndpointConfigurer] — [Name], [MaxBody], [RateLimitPerClient]… — and
// the markers that say who may run an operation declaring no rule of its
// own: [Anyone], [AnyUser].
type ExposeConfigurer interface {
	exposeConfigure(o *endpointOptions)
}

// exposed is an operation an endpoint exposes, whatever its types.
type exposed interface {
	node
	// authMode is how the operation asks for a user: its exposure
	// authenticates the request so.
	authMode() string
	// guarded reports whether it declares who may run it: a permission or
	// a rule.
	guarded() bool
	edgeKind() model.EdgeKind
}

// authModeSource is an operation an endpoint of its types can expose.
type authModeSource[Req, Resp any] interface {
	Operation[Req, Resp]
	authMode() string
	guarded() bool
}
