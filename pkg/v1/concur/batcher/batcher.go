//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/batcher .

// Package batcher coalesces items into batches: Add appends, and a deliver
// function you supply receives them a batch at a time — when a count or a
// weight cap is reached, on a ticker, or when you Flush or Close.
//
//	b := batcher.NewBatcher(func(ctx context.Context, batch []Event) error {
//	    return store.InsertMany(ctx, batch) // one round trip for many events
//	}, batcher.Config[Event]{
//	    MaxItems:   500,
//	    FlushEvery: time.Second,
//	    WeightOf:   func(e Event) int64 { return int64(len(e.Body)) },
//	    MaxWeight:  1 << 20,
//	})
//	err := b.Add(ctx, evt) // flushes at once when a cap is reached
//	err = b.Close(ctx)     // stops the ticker, delivers what is pending
//
// It is the batching primitive under the SDK's database and cloud log
// writers, published as an alias of that kernel package (ADR 0159 §4).
//
// # When to use it
//
// When each item costs a round trip that many items could share — a bulk
// insert, a log shipment, a metrics push — and a bounded delay is an
// acceptable price. It is not a queue: nothing is durable, a batch that fails
// is returned to whoever triggered it rather than retried, and a process that
// dies loses what is pending (the SDK's queue domain is the durable one).
//
// # When a batch is delivered
//
//   - Add delivers eagerly, on the caller's goroutine, once the pending batch
//     reaches [Config].MaxItems items or [Config].MaxWeight — the sum of
//     [Config].WeightOf over the batch; a nil WeightOf weighs every item 1 and
//     ignores MaxWeight.
//   - A positive [Config].FlushEvery starts one ticker goroutine that delivers
//     whatever is pending on each tick, on [Config].Clock — the wall clock
//     when nil, a pkg/v1/clock.ManualClock in a test. A failure on that path
//     has no caller to return to and goes to [Config].OnError.
//   - Flush delivers what is pending now; Close stops the ticker and delivers
//     what is pending with the context it is given, then refuses every later
//     Add and Flush with [BatcherClosed].
//
// The zero [Config] is usable: no cap, no ticker, so items wait for an
// explicit Flush or Close.
//
// # Concurrency
//
// Add, Flush and Close are safe from any number of goroutines. The pending
// batch is swapped out under a lock and delivered outside it, so a slow
// delivery never blocks a producer appending to the next batch. The deliver
// function is never entered concurrently — it may append to a shared slice or
// write to one connection without a lock of its own — but two batches racing
// to it are not ordered.
//
// # Errors
//
// [BatcherClosed] answers an Add or a Flush after Close. A deliver function's
// error comes back wrapped as [BatcherDeliverFailed], its cause still reachable
// with errors.Is and errors.As.
package batcher
