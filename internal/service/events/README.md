# events

Package `events` is the concrete `core/events.Bus`: one fact is published, and
every listener registered for that fact's Go type reacts to it, in ascending
priority, on the publisher's goroutine, before `Publish` returns.

`On[E]` is the registration a caller writes. It is a package-level generic
function rather than a method because Go methods cannot take type parameters —
and a `Bus[E]` would carry exactly one event type, which is a channel and not a
bus. It derives the routing key from `E`, wraps the typed function in the
erased `Listener` the bus dispatches, and refuses an interface `E`. The type
assertion that buys back the typing costs **2.4 ns per listener call**, measured
against an identical erased control in `BENCH.md`.

Three things it makes impossible. **A dispatch order that changes between
runs** — the subscription list is kept sorted at registration, ties resolve to
registration order, and both are asserted on the observable trace. **A broken
listener that takes the publisher down** — a panic is recovered on the spot,
reported as `ListenerPanicked` with the value and the originating stack in
fields, and the remaining listeners still run. **A listener that silently
vetoes its siblings** — stopping the dispatch requires `MayHalt` at the
registration site; without it the attempt is `HaltNotPermitted`, the dispatch
continues, and the refusal is reported.

A listener error never short-circuits. Everything that failed is aggregated
with `errors.Join`, carrying the bus's `ListenerFailed` verdict and the
listener's own error side by side, so `errs.HasCode` and the caller's
`errors.Is` both answer on the same value.

The membership is a `kernel/snapshot` copy-on-write value, so a dispatch is one
atomic load with no lock and no allocation, and a listener may subscribe or
unsubscribe from inside the dispatch that is walking the list.

ADR 0053. Facade: `pkg/v1/events`. See `CLAUDE.md` and `BENCH.md`.
