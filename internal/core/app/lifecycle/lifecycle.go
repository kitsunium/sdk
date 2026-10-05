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
