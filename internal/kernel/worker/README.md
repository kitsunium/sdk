# worker

Generic goroutine-lifecycle primitive for the SDK kernel. Stdlib-only.

```go
import "github.com/kitsunium/sdk/internal/kernel/worker"

// Spawn a background loop. It MUST return promptly once stop is closed.
d := worker.Start(func(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
			// ... do a unit of work ...
		}
	}
})

// Stop is idempotent and joins: it returns only after the loop has returned.
d.Stop()
```

`LoopDaemon` wraps a single goroutine with an idempotent `Stop` (close-once +
join) and a `Done` channel closed when the loop returns. `Every` layers a
ticker loop on top:

```go
d := worker.Every(time.Second, func() { /* fires each interval */ })
d.Stop() // stops the ticker and joins the goroutine
```

It collapses the `stop/stopOnce/done/doneOnce` scaffold the async drainer and
the s3/cloudwatch batching sinks each hand-rolled. Emits no errors.

See ADR 0014 §D6 for the design rationale.
