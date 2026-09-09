# topic

Typed in-process broadcast `Topic[T]` for the SDK kernel — stdlib-only, no
domain vocabulary. Every published value reaches every current subscriber.

```go
var updates topic.Topic[Position]

sub := updates.Subscribe(topic.DropOldest(64)) // the policy is NOT optional
defer sub.Unsubscribe()

go func() {
    for {
        select {
        case p := <-sub.Values():
            render(p)
        case <-sub.Done():   // the value channel is never closed; this is the end signal
            return
        }
    }
}()

updates.Publish(ctx, p) // returns how many subscribers took it
```

The zero `Topic` is ready to use, as `sync.Mutex` is.

**The delivery policy has no default, on purpose.** `Block(n)` couples the
producer to the subscriber (bounded by `Publish`'s context); `DropOldest(n)`
keeps the freshest window; `DropNewest(n)` keeps the earliest one. Both drop
policies **count** what they discard, readable through `sub.Dropped()` — so a
slow subscriber never stalls the producer, and never loses data, in silence. A
zero `DeliveryConfig` is refused at the call that omitted it (ADR 0031).

**Unsubscribing during a live fan-out is safe** and costs no other subscriber
its copy: `Publish` delivers outside every lock, and the value channel is never
closed, so a "send on closed channel" panic in the publisher is unreachable
rather than unlikely. The price is that ranging over `sub.Values()` never
ends — select on `sub.Done()` alongside it.

`Publish` allocates nothing (see `BENCH.md`); the cost sits on membership churn
instead. See `CLAUDE.md` for the design.
