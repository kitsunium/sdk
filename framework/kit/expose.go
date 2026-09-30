// Package kit — exposures: an operation an endpoint serves over HTTP, and
// who may run it.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// ExposeConfigurer configures an exposure ([Command].Expose, [Query].Expose):
// every [EndpointOption] — [Name], [MaxBody], [RateLimitPerClient]… — and
// the markers that say who may run an operation declaring no rule of its
// own: [Anyone], [AnyUser].
type ExposeOption = ikit.ExposeConfigurer

// Anyone says that an exposure is open to everyone, on purpose: its
// operation declares no permission and no rule, and whoever reaches the
// route may run it — a public form, say. It says so on the exposure's node,
// where the diagram and the API page read it. An exposure whose operation
// declares no rule refuses the start unless it says this, or [AnyUser].
//
//	var Report = Service.Command("report", file).Expose("POST /reports", kit.Anyone())
//
// It adds no authentication: an operation that asks for a user ([Auth])
// takes [AnyUser] instead, and one with [AuthOptional] still knows the
// caller who signed in.
func Anyone() ExposeOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Anyone()
}

// AnyUser says that an exposure is open to any signed-in user, on purpose:
// its operation asks for a user ([Auth]) and declares no permission and no
// rule — its handler reads [UserID] and keeps to what that user owns.
//
//	var MyOrders = Service.Query("my-orders", myOrders, kit.Auth()).Expose("GET /orders", kit.AnyUser())
//
// It adds no authentication of its own: an exposure authenticates as its
// operation asks, so the operation declares [Auth].
func AnyUser() ExposeOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.AnyUser()
}
