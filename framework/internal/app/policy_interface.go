package app

import (
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// policyGuard is one kind of policy: the settings it declares, the step the
// graph draws, and the SDK runner its settings' values make.
type policyGuard interface {
	// declare declares the policy's settings in svc, for the operation n.
	declare(svc *Service, n *nodeBase)
	// mechanic is the step as the graph draws it, with the values it runs
	// with in the app that runs it — its defaults before.
	mechanic() model.Mechanic
	// runner is the SDK runner of those values, on clk.
	runner(clk clock.Timed) resilience.Runner
}

// RateLimitConfigurer configures a [RateLimit].
type RateLimitConfigurer interface {
	rateLimitConfigure(o *rateLimitGuard)
}

// BulkheadConfigurer configures a [Bulkhead].
type BulkheadConfigurer interface {
	bulkheadConfigure(o *bulkheadGuard)
}

// WaitConfigurer configures a [RateLimit] and a [Bulkhead] alike.
type WaitConfigurer interface {
	RateLimitConfigurer
	BulkheadConfigurer
}
