# group

Structured concurrency for the SDK kernel — stdlib-only, no domain vocabulary.
Run N tasks, wait at one point, get the first error, and receive a panic raised
in a child goroutine instead of losing the process to it.

```go
tasks, ctx := group.New(parent, 4) // at most 4 at a time

for _, url := range urls {
    tasks.Go(func(ctx context.Context) error {
        return fetch(ctx, url)
    })
}

if err := tasks.Wait(); err != nil { // the FIRST failure; the rest were cancelled
    return err
}
```

`Collect` is the typed form — results in submission order, whatever order the
tasks finished in:

```go
sizes, err := group.Collect(ctx, group.Unlimited, probes)
```

Three edges worth knowing before you use it:

- **A panic in a task is re-raised in `Wait`** as a `PanicValue` carrying the
  stack of the goroutine that actually failed — after every sibling has
  returned, so no task outlives `Wait` even on the fault path.
- **A non-positive limit means one, not none.** Pass `group.Unlimited` for no
  bound; a zero that silently meant "unbounded" — or "nothing may run" — is the
  inert policy ADR 0031 removed from this SDK.
- **`Wait` waits for tasks to RETURN, not for the context to be cancelled.** A
  task that ignores cancellation keeps `Wait` blocked. That is the guarantee:
  returning while goroutines are still live is the leak this exists to prevent.

See `CLAUDE.md` for the design, and `BENCH.md` for the measured threshold below
which a group costs more than it saves.
