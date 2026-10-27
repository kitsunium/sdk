# ADR 0194 — a loop lane offloads to its own worker, and an http1 answer may be pending

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, carrying rule R3 of the unified network work (step
  P2, "port sortant boucle→worker"), on the owner's decisions of 2026-10-24.
  Three choices below (Q1–Q3) were taken by the lead maintainer while the owner
  was away, on the recommended options, and are open to the owner's review.
  It can be revisited while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers (lead), owner to confirm Q1–Q3
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D4 (`LoopIO.Post`, `LoopConnRef` and its generation),
  [ADR 0191](0191-a-loop-middleware-forwards-every-callback-and-the-http1-chain-judges-one-canonical-target.md)
  (lanes, the chain walked by `Next`)

## Context

A loop handler runs on its lane's thread and must not block it. Work that
blocks — a database query, a call to another service — had two ways out, both
wrong: block the lane, stalling every connection on it, or start a goroutine
per request and come back with `Post`, which allocates, has no bound and
nothing that stops it with the server. `HTTP1ResponseValue.Defer` holds an
answer to the end of a batch only, which is for work that batches, not for
work that waits.

## Decision

### D1 — The port: one worker per lane

`core/net` declares, in `design/net.yaml`:

- `LoopJobValue` — the connection's full `LoopConnRef`, the sender's slot and
  that slot's own generation, and an integer: no pointer.
- `LoopOutbound.Send(io, job)` — one lane's single-producer single-consumer
  queue, preallocated to `LoopValue.Outbound` jobs (zero: 256). It never
  blocks; a full queue refuses with `LoopOutboundFull` (`0.2.11.63`) and the
  refusal is counted in `LoopStateValue.OutboundRefused`.
- `LoopWorker.Work(lane, job)` — runs on the lane's worker goroutine and may
  block.
- `LoopOffloader` — the optional capability of a loop handler. The server
  walks the chain by `Next` for the first one whose `Worker` is not nil,
  starts one worker goroutine per lane, and calls `OnOutbound` once, after
  `OnStart` and before any other callback. Each job comes back through
  `OnDone` on the lane's thread: the worker pushes it into a second
  preallocated ring and wakes the lane with one `Post`, made only when that
  ring was idle and retried while the post queue is full. `Pending(lane, ref)`
  names the connections still waiting on a worker: a draining loop keeps them
  open and eviction passes them by, on every backend.

The workers start before the engine, and stop after it: once `Shutdown`'s
drain is over — answers still out reach their connections — or at once on
`Close`; jobs still queued then are dropped, since no loop is left to write
their answers.

### D2 — The http1 adapter: a pending answer

`HTTP1ResponseValue` gains `Pending` and `Job`, and `corenet.HTTP1Worker`
(`WorkHTTP1(lane, job, res)`) sits beside `HTTP1Handler`. A handler that is an
`HTTP1Worker` sets `Pending` and `Job` — an index of what it copied out of the
request, whose bytes do not outlive the batch — and the adapter sends a job
whose slot holds a response value of its own; the worker fills it; the
adapter writes it from the lane's thread, through `LoopIO.Write`, so a
connection that has closed is a `LoopStaleRef` and nothing more.

- A connection that closes first cancels its slot: a worker that has not
  started skips `WorkHTTP1`, and the slot is freed when the job comes back.
- `Pending` with `Defer`, or from a handler that is no `HTTP1Worker`, is a 500
  that closes the connection.

### D3 — Order on a connection (Q2, lead's decision)

HTTP/1.1 answers in request order. From a connection's first pending answer
until it is written, every later answer of that connection — answered on the
loop, deferred to the batch's end, or pending — is held, in order, in a
buffer of the connection's own, and written at a batch's end once every
answer before it is ready. What a connection holds is bounded by
`LoopValue.OutputCap`: past it the connection is closed. The alternative,
stop reading the connection until its answer is back, needed a new
"resume reading" primitive on every backend.

### D4 — A full queue is a 503, the connection kept (Q1, lead's decision)

A pending answer the lane's queue refuses is answered at once, in its place,
`503 Service Unavailable` with `Retry-After: 1`, and the connection stays
open: the client may retry on it. Each one is counted in
`LoopStateValue.OutboundRefused`.

### D5 — No new option (Q3, lead's decision)

The queue's depth is `LoopValue.Outbound`, beside the loop's other bounds; no
`OptionKind` is added, since the group's option set does not change.

## Consequences

- New: `LoopJobValue`, `LoopOutboundValue`, `LoopOutbound`, `LoopWorker`,
  `LoopOffloader`, `HTTP1Worker`, `LoopOutboundFull` (`0.2.11.63`),
  `LoopValue.Outbound`, `LoopStateValue.OutboundRefused`,
  `HTTP1ResponseValue.Pending` and `Job`; re-exported by `pkg/v1/net/server`
  and, for `HTTP1Worker` as `Worker`, `pkg/v1/net/http1`.
- The worker's completion reaches the lane through a process-wide table of
  lanes indexed by the integer it posts with, since a `PostFunc` holds no
  closure; the table only grows, by one entry per lane per started group.
- A handler that never sets `Pending` pays one flag lookup per request; the
  answers' path allocates nothing new.
- Tests, on every loop backend present, under `-race`: a pending answer and
  pipelined answers of every kind behind it in order; a full queue's 503 with
  `Retry-After`, the connection kept and the refusal counted; connections
  that close first; the holdback ceiling; Pending without a worker; and a
  drain that delivers an answer still out.

## Alternatives considered

- **A goroutine per pending request.** Rejected: unbounded, allocating, and
  nothing stops it with the server.
- **A pool of workers shared by the lanes.** Rejected for now: one worker per
  lane keeps both queues single-producer single-consumer and lock-free, and
  keeps a lane's jobs in its order; a pool is a measured change later.
