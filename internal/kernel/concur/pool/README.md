# pool

A bounded worker pool (#804, ADR 0216). `Pool` runs tasks on a fixed set of
long-lived workers fed by a bounded queue: a refusal when there is no room, a
drain, and a task's panic kept on its worker.

```go
p := pool.NewPool(ctx, pool.Config{
    Workers: 8,
    Queue:   64,
    OnPanic: func(pv pool.PanicValue) { report(pv) },
    OnError: func(err error) { report(err) },
})
if err := p.Submit(ctx, handle); errs.HasCode(err, pool.CodePoolFull) {
    // shed the load
}
err := p.Drain(ctx) // runs what is queued, the workers exit
```

- `Queue` 0: `Submit` hands the task to an idle worker or returns `PoolFull`
  at once. `Queue` > 0: a full queue is waited on within the submitter's
  context.
- `Drain(ctx)` runs what is queued; `Stop(ctx)` abandons it and cancels the
  running tasks' context. Both wait for the workers within `ctx`.
- A task receives the pool's context; what it needs from its submitter travels
  in its closure, so a `Submit` allocates nothing.

Published as `pkg/v1/concur/pool`.
