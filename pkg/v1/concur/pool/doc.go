// Package pool runs tasks on a fixed set of long-lived workers fed by a
// bounded queue: a refusal when there is no room, a drain, and a task's
// panic kept on its worker instead of taking the process down.
//
//	p := pool.NewPool(ctx, pool.Config{Workers: 8, Queue: 64,
//	    OnPanic: func(pv pool.PanicValue) { log.Print(pv) },
//	})
//	if err := p.Submit(ctx, handle); errors.Is(err, pool.PoolFull) {
//	    // shed the load: every worker busy and the queue full
//	}
//	err := p.Drain(ctx) // runs what is queued, then the workers exit
//
// It is the SDK's own worker pool, published as an alias of the kernel
// package its services run on (ADR 0159 §4, ADR 0216).
//
// # When to use it
//
// When the same kind of work arrives for as long as a service lives — a
// request pipeline, a probe per check, a send per batch — and its
// concurrency must be bounded without a goroutine per task. For one fan-out
// that is waited on once, the group package is the tool; for one daemon
// goroutine, worker.
//
// # Room, and the refusal
//
// Without a queue ([Config].Queue 0) [Pool].Submit hands the task to an idle
// worker or returns [PoolFull] at once: it never parks. With a queue, a full
// one is waited on within the submitter's context, and PoolFull then wraps
// that context's error. After [Pool].Drain or [Pool].Stop, Submit returns
// [PoolClosed].
//
// # Context
//
// A task receives the pool's context — the one given to [NewPool], cancelled by
// Stop —, not the submitter's: the values a task needs travel in its closure,
// which is what keeps a Submit free of allocation.
//
// # Faults
//
// A task's panic is recovered on its worker into a [PanicValue] — the value
// and the worker's stack — handed to [Config].OnPanic, and the worker serves
// the next task. Without an OnPanic the panic is raised again: a fault nobody
// observes is never swallowed. A task's error goes to [Config].OnError.
package pool
