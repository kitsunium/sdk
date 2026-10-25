# ADR 0189 — a loop middleware forwards every callback, and the http1 chain judges one canonical target

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-24 for the
  unified network work (the hot path is the SDK's own handlers and
  middlewares, with no blocking and no allocation, D-N5), carrying step P2 of
  that plan: lane identity, loop middlewares and the `http1` chain. It can be
  revisited while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D3 (the portable backend runs `Loops` lanes, not one loop) and §D6 (the
  profile computes a canonical target and refuses three more shapes of
  request-target); [ADR 0187](0187-the-handler-type-chooses-the-engine.md)
  §Deferred (the loop middlewares it names as their own record)
- **Related**: [ADR 0029](0029-sdk-net-domain.md) D4 (`Middleware[H]` and
  `Chain`), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md)
  (the allocation budgets), [ADR 0031](0031-policy-zero-values-are-never-inert.md)

## Context

The loop engine serves a `LoopHandler` and the `http1` profile serves an
`HTTP1Handler`, but a program that wants routing, a Bearer check, response
fields or an access log writes them inside its handler, by hand, every time.
Three things are missing for that work to move into reusable links without
costing the hot path:

- **A layer of a loop chain can drop a callback silently.** `LoopHandler` has
  six callbacks and two optional capabilities (`Honours`, and a lane count to
  size per-lane state). A wrapper that forgets one — `OnClose`, say — leaks the
  wrapped handler's state per connection and nothing fails.
- **Two links can read two targets.** The codec carried the request-target as
  sent. A router matching `/api/` and an authenticating link exempting
  `/health` read the same bytes differently the moment the target holds
  `/health/../api`, `//api`, `%2e%2e`, `%2f` or an absolute-form whose authority
  is not the Host: the classic way a route escapes its check.
- **State per loop had no count.** `LoopIO.Loop` named the loop, but a handler
  learnt how many there are only by growing a table under a lock on first
  sight, and the portable backend always ran one loop whatever `Loops` said, so
  a handler sized for `Loops` saw a different count after a fallback.

## Decision

### D1 — A lane is one thread or one goroutine, and its count is `Loops` on every backend

`LoopValue.Loops`, zero meaning one (ADR 0187 §D9), is the lane count of a
loop group on every backend: a native backend runs one locked thread a lane,
the portable backend one handler goroutine a lane, its acceptors handing
connections to the lanes in turn. A fallback from a native backend to the
portable one keeps the count. `LoopIO.Loop` is the lane's index, from 0 to
the count less one, and `HTTP1RequestValue.Lane` (renamed from `Loop`) carries
it to an `HTTP1Handler`; `HTTP1Batcher.EndBatch` names it too.

`corenet.LoopStarter` is the optional capability that hears the count:
`OnStart(lanes)` runs once, before any other callback, so a handler sizes a
slice indexed by the lane and no callback grows one. The server calls it on
the chain it serves, before it starts the engine. `http1.LoopHandler`
implements it and builds every lane's state there.

### D2 — A loop middleware's layer forwards every callback, checked at Start

`StreamGroup.UseLoop(mws ...corenet.Middleware[corenet.LoopHandler])`, kind
`OptionLoopMiddleware` (19), recorded on every call as `Use` records its own.
A loop group wraps its handler in them at `Start`, the first listed
outermost; the loop engine honours the kind, and a group no loop handler
serves refuses it with `LoopOptionUnsupported` (R6).

- **The base forwards eight callbacks.** `corenet.LoopLayer`, built by
  `NewLoopLayer(next)` and embedded by a layer, forwards `OnOpen`, `OnData`,
  `OnTimeout`, `OnWritable`, `OnBatchEnd`, `OnClose`, `Honours` and `OnStart`
  to `next`, and `Next` returns it (`corenet.LoopLayered`). A layer overrides
  what it acts on and forwards in turn.
- **Start checks the chain twice.** Every layer must be a `LoopLayered` whose
  `Next` is the handler it was given; then the same middlewares are built a
  second time over a probe, and each of the eight callbacks is sent through
  that probe chain once — one lane, the zero reference, a `LoopIO` that
  refuses everything. A layer that fails either check fails `Start` with
  `LoopMiddlewareInvalid` (`0.2.11.60`), naming the group and the callback
  (`Next` for the first check), before anything is bound.
- **The chain merges `Honours`.** An option is honoured when a layer or the
  handler honours it, walked by `Next`; `Use` stays refused whatever any of
  them claims (ADR 0187 §D2).

A middleware is therefore called twice at `Start`. That is the price of
checking forwarding without wrapping the served handler in a recorder that
every callback would then pay for.

### D3 — The codec computes one canonical target, and refuses what would read twice

Once a head is whole — its Host known — `Parse` sets `HTTP1RequestValue.Path`
and `Query`, both aliasing the target, and every link routes and judges `Path`:

- origin-form: the path up to the first `?`, the query after it;
- absolute-form (`http://` or `https://`, any case): accepted only when its
  authority is non-empty, carries no userinfo and equals the Host field, A–Z
  folded; then reduced to its path (`/` when empty) and query;
- asterisk-form: `*`, for `OPTIONS` only.

