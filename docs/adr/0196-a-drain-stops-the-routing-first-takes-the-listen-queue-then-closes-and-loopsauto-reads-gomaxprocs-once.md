# ADR 0196 — a drain stops the routing first, takes the listen queue, then closes, and LoopsAuto reads GOMAXPROCS once

- **Status**: Accepted
- **Date**: 2026-10-27
- **Accepted**: 2026-10-27, carrying step P6 of the unified network plan on
  the owner's decisions of 2026-10-24 (R8: `Loops` of zero is one, `LoopsAuto`
  a sentinel) and the plan's drain sequence. It can be revisited while the SDK
  is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amended 2026-11-10** (#856): a loop group's `Shards(0)` binds one SO_REUSEPORT listener per loop; each is taken by its one loop's rings (§D3's taker is the loop that owns the shard), and a loop whose watchdog gave up closes its own shards. A partial drain of a reuseport group stays `tcp_migrate_req=1`'s (§D3).
- **Amended 2026-11-14** (#1385), in two sections at the end: the drain looks at the established connections once `OnBatchEnd` has returned, a closing connection reads nothing more, its output is settled whichever path it left by, and the drain's close lingers — the writing side shut after the output, the descriptor closed at the peer's end or after 500 ms —; and io_uring's take arms a round of up to 1 024 accepts and reads the queue again at once, its take over at the first empty read. §D3 is unchanged.
- **Amends**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  §D1 (the drain a loop group shares with the server) and §D5 (the loop
  count); [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D9
  (`LoopsAuto` lands)
- **Related**: [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md)
  (readiness built from `State`), [ADR 0190](0190-a-merge-gate-judges-the-ratio-of-two-engines-measured-side-by-side.md)
  (the paired measure P6's gate reads)

## Context

Two things stood between the loop engine and a program sized and stopped like
any other.

**The loop count.** `LoopValue.Loops` of zero is one loop (ADR 0187 §D9), so a
program that wants one loop per CPU reads `GOMAXPROCS` itself and passes it.
Since Go 1.25 `GOMAXPROCS` follows the container's CPU limit and can change
while the process runs; two reads — one for the chain's `OnStart`, one for the
backend — could disagree, and a lane index past what `OnStart` sized would
index out of range.

**The drain.** A loop group's drain took its listeners out of the epoll set,
or cancelled its io_uring accepts, and closed them only once the last
connection had closed. Two resets followed, both seen by a client that had
done nothing wrong:

- every connection the kernel completed after the listeners left the set sat
  in the listen queue until the very end, and the close reset it;
- a connection accepted as the drain began — the batch that carried its
  accept also carried the drain request — was closed at once, before its
  request was read; a close with unread bytes, or one the request meets, is a
  reset.

And the server closed every listener the instant `Shutdown` began, the same
instant readiness turned false, so whoever routes on readiness — a load
balancer, an orchestrator's endpoints — kept sending for as long as it took to
notice, into a closed port.

## Decision

### D1 — `LoopsAuto` is GOMAXPROCS, read once at Start

`corenet.LoopsAuto` (`-1`, re-exported by `pkg/v1/net/server`) as
`LoopValue.Loops` runs one loop per `GOMAXPROCS`. Server reads it once, in
`bindLoopGroup`, before the chain's `OnStart`, and every backend, the chain
and `State` see that snapshot; a later change of `GOMAXPROCS` moves nothing.
It is a sentinel, never the zero value, which stays one loop (R8).
`LoopStateValue.LoopsSource` says where the count came from — `explicit`,
`auto`, `single` — beside `GOMAXPROCS`, and the start line carries it. The SDK
still never sets `GOMAXPROCS`.

### D2 — Readiness first, then a grace, then the listeners

`Shutdown` turns readiness false (the phase is `draining`), then keeps
accepting and serving for `WithDrainGrace(d)` — zero by default, which closes
at once as before —, then closes the listeners and drains the connections.
The grace is the time the routing takes to notice: an orchestrator's endpoint
removal, a load balancer's health interval. `Serve` adds it to the drain
budget, so a grace never eats the drain; a caller of `Shutdown` gives it
within its own ctx, and a ctx that ends first ends the grace.

### D3 — A loop group takes its listen queue before it closes the listener

The drain of a loop group, per backend:

1. **epoll**: each loop takes the listeners out of its set (`EPOLL_CTL_DEL`),
   then, at each batch of the drain, accepts on each until `EAGAIN` — 1 024 a
   batch, so the loop serves between two takes — and serves what it accepted.
   Out of descriptors (`EMFILE`, `ENFILE`) or with its table full, it waits
   for the drain's closes to free some instead of handing the queue to the
   close. The errors accept(2) says to retry on (`EPROTO`, `ENETDOWN`,
   `ENOPROTOOPT`, `EHOSTDOWN`, `ENONET`, `EHOSTUNREACH`, `EOPNOTSUPP`,
   `ENETUNREACH`), and `ECONNABORTED`, skip one connection, on every backend.
2. **io_uring**: each ring cancels its single-shot accepts. An accept that
   completed as its cancel crossed it is served, not closed. Once every cancel
   came back, the ring arms as many accepts as the listener's queue holds —
   `TCP_INFO`'s `tcpi_unacked` on a TCP listener; on one `TCP_INFO` does not
   answer, a Unix socket, `acceptDepth` while a zero-timeout `ppoll` says it is
   readable — within the room above its reserve, until the queue reads empty
   with none in flight; an accept left waiting because another ring took the
   connection is cancelled again. A queue with no room above the reserve, or
   with process descriptors out until the backoff ends, waits for the drain's
   closes; it is never read as empty. A ring the watchdog drained before the
   group takes nothing. An accept's failure stops the ring's take only when it
   says the listener failed: its own cancel, `ECONNABORTED`, `EINTR`, `EAGAIN`
   (should a kernel honour the listener's `O_NONBLOCK`), `ENFILE` and
   `EMFILE` only wait.
   
   Both backends take at most 65 536 connections from one listener: past it
   the arrivals outpace the drain — no grace let the routing stop —, and what
   is left in the queue meets the close.
3. Each taker — a loop on epoll, a ring on io_uring — hands each listener back
   once it is done with it; the last one closes it (`listenerSet`). The
   listener therefore closes before the established connections drain, never
   while another taker may still accept on it, and what no taker closed — a
   group stopped rather than drained — closes once every loop returned.
4. The established connections drain as before, one rule added: a connection
   that has sent nothing yet closes only once it has, or once it stayed silent
   for a second (`drainQuiet`). A connection accepted by the take has its
   request on the way; closing it unread would reset it.

The loop does not stop until its connections are gone **and** its listeners
handed back.

The portable backend does the same on every Unix platform. At Start it
duplicates each listener server bound (`File`, then `FileListener`; both
duplicates share the original's open file description, so its `O_NONBLOCK`,
which `FileListener` sets, is the original's too, as it already was) so that
server's close after the grace leaves the socket listening; its acceptors
keep admitting while it drains; its `Shutdown` accepts on each listener's own
non-blocking descriptor until `EAGAIN` — `accept4(SOCK_CLOEXEC)` on Linux,
`accept` then `FD_CLOEXEC` under `syscall.ForkLock` elsewhere, under
`RawConn.Control`, each descriptor wrapped by `FileConn` —, then closes it,
within its ctx. Out of descriptors — the accept, or the duplicate
`FileConn` needs, the accepted descriptor then held open — it tries again
each tick while a lane still has a connection to close. A lane stops only
once no acceptor runs, read before its count of connections; a connection
handed to a lane that stopped is closed. `MaxConns` refuses during the
drain as it does on the native backends. Its connections follow rule 4 too
(`portableQuiet`, a second). On Windows a listener's descriptor cannot be
duplicated into a `net.Listener`: there the portable backend's listeners stay
server's, close after the grace, and no queue is taken.

### D4 — A partial drain is the kernel's `tcp_migrate_req`

Between a taker's last empty read of the queue and its close, a connection can
still complete its handshake, and the close resets it; so can one whose final
ACK the kernel has not processed yet. On a single listener no sequence of
calls closes that window: the grace (D2) is what empties the queue before it.
When the drain is partial — one process of several in an `SO_REUSEPORT` group,
as in a rolling restart — Linux 5.14 and later migrate the closing listener's
queued and half-open connections to another listener of the group when
`net.ipv4.tcp_migrate_req=1`. The SDK never sets a sysctl (the plan puts
sysctls out of scope); the documentation names this one for whoever runs
several processes on one port.

Two more refusals reach a peer during the drain, on every backend, and both
are the ones it meets at any time: `MaxConns` reached — a connection the take
accepts past it is closed, as one accepted while serving is — and a take
that gives up, out of descriptors with no connection left to close, or past
its budget. On the portable backend, `FileListener` calls `Fd()` on the
duplicate `File` returned, which makes the shared open file description
blocking for the microseconds before `FileListener`'s own duplicate sets
`O_NONBLOCK` again: no acceptor of this process runs on it then, but a
socket adopted from a supervisor that another process also accepts on could
see one blocking accept in that window.

### D5 — P6's gate is active

The `P6` row of `tools/netgate` — `cpu-ns/req` of the loop engine over the
goroutine engine on L1 at most 0.70 on the bound of the interval — is active
from this change. L1's loop engine runs `LoopsAuto`, so the gate judges the
engine as a program sizes it, one loop per P, against the goroutine engine,
which has every P. L2 keeps one loop: both of P2's engines are loop engines,
and their ratio is what P2 judges.

## Consequences / Semantics

- `core/net` gains `LoopsAuto` and `LoopStateValue.LoopsSource`; `net/loop`'s
  `Config` gains `LoopsSource`; `net/server` gains `WithDrainGrace`;
  `pkg/v1/net/server` re-exports `LoopsAuto` and `WithDrainGrace`.
- A program that sets no grace and no `LoopsAuto` sees one difference: a loop
  group's drain serves what its listen queue held and closes the listener
  early, instead of resetting both.
- The drain's take arms io_uring accepts after the cancels came back, a batch
  at a time: a drain costs a few ticks more than before when the queue is
  long.
- Tests: `TestConformance_drainTakesQueue` (both native backends, on a TCP
  and on a Unix listener: the loop held at its batch end, 384 connections
  queued with their request, then the drain — every one reads its echo and a
  clean end, the listener refuses after; 1 280 queued on one loop, past
  epoll's 1 024 a batch), `TestConformance_drainOutOfDescriptors` (a child
  under `RLIMIT_NOFILE` 64, 150 connections dialled: the take waits for the
  drain's closes, every one served, `Shutdown` nil), `TestDrain_arrivalsAcrossTheDrain`
  (every backend: arrivals that never stop until a dial is refused; every
  connection dialled earlier than 20 ms before the first refusal reads its
  echo — the close's own window, D4, is the only reset allowed, and it is
  logged; on Linux the 45 runs measured showed one), `TestDrain_graceUnderArrivals` (every backend: clients dial back to
  back and stop 150 ms after readiness turns false, under a 400 ms grace —
  no dial fails, no exchange is cut short, connections dialled after readiness
  turned false are served, kept connections end cleanly), and
  `TestResolve_loops`.

## Alternatives rejected

- **Closing the listener with a filter that drops SYNs first.** A socket
  filter returning zero on the listener drops new SYNs and the final ACK of a
  handshake in progress alike: the client of the latter believes itself
  connected and is reset at the close all the same. It narrows nothing the
  grace does not.
- **`shutdown(2)` on the listener.** On Linux it disconnects the listener,
  which resets its queue exactly as the close does.
- **Reading `GOMAXPROCS` in each backend.** Two reads can disagree since Go
  1.25 follows the CPU limit; a lane count fixed once is what `OnStart`
  promises.
- **A default grace.** The right value is the routing's, not the SDK's; a
  non-zero default would slow every test and every shutdown of a program that
  routes on nothing.

## Amendment 2026-11-14 (#1385): the drain looks once the batch's answers are written

§D3's rule 4 closes a connection with no partial input once it is heard or
quiet. Where it looked was left unsaid, and every backend looked before
`OnBatchEnd`. The HTTP adapters answer a batch's requests at `OnBatchEnd`: a
connection whose request came in the batch was marked closing first and its
answer refused, so it ended unanswered well inside the drain — on every
backend, h2c on every run measured, HTTP/1.1 spared only because its
`OnDrain` deferred the close a batch.

- **The look comes after `OnBatchEnd`**, before the batch's flush
  (`settleDrain` on io_uring, epoll and kqueue, the portable backend's
  `closeSettled` after `OnBatchEnd`); the drain's queue take stays where it
  was. Every answer the batch's requests asked for is written before a close
  is marked, and a closing connection finishes only once its output has left.
  A connection fed in every batch is judged at every look: it hears
  `OnDrain` at one and closes at the next.
- **A closing connection reads nothing more**, on every backend: an answer to
  bytes read after the close was decided could not leave. The native
  backends already dropped them; the portable backend now does.
- **The drain's close lingers.** Once a closing connection's output has left
  for the kernel, the drain shuts its writing side — the FIN follows the
  output — and keeps reading, and dropping, what the peer still sends,
  until the peer ends or `drainLinger` (500 ms, the portable backend's
  `portableLinger`) passed; only then is the descriptor closed. A close with
  input unread sends a reset, and the reset discards whatever of the output
  the kernel has not delivered yet: a peer that keeps sending and reads
  slower than the answers come lost them, on every backend
  (`TestDrain_aConnectionFedThroughTheDrainGetsEveryAnswer/raw_handler,_a_slow_reader`),
  and OpenBSD's kqueue lost the last answer of a connection fed through the
  drain even at full speed. On io_uring the shut of the writing side is the
  connection's one shutdown (ADR 0200): after the linger, the recv still
  armed is cancelled by its user data and the direct descriptor closed.
  A connection whose peer already ended closes at once.
- **A closing connection whose output left through the writable path is
  settled.** On epoll and kqueue a write the socket refused for room resumes
  on the writable event; once it took the last of a closing connection's
  output, nothing looked at the slot again, so its close — the linger
  first — never began, and the drain waited for its deadline (FreeBSD and
  NetBSD, a slow reader on their small send buffers; on Linux with
  `SendBuffer(8 KiB)`). The slot is now settled at the batch's end, as
  io_uring's send completion already did.
- **A first version deferred the look instead** for a connection the batch
  fed. Unbounded, a connection fed in every batch never closed; bounded to
  one batch, it was closed before its answer was written —
  `TestDrain_aConnectionFedThroughTheDrainGetsEveryAnswer` lost answers over
  three runs on HTTP/1.1 on every backend, on a raw handler answering at
  `OnBatchEnd` on epoll and the portable backend, on h2c on epoll.
- The HTTP/2 adapter's GOAWAY, run in a batch that fed a request, leaves the
  connection to the batch's end, which writes its answers and then closes it.

## Amendment 2026-11-14 (#1385): io_uring's take reads the queue once its accepts ran

§D3's rule 2 arms "as many accepts as the listener's queue holds". The code
held a round to `acceptDepth` (32) and read the queue again only a batch
later, once the round's completions had come back. Under arrivals that do not
stop — a client that keeps dialling, as `TestDrain_arrivalsAcrossTheDrain`'s
probe does — the queue was never empty at that read once a batch outlasted the
gap between two dials, and 32 a batch fell behind a single dialer. On the
`bazel` job (`h2c/auto`, three loops of one ring, the race detector, a loaded
runner) the take ran some six seconds into its 65 536 budget with the listen
queue at its backlog, and the close reset everything queued: two connections
whose dial had returned 475 ms before it. Two of the three rings had read the
queue empty at the drain's start and handed the listener back, so one ring
carried the take. Reproduced on Linux 6.12 with three loops of one ring and
the drain's batches a millisecond longer: under eight dialers the last ring
handed the listener back at `taken=65536` with 4 069 connections queued, and
the close reset connections dialled up to 490 ms before it, seven seconds
into the drain; under one, the take did not end within `Shutdown`'s ten
seconds, and the stop reset connections dialled 100 to 190 ms before it.
epoll, whose take reads `EAGAIN` right after its accepts, ended at once.

- A round arms up to `queueTakeBatch` (1 024, epoll's and kqueue's bound a
  batch) for what `TCP_INFO` counts, within the room above the reserve and
  the budget. A Unix listener, whose poll says only that one waits, keeps
  `acceptDepth` a round.
- The round is submitted at once and the queue read again: a single-shot
  accept on a queue that holds a connection completes as it is submitted, so
  an empty read there is the take's `EAGAIN`, as on epoll and kqueue. The
  ring's take of that listener is then over: its accepts still waiting —
  another ring took their connection — are cancelled, and the listener is
  handed back once they came back. A kernel that completes the accepts later
  leaves the queue non-empty at that read, and the take goes on at the next
  batch as before.
- What arrives between that read and the close meets the close: D4's window,
  on io_uring now the rest of the batch and the next one's reap, where under
  arrivals it was the whole budget.
- The budget stays: past it the arrivals outpace a take that empties the
  queue at every batch.
- The issue's hypothesis — a loop's own `SO_REUSEPORT` listener left
  untaken — does not apply to the run: without `Shards(0)` the loops share one
  listener and one queue, and the connections reset were in it.
- Test: `TestConformance_drainTakesWhatTheQueueHolds` — three loops of one
  ring share one TCP listener, each held at every batch end; the drain is
  asked for, 1 200 connections complete their handshake into the queue
  (96 on darwin and the BSDs), and every loop runs four batches: every one
  must be open by then, then served and ended cleanly. On the code before,
  io_uring had opened 385 of 1 201; epoll and kqueue are unchanged.

## Amendment 2026-11-15 (#1435, #1436, #1437): the drain's close keeps its answers on every backend

Three paths the amendment above left out, each found on #1430 and each red
on `main` before this change (the tests name them):

- **A lingering close keeps reading whichever way its recv ended** (#1437).
  On io_uring a recv that ran out of buffers is queued and re-armed, and a
  cancelled one re-armed, for a lingering slot as for an open one (`reads`);
  taken for closed, the slot stopped reading and closed at the bound with
  input unread — the reset that loses answers the linger exists to keep.
  Past the starved queue's bound a lingering slot stops reading and
  `drainLinger` ends it, the bound the linger already has.
  `TestURing_lingerReadsOnAfterStarveOrCancel`.
- **The peer's end waits for the output** (#1436). On the portable backend,
  the end of a closing connection's input goes through the settlement that
  sends its queued output, then closes; it finished the connection at once,
  the rest of an answer dropped. Only a lingering close, its writing side
  already shut, ends at the peer's end.
  `TestPortableEnd_aClosingConnectionsOutputLeavesFirst`.
- **An HTTP/2 answer a worker returns in a batch that fed its connection
  joins the batch's output** (#1435), behind the SETTINGS and the drain's
  GOAWAY the batch queued, and the batch's end writes it and closes the
  connection. Written and closed at once, it went out first — ahead of the
  SETTINGS RFC 9113 §3.4 makes the first frame, drain or no drain — and its
  close refused the rest at the batch's `flush`.
  `TestLoopHandler_aWorkersAnswerInTheDrainingBatchFollowsItsOutput`.
