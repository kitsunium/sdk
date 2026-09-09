//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/lifecycle .

// Package lifecycle is the public facade for the SDK's ordered start/stop
// domain: components come up in the order you declare them, and go down in the
// exact reverse.
//
//	app := lifecycle.New(lifecycle.Config{StopTimeout: 15 * time.Second})
//
//	// the order of these three calls IS the dependency order
//	_ = app.Add(lifecycle.Component{Name: "db", Start: db.Open, Stop: db.Close})
//	_ = app.Add(lifecycle.Component{Name: "cache", Start: cache.Dial, Stop: cache.Close})
//	_ = app.Add(lifecycle.Component{Name: "http", Start: srv.Start, Stop: srv.Shutdown})
//
//	// start, wait for SIGTERM, stop http → cache → db
//	if err := lifecycle.Run(ctx, app, lifecycle.RunConfig{Signals: []lifecycle.Signal{term}}); err != nil {
//		return err
//	}
//
// # What it makes impossible
//
// **A partial start that leaks.** If the fourth component of six fails, the
// three that are up are stopped, in reverse, before [Lifecycle.Start] returns
// — through the same code path an ordinary [Lifecycle.Stop] uses, not a second
// copy of it. The component that failed is deliberately NOT stopped: its Start
// returned an error, so it never handed back a running thing, and a Stop on a
// half-constructed component is how a double-close gets written. A Start that
// fails owns what it acquired, exactly as a Go constructor does.
//
// The unwind also runs when the start failed BECAUSE the context was
// cancelled: each Stop is called with a context derived by
// context.WithoutCancel, so a cleanup is never driven by the cancellation that
// made it necessary.
//
// **A shutdown budget one component can spend on everyone's behalf.**
// [Config.StopTimeout] is the budget ONE component gets, not a budget for the
// whole shutdown. A component that will not finish is abandoned at its own
// deadline and the shutdown continues; every component before it in the
// reverse order still gets its full budget. The price is stated rather than
// hidden: a Stop of n components returns in at most n × StopTimeout.
//
// # What "the budget expired" means
//
// Exactly three things, and deliberately nothing more:
//
//   - The context handed to that component's Stop is cancelled. That is an
//     ANNOUNCEMENT — the one piece of information the component cannot
//     otherwise have — and it is what a cooperative Stop selects on.
//   - The lifecycle stops WAITING and moves to the next component.
//   - A [StopTimeout] error naming the component is collected into the
//     aggregate that [Lifecycle.Stop] returns.
//
// The goroutine is not killed, because Go cannot kill one, and nothing the
// component owns is closed on its behalf. Severing a resource under live work
// is how an unfinishable stream used to take a whole HTTP drain down with it;
// a budget that expires must not cut short what was about to finish.
//
// A non-positive [Config.StopTimeout] CLAMPS to [DefaultStopTimeout] (30s).
// It is never read as "stop immediately": a zero there is what an unset field
// looks like, and reporting a timeout for components that were about to
// succeed is not a shutdown policy.
//
// # What it deliberately does not do
//
// There is no dependency GRAPH. A linear sequence already is a topological
// order, and the caller — who wrote the constructors — is the only party that
// knows it. No edges, no cycle detection, no parallel start. The cost is
// stated: the SDK cannot tell you that you ordered them wrong, because it has
// nothing to check the order against.
//
// There is no autowiring, no reflection over constructors and no container. A
// convention that decides what gets injected where is a framework, and this
// SDK is a toolbox. Explicit constructors called in an order you can read are
// the whole mechanism.
//
// Signals and sd_notify are OPT-IN fields of [RunConfig], never a default:
// they are process-wide, observable side effects, and a library that installs
// them because it was imported fights the caller's own main. Both delegate to
// the SDK packages that already implement them.
//
// # Errors
//
// A failed [Lifecycle.Start] returns an errors.Join carrying [StartFailed]
// AND the component's own error, side by side rather than one wrapping the
// other — so errs.HasCode(err, CodeStartFailed) and the caller's own
// errors.Is both answer. [Lifecycle.Stop] aggregates the same way, one entry
// per component that failed or overran.
//
// A component that PANICS is recovered and reported as [ComponentPanicked]
// with the panic value in a field. A panic escaping a Start would skip the
// unwind entirely and leak every component already up, which is the failure
// this domain exists to prevent.
package lifecycle

