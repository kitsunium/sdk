# commit

Group commit (#803). `Group[T]` coalesces concurrent `Do` calls into one
flush: every caller contributes an item, one goroutine runs the flush over all
of them, and every caller receives that flush's verdict — N callers that each
need their item durable share one fsync.

```go
group := commit.New(func(ctx context.Context, records []Record) error {
    for _, r := range records {
        if _, err := journal.Write(r.Bytes()); err != nil {
            return err
        }
    }
    return journal.Sync() // one fsync for the whole batch
}, commit.Config{MaxItems: 256, MaxWait: 0})

err := group.Do(ctx, record) // returns once the flush carrying record has run
```

A batch starts with its first item and gathers followers for `MaxWait` on
`Config.Clock` (a `clock.ManualClock` in tests), or until `MaxItems`. Flushes
run one at a time, in the order their batches started; a batch keeps
gathering while the one before it flushes, so with a zero `MaxWait` the
callers that arrive during one flush share the next.

A caller whose context ends before its flush returns `ctx.Err()` — and its
item is flushed all the same. `Do` after `Close` returns `commit.Closed`
(`0.1.4.1`); a panicking flush panics every participant.

stdlib-only, domain-neutral, kernel-layer. See `CLAUDE.md` for the contract.
