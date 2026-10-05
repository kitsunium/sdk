// Package lifecycle — hosts Config, the engine's construction parameters.
//
// Package lifecycle — hosts the engine: registration, the state Start and
// Stop share, and the observation hook.
//
// Package lifecycle — hosts Run, the opt-in wiring between a Lifecycle and
// the process supervision the SDK already implements.
//
// Package lifecycle — hosts the bring-up sequence and the partial-start
// unwind that is its whole reason to exist.
//
// Package lifecycle — hosts the shutdown sequence and the per-component
// budget that bounds it.
//
// Package lifecycle — the supervisor: a function run until it is stopped,
// restarted after every early end with a backoff, observed run by run.
//
// A Lifecycle orders components that START and STOP; many of those components
// own a loop that must keep running in between — a consumer, a sweeper, a
// watcher. That loop is where a component usually dies unnoticed: it returns
// an error nobody reads, or panics and takes the process with it. The
// supervisor is the other half of the component: it runs the loop on its own
// goroutine, recovers a panic, restarts after an early end on a backoff
// (kernel/backoff.Value, the one curve the SDK computes), tells an
// observer about every run, and on Stop cancels the loop's context and waits
// for it — which a Lifecycle then budgets like any other Stop.
//
// Package lifecycle — the supervisor's construction parameters and the
// defaults its zero values clamp to.
//
// Package lifecycle — what a supervisor tells its observer.
package lifecycle