import (
	"context"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

const (
	// PhaseStart reports a call to a component's Start.
	PhaseStart Phase = corelc.PhaseStart
	// PhaseStop reports a call to a component's Stop.
	PhaseStop Phase = corelc.PhaseStop
	// DefaultStopTimeout is the per-component budget a non-positive
	// Config.StopTimeout clamps to.
	DefaultStopTimeout time.Duration = svclc.DefaultStopTimeout
)

// Start is the public alias for the ctx-aware bring-up half of a component.
type Start = corelc.Start

// Stop is the public alias for the ctx-aware teardown half of a component.
type Stop = corelc.Stop

// Lifecycle is the public alias for the ordered start/stop contract.
type Lifecycle = corelc.Lifecycle

// Component is the public alias for one named Start/Stop registration.
type Component = corelc.ComponentValue

// Transition is the public alias for one reported component move.
type Transition = corelc.TransitionValue

// Phase is the public alias for which half of a component a Transition
// reports.
type Phase = corelc.Phase

// Signal is the public alias for a typed OS signal, re-exported here so a
// caller wiring RunConfig.Signals needs no second import.
type Signal = coreproc.Signal

// Config is the public alias for the engine's construction parameters.
type Config = svclc.Config

// RunConfig is the public alias for Run's opt-in supervision wiring.
type RunConfig = svclc.RunConfig

var (
	// InvalidComponent is returned by Add for a component that could never
	// run — an empty Name, a nil Start, a nil Stop. The "missing" field names
	// which.
	InvalidComponent = corelc.InvalidComponent
	// DuplicateComponent is returned by Add when the name is already taken.
	DuplicateComponent = corelc.DuplicateComponent
	// LifecycleRunning is returned by Add, and by a second Start, while the
	// Lifecycle is started. Add before Start, or after Stop returns.
	LifecycleRunning = corelc.LifecycleRunning
	// ComponentPanicked is the failure of a component whose Start or Stop
	// panicked. It was recovered; the panic value travels as a field.
	ComponentPanicked = corelc.ComponentPanicked
	// StartFailed is joined with the component's own error when a Start
	// aborts the sequence. The components already up were stopped first.
	StartFailed = svclc.StartFailed
	// StopFailed is joined with the component's own error when its Stop
	// returns one. The shutdown continued with the remaining components.
	StopFailed = svclc.StopFailed
	// StopTimeout reports a component whose Stop had not returned when its
	// budget expired. Its context was cancelled and it was abandoned — never
	// killed, and nothing it owns was closed on its behalf.
	StopTimeout = svclc.StopTimeout
	// UnwindFailed marks a failure DURING the cleanup of a partial start. It
	// travels alongside the start failure, never instead of it.
	UnwindFailed = svclc.UnwindFailed
	// ReadinessFailed is returned when an opt-in sd_notify datagram could not
	// be delivered.
	ReadinessFailed = svclc.ReadinessFailed
)

// New returns a Lifecycle. It cannot fail: a nil cfg.Clock falls back to the
// wall clock, a non-positive cfg.StopTimeout to DefaultStopTimeout, and a nil
// cfg.OnTransition to no observation. What can fail fails at Add.
func New(cfg Config) Lifecycle {
	//: delegate to the service constructor.
	return svclc.New(cfg)
}

// Run starts every component, waits for ctx or one of cfg.Signals, then stops
// them in reverse order. See the package documentation for what the opt-in
// fields wire, and what the zero RunConfig deliberately does not.
func Run(ctx context.Context, lc Lifecycle, cfg RunConfig) error {
	//: delegate to the service helper.
	return svclc.Run(ctx, lc, cfg)
}
