// Package group — the typed fan-out built on the Group.
//
// Package group runs a set of tasks concurrently and gives their caller one
// place to wait: one first error — or, from [NewJoined], every error — one
// bounded degree of parallelism, and — the part the familiar shape leaves out —
// the panic that a child goroutine would otherwise take the whole process down
// with. It is a kernel primitive (stdlib-only, fully domain-neutral: Group, Go,
// Wait, Collect; no Job, no Task, no Worker appears in a signature).
//
// # One error, or every error
//
// A group from [New] reports the FIRST error, because almost every error after
// it is a consequence of the cancellation the first one caused. A group from
// [NewJoined] reports EVERY task's error, joined with errors.Join in submission
// order — for tasks whose failures are independent facts, such as workers
// polling one medium, where the second failure is not an echo of the first.
// Both cancel the siblings on the first failure, and in both the first failure
// is the context's cause.
//
// # A panic in a child is delivered, not fatal
//
// A goroutine that panics and is not recovered ON ITS OWN STACK kills the
// process. No amount of recover() in the parent helps, which is why the common
// "spawn N goroutines, collect errors" helper turns one bad input into a crash
// with a stack pointing at a goroutine the reader has never seen. Here the
// recovery happens inside the child, together with the stack of the goroutine
// that actually failed, and is re-raised in [Group.Wait] as a [PanicValue].
//
// The re-raise happens AFTER every other task has returned, so the structured
// guarantee survives the fault: when Wait leaves — by return or by panic — the
// group owns no running goroutine.
//
// # What Wait guarantees, and what it cannot
//
// Wait returns when every task started by [Group.Go] has RETURNED. It does not
// return when the context is cancelled, because cancellation is a request and
// Go has no way to force a goroutine to honour one.
//
// So a task that ignores its context keeps Wait blocked for as long as it
// runs, and that is the contract rather than a defect: the alternative —
// returning while goroutines are still live — is precisely the leak structured
// concurrency exists to prevent, and it would hand the caller a "finished"
// group that is still writing to their memory. Bound such a task from the
// inside (a deadline on the I/O it performs). A group cannot bound it from the
// outside without abandoning it.
//
// # Limits
//
// The concurrency limit is a real bound taken before the goroutine is created,
// so N submissions against a limit of L hold L goroutines, not N. A
// non-positive limit is clamped to 1 and [Unlimited] must be spelled out —
// see [New].
//
// Package group — the panic carried off a task's goroutine and re-raised in
// the waiter.
package group
