# batcher

Generic coalescing buffer (ADR 0014). `Batcher[T]` accumulates items via `Add`
and delivers them to a `Sink[T]` closure in batches — flushed eagerly at a
`MaxItems` / `MaxWeight` cap, on an optional `FlushEvery` ticker, or on `Flush`
/ `Close`.

```go
b := batcher.NewBatcher(deliver, batcher.Config[Event]{
    MaxItems:   1000,
    FlushEvery: time.Second,
    WeightOf:   func(e Event) int64 { return int64(len(e.Body)) },
    MaxWeight:  1 << 20,
})
_ = b.Add(ctx, evt) // eager flush when a cap is reached
_ = b.Flush(ctx)    // deliver the pending batch now
_ = b.Close(ctx)    // stop the ticker + final flush
```

The deliver closure carries every sink-specific concern: a pre-delivery
reorder, a per-batch key, or a byte-vs-count weight. A nil `WeightOf` means
count-only batching (each item weighs 1, `MaxWeight` is ignored).

Errors are typed: `Add` / `Flush` after `Close` return `batcher.Closed`; a
deliver-closure failure is wrapped as `batcher.DeliverFailed` with the original
cause reachable via `errors.Is`.

stdlib-only, domain-neutral, kernel-layer. See `CLAUDE.md` for the full
contract.
