// Package lifecycle declares the ordered start/stop port of the SDK: the
// [Start] that brings a component up, the [Stop] that takes it down, and the
// [Lifecycle] that owns an ordered set of them and drives it in both
// directions. A core sibling admitted by ADR 0050.
//
// Both halves of a component are FUNCTION ports rather than interfaces — the
// shape internal/core/CLAUDE.md already admits for resilience.Operation and
// scheduler.Job. Each is a single behaviour, so a named func IS the contract
// and needs no adapter at the call site; it is also the narrowest thing
// pkg/v1 can publish. ADR 0039's lesson is that a published port cannot grow
// a method without breaking every downstream implementer at compile time, and
// a func type cannot grow one at all.
//
// # What the order means
//
// The order components are added in IS the dependency order, and shutdown is
// its exact reverse. This package declares no graph, no edges and no
// DependsOn field: a linear sequence already IS a topological order, and the
// caller — who wrote the constructors — is the only party that knows it. The
// price is stated rather than hidden: the SDK cannot detect that the caller
// ordered them wrong, because it has nothing to check the order against.
//
// # What a component owns
//
// A [ComponentValue] is a pairing of a name, a Start and a Stop. Stop is
// called ONLY for a component whose Start returned nil, so a Stop may assume
// its Start succeeded — which is what makes a double-close impossible on the
// failure path. A Start that fails owns whatever it acquired before failing,
// exactly as a Go constructor does.
//
// The engine that drives the sequence, the shutdown budget, and the opt-in
// signal / sd_notify wiring live in internal/service/lifecycle; this package
// owns only the contract, the three domain values, and the typed sentinels a
// registration refuses with.
package lifecycle

import "context"

// Start brings one component up. It MUST honour ctx: the [Lifecycle] passes
// the context Start was called with, so cancelling that context is how a
// caller aborts a startup that is taking too long.
//
// Returning a non-nil error means the component is NOT up. The Lifecycle then
// stops every component that IS up, in reverse order, and never calls this
// component's Stop — a Start that fails owns its own cleanup, which is the
// only rule under which a Stop may assume its Start succeeded.
type Start func(ctx context.Context) error

// Stop takes one component down. It is called only for a component whose
// [Start] returned nil, and only once.
//
// ctx carries this component's shutdown budget and is cancelled when that
// budget expires. Cancellation is an ANNOUNCEMENT, not a severance: the
// Lifecycle stops waiting, but it does not and cannot kill the goroutine, and
// it closes nothing on the component's behalf. A Stop that ignores ctx
// therefore delays nothing except its own report — the components after it in
// the reverse order each get their own full budget.
type Stop func(ctx context.Context) error

// Lifecycle owns an ordered set of [ComponentValue] and drives it up and down.
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the concrete engine stays unexported behind its constructor in
// internal/service/lifecycle.
type Lifecycle interface {
	// Add registers a component at the end of the order. It is refused while
	// the Lifecycle is started ([LifecycleRunning]), on a name already
	// registered ([DuplicateComponent]), and on a component that could never
	// run — empty name, nil Start, nil Stop ([InvalidComponent]).
	Add(component ComponentValue) error
	// Start runs every registered Start in registration order and returns nil
	// once all of them have. On the first failure it stops the components
	// already up, in reverse order, and reports the failure; the Lifecycle is
	// then back in its not-started state, so a caller may fix the cause, Add,
	// and Start again.
	Start(ctx context.Context) error
	// Stop runs the Stop of every component that started, in reverse of the
	// order they started in, and reports what failed. It is idempotent: a
	// Lifecycle that never started, or that has already stopped, returns nil
	// without calling anything.
	Stop(ctx context.Context) error
}
