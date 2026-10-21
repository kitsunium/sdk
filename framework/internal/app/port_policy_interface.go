package app

import (
	"context"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
)

// PortPolicyConfigurer guards the calls a port makes: [Retry], [Budget],
// [Breaker], [Hedge], [Idempotent], [Throttle] and [Timeout], given to
// [PortService.Guard].
type PortPolicyConfigurer interface {
	portPolicyConfigure(g *portGuards)
}

// TimeoutConfigurer configures an endpoint, a command, a query and a port
// alike: [Timeout].
type TimeoutConfigurer interface {
	OperationOption
	PortPolicyConfigurer
}

// RetryConfigurer configures a [Retry].
type RetryConfigurer interface {
	retryConfigure(g *retryGuard)
}

// BreakerConfigurer configures a [Breaker].
type BreakerConfigurer interface {
	breakerConfigure(g *breakerGuard)
}

// portLayer is one policy around a port's calls: where it sits in the
// composition, the settings it declares, the step the graph draws, and the
// SDK runner a call runs through. A layer is reached only through this
// interface, so a product that guards no port links none of their engines.
type portLayer interface {
	// rank is the layer's place, the outermost first (rankRetry … rankTimeout).
	rank() int
	// declare declares the layer's settings in svc, for the port n.
	declare(svc *Service, n *nodeBase)
	// mechanic is the step as the graph draws it, with the values it runs
	// with in the app that runs it — its defaults before.
	mechanic() model.Mechanic
	// runner is the SDK runner the call c runs through.
	runner(c *portCall) resilience.Runner
	// started readies the layer in an app that starts, and answers what
	// stops the start.
	started(a *App, port string) phrase
}

// portBreaker is a breaker layer, as the start, the readiness, the
// operator's marks and the CLI reach it.
type portBreaker interface {
	// state is the breaker of the app's start; nil when the SDK refused it.
	state(a *App) resilience.BreakerState
	// command is Main's `breaker` command of app a: reached through a
	// breaker, so that a product that declares none links none of it.
	command(a *App) func(ctx context.Context, args []string) int
	// attach readies the app's breakers bs at its start: reached through
	// one of them, for the same reason.
	attach(a *App, bs []breakerAt, cli bool) error
}