It refuses with 400, as three new `Refusal`s mapped to `HTTP1RequestMalformed`:
`RefusalTargetPath` — a `.` or `..` segment, an empty segment but the last, a
backslash, an invalid escape, or an escape of `/`, `.`, `\` or NUL;
`RefusalTargetForm` — any other form, `*` with another method included;
`RefusalTargetHost` — an absolute-form authority that is empty, carries
userinfo, differs from the Host, or comes with no Host. Nothing is decoded,
so nothing allocates; other escapes stay encoded, and since none of them can
spell a separator, a link matching the encoded path cannot be routed around.
Every refusal is one `net/http` also refuses or one the profile adds; the
differential fuzz still holds the codec to a subset of what `net/http`
accepts, with the same bytes consumed.

### D4 — The http1 chain: four links, nothing aliased, nothing framed

A link is a `corenet.Middleware[corenet.HTTP1Handler]`, composed by `Chain`.
`internal/service/net/http1` builds four, each an exported layer type whose
`ServeHTTP1` is held to zero allocations by a kit budget, and each forwarding
`EndBatch` so a chain defers answers as its handler does:

- **`Routes(routes ...RouteValue)`** sets `Route` to the first route whose
  method (empty: any) and `Path` (exact, or prefix) match, zero for none, and
  always calls the next handler: an unrouted request still meets the
  authenticating link, and only the handler answers 404.
- **`Bearer(v HTTP1BearerVerifier, exempt ...int)`** resets `Auth` and
  `Authenticated`, then requires `Authorization: Bearer <token>` on every
  route not in `exempt` — exemptions are route identifiers, never paths — and
  answers 401 with `WWW-Authenticate: Bearer` otherwise, the next handler not
  called. The verifier sets the subject and the claims on the request's own
  `HTTP1AuthValue`: an array of at most `HTTP1MaxSubject` (128) bytes and 64
  bits, so the subject is copied by value before `next` and never aliases the
  token's bytes, which the connection reuses once the batch ends.
- **`Headers(pairs ...string)`** gives every answer the fields `AppendField`
  wrote once, as a slice with no spare capacity, so a handler appending its
  own copies them rather than writes into the link's bytes.
- **`Log(l HTTP1AccessLogger, allow ...string)`** hands `l` one
  `HTTP1AccessValue` by value after the answer: lane, route, method, `Path`,
  status, body length, authenticated, deferred, and the allowed request
  fields, at most `HTTP1MaxAccessFields` (8). The query and the body have no
  slot; `Authorization`, `Proxy-Authorization` and `Cookie` in `allow` are
  refused with `HTTP1Misconfigured`.

No link frames: the adapter advances by what `Parse` returned, so a link that
writes `Consumed`, `HeadLen` or `Body` changes nothing (a test holds it).

## Consequences / Semantics

- `core/net` gains `LoopStarter`, `LoopLayered`, `LoopLayer` and
  `NewLoopLayer`, `OptionLoopMiddleware` (19), `LoopMiddlewareInvalid`
  (`0.2.11.60`), `HTTP1AuthValue`, `HTTP1AccessValue`, `HTTP1BearerVerifier`,
  `HTTP1AccessLogger`, `HTTP1MaxSubject` and `HTTP1MaxAccessFields`;
  `HTTP1RequestValue` gains `Path`, `Query`, `Route`, `Auth` and
  `Authenticated`, which `Parse` resets. `pkg/v1/net/server` re-exports the
  loop half and `UseLoop`; `pkg/v1/net/http1` the chain, with its own
  `Middleware` and `Chain`.
- P2's gate (`tools/netgate`) is active: L2 through `Routes` and `Bearer`
  against the same auth and routing written by hand in one handler, CPU a
  request within 5 % on the bound of benchstat's interval, and the four links'
  zero allocations as kit budgets of `design/sdk.yaml`. The one exclusion is
  an error path: a 401 behind a `Headers` link, whose field is copied after
  the link's own.
- The conformance runs N connections × N tokens through the chain on every
  loop backend present, under `-race`, the portable backend marking each read
  buffer as its batch releases it; the same suite over portable+TLS lands
  with that path.

## Breaking changes

Deliberate, and allowed while v0 (ADR 0040):

1. `HTTP1RequestValue.Loop` is renamed `Lane`; `HTTP1Batcher.EndBatch`'s
   argument is the lane.
2. The `http1` profile refuses targets it accepted: a dot-segment, an empty
   segment, a backslash, an invalid or forbidden escape, `*` for any method
   but `OPTIONS`, a target in no form, and an absolute-form whose authority is
   not the Host or that comes without one.
3. A portable loop group given `Loops: n` runs `n` lanes, where it ran one.

## Alternatives considered

- **Add `OnStart` to `LoopHandler` itself.** Rejected: every handler would
  have to implement a callback most do not need; an optional capability, like
  `LoopOptionHonourer`, costs nothing to the others.
- **Wrap the served handler in a recorder to check forwarding.** Rejected:
  every callback of every connection would pay an indirection to prove a
  property checked once.
- **Decode the target and route on the decoded path.** Rejected: decoding
  needs a buffer per request, and a decoded path is exactly what lets
  `%2f` become a separator; refusing the escapes that change segmentation
  keeps one encoded path every reader agrees on.
- **Keep the subject as a slice of the token.** Rejected: the token's bytes
  are the connection's buffer, reused once the batch ends; a deferred answer,
  or a handler that keeps the request's `Auth`, would read another request's
  bytes.

## References

- [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md) §D3, §D6; [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D2, §Deferred.
- RFC 9110 §4.2.4 (normalization), §11.6.2 (Authorization); RFC 9112 §3.2 (request-target forms); RFC 3986 §5.2.4 (dot-segments); RFC 6750 §2.1, §3.
- Impl: `internal/core/net/` (`loop_layer.go`, `http1.go`, `http1_auth.go`),
  `internal/service/net/http1/` (`target.go`, `chain.go`),
  `internal/service/net/server/` (`loop_chain.go`, `loop_portable.go`),
  `tools/netgate/gates.go`, `design/net.yaml`, `design/sdk.yaml`.
