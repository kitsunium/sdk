// Package pool provides Pool, a bounded worker pool: a fixed set of
// long-lived workers consuming a bounded queue of tasks, with a refusal when
// there is no room, a drain, and a task's panic isolated on its worker.
//
// It is the shape group and worker are not. group is a one-shot fan-out —
// a goroutine per task, one wait, used once —, worker is one daemon. A
// service that runs the same kind of work for as long as it lives — a probe
// per check, a fire per scheduled job, a sender per batch — needs workers
// that outlive a task, a queue that refuses when full instead of growing,
// and a fault in one task that does not end the others.
//
// Submit hands a task over: without a queue (Config.Queue 0) an idle worker
// takes it or PoolFull answers at once — a refusal, never a park (ADR
// 0031) —; with one, a full queue is waited on within the submitter's
// context. Drain stops accepting and runs what is queued; Stop abandons the
// queue and cancels the context the running tasks received; both wait for
// the workers within their own context. A task runs under the pool's
// context — New's, cancelled by Stop —, not the submitter's: a per-task
// context carrying the submitter's values would cost an allocation per
// task, so the values a task needs travel in its closure.
//
// A task's panic is recovered on its worker into a PanicValue with the
// worker's stack and handed to Config.OnPanic, and the worker serves the
// next task: unlike group, which re-raises because it is one-shot, the pool
// outlives a task's fault. With no OnPanic the panic is raised again — a
// fault nobody observes is not swallowed.
//
// Stdlib-only and generic. Range 0.1.6.* (ADR 0005 kernel block, ADR 0216).
//
// Package pool — the Config value type.
//
// Package pool — declares the sentinels returned by Submit.
package pool
