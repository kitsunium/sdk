# ADR 0043 — draining is announced to the handler, not imposed on it

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0029](0029-sdk-net-domain.md) §shutdown — the documented outcome of shutting down with a long-lived request in flight
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md), [ADR 0018](0018-sdk-cross-platform-portability.md)

## Context

ADR 0029 gave the server a drain: stop accepting, let in-flight requests
finish, close. That is correct for a request/response workload, where "in
flight" is bounded by the handler returning.

Adding Server-Sent Events made the unbounded case reachable, and **measuring it
first turned up a defect that had been there all along**. With one open stream:

| | before |
|---|---|
| `Shutdown` duration | the caller's **entire** budget (5 s of a 5 s context) |
| result | `DRAIN_TIMEOUT` |
| how the handler learnt | `closeLive()` severed its socket underneath it |

The cause chain is entirely in existing code: `shutdownHTTP` →
`httpAdapter.shutdown` → `http.Server.Shutdown(ctx)`, which by its own contract
waits for in-flight requests. **A request that never ends never becomes idle.**
`waitIdle` then returns at once because the budget is already spent, and
`s.stop()` / `closeLive()` run after.

So `DRAIN_TIMEOUT` was not an edge case: it was **the normal outcome of every
deployment, for every connected client** — and nothing in the suite covered it,
because no test held a request open across a shutdown.

## Decision

1. **The adapter publishes a drain *signal*.** A channel, closed at the top of
   `stop()` — before the bridge closes and before `Shutdown` begins waiting —
   placed on every request context through `http.Server.BaseContext` and read
   with `corenet.DrainSignal(ctx)`. A handler that wants to end early selects on
   it; a handler that does not is unaffected.

2. **It is deliberately NOT a context cancellation.** Cancelling the request
   context would tell every handler to abandon the response **that the drain
   exists to let it finish**. The signal says "no more work after this one",
   which is what draining means; cancellation says "stop now", which is what
   closing means. Conflating them turns a graceful shutdown into an abrupt one
   for every handler that respects `ctx.Done()` — i.e. the well-behaved ones.
   `TestDrainSignalReachesAPlainHTTPHandler` asserts, with no SSE involved, that
   the channel arrives **and** `r.Context()` is not cancelled.

3. **The change is additive.** No existing handler changes behaviour; a handler
   that ignores the signal drains exactly as before. What changes is that a
   long-lived handler now *can* cooperate, so the documented outcome of ADR 0029
   §shutdown is no longer "timeout" for that class.

4. **The fix is mutation-checked, not merely tested.** Suppressing
   `close(a.draining)` restores the 5-second `DRAIN_TIMEOUT` and fails only the
   new open-stream cases. A test that passes both with and without the fix would
   prove nothing about it.

## Consequences

- Measured after: **40 ms, clean**, same budget.
- `TestHTTPAdapterDrainsOnShutdown` now covers one, three, and mixed open
  streams. The gap it had was structural — it drained requests that end.
- **`http.Flusher` is confirmed available behind the adapter**, pinned by a test
  whose handler cannot progress until the client has received the first event.
  It deadlocks if the response is buffered, so "streaming works here" is an
  executable claim rather than an assumption.
- A handler that ignores the signal and never returns still consumes the budget.
  The signal makes cooperation *possible*, not mandatory; forcing it is
  cancellation, which Decision 2 rejects.

## Why not

- **Cancel the request context on drain.** Rejected — Decision 2. It punishes
  exactly the handlers that follow Go's own convention.
- **Give SSE its own shutdown path.** Rejected: the defect is not
  SSE-specific. Any long-lived handler — a long poll, a large upload, a slow
  proxy — hit it identically. A format-specific fix would have left the general
  case broken and harder to find the next time.
- **Shorten the drain budget so the timeout hurts less.** Rejected: it makes the
  symptom cheaper and the cause permanent, and it would cut short the
  request/response drains that were working correctly.
- **Document `DRAIN_TIMEOUT` as expected with streaming.** Rejected: it was
  reachable without streaming, it severed sockets under handlers that had no way
  to know, and normalising it would have retired a real bug into a convention.

## References

- `internal/core/net/drain.go` (`WithDrainSignal` / `DrainSignal`)
- `internal/service/net/server/{http_adapter.go,lifecycle.go}`
- `pkg/v1/server/server_external_test.go` — the open-stream drain cases and
  `TestDrainSignalReachesAPlainHTTPHandler`
- ADR 0029 §shutdown (amended here)
