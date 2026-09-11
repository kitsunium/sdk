<!-- updated: 2026-09-09T00:00:00Z -->
# internal/service/net/server/

## Purpose

The inbound engine of the network domain (ADR 0029): one unified listener for
TCP, Unix, TLS and mutual TLS, serving handlers grouped behind shared
middlewares, with an explicit lifecycle and a bounded drain.

Public façade: `pkg/v1/server`.

## Contents

| File | Surface |
|---|---|
| `server.go` | `Server`, `New`, `Group`, `State` |
| `lifecycle.go` | `Start`, `Serve`, `Shutdown`, `Close`, the accept loop, the drain |
| `stream_group.go` | `StreamGroup` — `Handle`, `HandleFunc`, `Use` |
| `listen.go` | listener construction; TLS/mTLS wrapping; family validation |
| `conn.go` | the pooled `corenet.Conn` implementation, and the `hijacked` hand-over flag |
| `pool.go` | per-connection recycling + the live-socket registry |
| `options.go` | `Option` / `GroupOption` |
| `packet_group.go` | `PacketGroup` — the datagram mirror of `StreamGroup` |
| `packet.go` | the pooled `corenet.Packet` implementation |
| `packet_listen.go` | datagram socket construction; family validation; ceilings |
| `packet_loop.go` | the datagram read loop and dispatch |
| `batch.go` | the `datagramSource` seam + reusable read slots |
| `batch_portable.go` | one datagram per syscall — the floor everywhere |
| `multireader_linux.go` | `recvmmsg` batched reader |
| `multireader_mmsghdr_linux.go` | the cited `struct mmsghdr` layout |
| `sockaddr_linux.go` | kernel sockaddr decoding for the batched path |
| `http_adapter.go` | the `net/http` adapter (ADR 0029 D3) |
| `conn_waiter.go` | `connWaiter` — the completion channel and the hijack flag one `ServeConn` blocks on |
| `http_listener.go` | the channel-fed bridge listener |
| `reuseport_{linux,bsd,other}.go` | the cited `SO_REUSEPORT` constant per family |
| `stream_group_limiter.go` | the per-group connection ceiling — a reject-mode semaphore whose slot a hijacked connection keeps until it closes |
| `conn_tracked.go` | `trackedConn` — the socket that reports its own `Close`, so a hijacked connection's slot comes back when it ends |
| `listen_tracked.go` | `trackedListener` — hands out `trackedConn`s beneath TLS, for a group that serves HTTP under a ceiling |
| `adopt.go` | adoption of listeners inherited from a supervisor |

## Why-this-shape

- **Goroutine per connection on the runtime netpoller** (ADR 0029 D2). Not an
  event loop: TLS is a hard requirement and `crypto/tls` is written against
  blocking `net.Conn`, the netpoller already *is* an epoll, and every io helper
  in the ecosystem speaks `net.Conn`.
- **`conn` embeds `stdnet.Conn`**, so reads and writes reach the socket with no
  wrapper on the hot path, and `io.Copy(c, c)` is a working echo server.
- **`Group` returns the group, not `(group, error)`.** A declaration mistake is
  recorded in `declErr` and surfaced by `Start`. This is the trade that keeps
  wiring a server to five statements; it is only acceptable because `Start`
  reports every recorded error, and `TestDeclarationErrorsSurfaceAtStart` pins
  that it does.
- **TLS is applied at the listener, not in the accept loop.** `tls.NewListener`
  defers the handshake to the connection's own goroutine, so a slow or hostile
  peer stalls only itself. Doing the handshake inline in `Accept` would let one
  peer block every pending connection.
- **`Start` returns only once every listener is bound.** A caller with a nil
  error knows the ports are open — which is what lets a test dial immediately
  and a supervisor report readiness honestly.
- **The accept loop exits on `net.ErrClosed` and continues on anything else.**
  Closing the listener is the only signal that reliably interrupts a blocking
  `Accept`; a transient accept error must not stop the server.
- **The drain has teeth.** `waitIdle` polls the active count against the
  caller's budget. When the budget expires the engine severs the live sockets
  through the `live` registry and returns `DRAIN_TIMEOUT` **without** waiting on
  the WaitGroup. The first implementation did wait, which made the budget
  meaningless: a handler blocked on something other than its socket hung
  `Shutdown` forever. `TestShutdownReportsAnExpiredBudget` is the regression
  guard, and it went from a 120-second hang to 1.1 seconds when fixed.
- **A handler panic closes only its connection.** One malformed peer must never
  take the process down.
- **`release` always runs, via defer.** A handler that returns early, errors, or
  panics still cannot leak a descriptor or a pooled wrapper.
- **`reset` drops every reference.** A pooled entry that kept its `net.Conn`
  would pin a closed socket for as long as the pool lives.

## The datagram path

- **One `datagramSource` seam, two implementations.** The read loop is written
  once against "give me up to N datagrams"; the platform decides how many
  syscalls that costs. On Linux it is one `recvmmsg`; elsewhere it is a
  `ReadFrom` per datagram. The loop above does not change either way.
- **`golang.org/x/net/ipv4.PacketConn.ReadBatch` is unavailable to us**: it
  transitively imports `golang.org/x/sys`, banned SDK-wide (ADR 0016/0018). The
  ABI is therefore declared in-tree and cited, the same discipline `proc`
  already uses. `struct mmsghdr` lives in `multireader_mmsghdr_linux.go` with
  its header quoted; `syscall.Msghdr`, `Iovec`, `SYS_RECVMMSG` and
  `MSG_WAITFORONE` are all in the stdlib.
- **The syscall is driven inside `RawConn.Read`.** Returning false on `EAGAIN`
  parks the goroutine on the **netpoller**, exactly as a blocking `ReadFrom`
  would. Batching does not cost us runtime integration.
- **The read slots are allocated once per socket** and wired into the kernel's
  message array in lockstep, so a steady-state read allocates nothing. This is
  why the slice-preallocation rules are excluded for this package: the arrays
  are indexed, never appended to, and rebuilding them would break the lockstep.
- **`newDatagramSource` falls back rather than fails** when a `PacketConn`
  exposes no raw descriptor — a wrapper or a test double. Correctness is
  identical; only the syscall count differs.
- **The fallback is reported, never silent.** `batchDegradation` returns both a
  flag and a reason, and they reach `State().Listeners[i]`. A degradation nobody
  notices is the failure mode the field exists to prevent.
- **`TestLinuxSelectsTheBatchedReader` is the only test that proves the batched
  path is in use.** Every behavioural datagram test passes identically on the
  portable fallback, so without it a broken type assertion would degrade the
  engine to one syscall per datagram with the whole suite still green.
- **Handlers run inline on the read goroutine.** A hand-off would cost a channel
  send and an allocation per packet, undoing the batching this path exists for.
  A handler that must block should copy the payload and hand it to its own
  worker — which is why `Packet.Data` documents its lifetime.

## The HTTP adapter

`net/http` can only be driven through `Serve(net.Listener)`; there is no public
per-connection entry point. The adapter therefore hands `http.Server` a bridge
listener whose `Accept` pops from a channel our accept loop fills. The cost is
one hand-off per **connection**, not per request —
`TestHTTPAdapterKeepsAliveAcrossRequests` pins that three keep-alive requests
count as one accept.

Two things about it were got wrong first and are worth keeping wrong-proof:

- **The connection handed to `net/http` is the raw socket, never our pooled
  wrapper.** Wrapping it was the obvious design and it fails twice over. It puts
  the pooled wrapper in two goroutines at once — the race detector caught the
  engine resetting it while `net/http` was closing it — and it hides the
  concrete `*tls.Conn` that `net/http` type-asserts on to populate
  `Request.TLS`, so every request on an HTTPS listener arrived looking like
  plaintext. `TestHTTPAdapterOverTLS` asserts `r.TLS != nil` for exactly that
  reason.
- **Completion is tracked through `http.Server.ConnState`**, not by wrapping
  `Close`. Only `StateClosed` and `StateHijacked` are terminal; acting on
  `StateIdle` would release the connection between two keep-alive requests.
- **`conn.Close` is nil-safe** because `net/http` can close a connection after
  the engine has reclaimed the wrapper — on a drain, where the adapter releases
  its waiter without waiting for `net/http` to finish.
- **A HIJACKED connection is no longer the engine's to close** (ADR 0047 §D9).
  This was a defect that predated WebSocket and was reachable with nothing but
  `net/http`: the adapter released its waiter on `StateHijacked` — correctly,
  since `net/http` is finished with the connection at that point — and `release`
  then ran its deferred `Close`, severing a socket the handler had just been
  handed. The flag rides on the `connWaiter` and is transferred to the pooled
  wrapper by `markHijacked`; `release` skips the close when it is set. It is
  read on **both** arms of the `select` in `ServeConn`, not only under `<-done`:
  a hijack landing while the drain cancels the context leaves both cases ready,
  and `select` chooses between ready cases at random. Two consequences are
  deliberate and match `net/http`'s own carve-out ("Shutdown does not attempt to
  close nor wait for hijacked connections such as WebSockets"): such a
  connection is **not waited for** by the drain, and it stops counting toward
  `State().Active`. The drain SIGNAL is what reaches it instead.
  `TestAHijackedConnectionSurvivesTheEngine` provokes the defect with a plain
  `http.Handler` and is mutation-checked — forcing the close back on fails it
  and nothing else.
- **A hijacked connection still holds its `MaxConns` slot until it closes** —
  the consequence ADR 0047 §D9 did not list. The slot used to be held only
  while `ServeConn` ran, and `ServeConn` returns at the hijack, so every
  upgraded WebSocket gave its slot back while it stayed open and `MaxConns`
  bounded nothing an upgrade reached. Now the slot is handed to the socket, and
  the socket's `Close` returns it; see "Connection ceiling" below. Neither
  consequence above changed — the drain still does not wait on the connection,
  the engine still never closes it — which
  `TestAHijackedConnectionStillCountsAgainstTheCeiling` pins in the same run.
- **The serving goroutine is a `kernel/worker.LoopDaemon`**, so its owner and
  termination are explicit and `shutdown` has a `Done()` channel to bound its
  wait on. `http.Server` is interrupted by closing its listener, not by a stop
  signal, which is why the loop ignores the stop channel.
- **`Shutdown` stops the embedded `http.Server` before draining.** Without it an
  idle keep-alive connection holds `ServeConn` open for the whole drain budget;
  `TestHTTPAdapterDrainsOnShutdown` fails if the drain takes more than 3s.
- **The adapter publishes a drain signal, because stopping `http.Server` is not
  enough for a request that never ends.** `http.Server.Shutdown` waits for
  in-flight requests, and an event stream or a long poll never becomes idle, so
  it held the drain for the caller's ENTIRE budget and was then killed by having
  its socket severed — `DRAIN_TIMEOUT` on every deploy, for every connected
  client, with no chance for the handler to stop cleanly. Measured before the
  fix: a five-second budget cost five seconds and returned an error; after: 40 ms
  and a clean drain. `stop()` closes `a.draining` FIRST — before the bridge
  closes and before `http.Server.Shutdown` starts waiting — and `BaseContext`
  puts that channel on every request context, where `corenet.DrainSignal` reads
  it. The open-stream cases of `TestHTTPAdapterDrainsOnShutdown` are the
  regression guard, and they have been mutation-checked: suppressing the close
  restores the budget-length `DRAIN_TIMEOUT`.
- **The request context is deliberately NOT cancelled to say it.** Cancelling it
  would tell every handler to abandon the response it is halfway through, which
  is precisely what the drain exists to let them finish. The signal is a context
  VALUE, so it is additive: a handler that ignores it behaves exactly as before.

## Sharded accept

`Shards(n)` opens n listeners on one address with `SO_REUSEPORT`, each with its
own accept loop, so the kernel load-balances connections instead of N loops
contending on one accept queue. Zero means one per core; one disables it.

- **The constant is hand-defined per family and cited**, because it is absent
  from the stdlib `syscall` package and `golang.org/x/sys` is banned SDK-wide.
  Linux says 15, the BSDs say 0x200 — different values, which is why they are
  declared separately rather than once.
- **It must be set before `bind`**, which is what `net.ListenConfig.Control`
  exists for. Setting it afterwards silently does nothing.
- **Windows returns false rather than substituting `SO_REUSEADDR`**, whose
  semantics are not equivalent: it permits *hijacking* a bound port rather than
  load-balancing across sockets.
- **A Unix socket is not shardable and says so** — but only when sharding was
  explicitly asked for. Auto-sizing on a Unix socket disappoints no expectation,
  so it is not reported as a degradation.
- **Only the first shard appears in `State`**, carrying the real shard count. N
  rows for one address would read as N addresses.
- **On this machine it buys nothing.** `BENCH.md` §4 reports the null result and
  the two tests that prove it is a null result rather than a broken comparison.

## Connection ceiling

`MaxConns(n)` is a reject-mode channel semaphore (`connLimiter`): a free slot
admits, a full one refuses at once, nothing queues. It used to be
`resilience.NewBulkhead`, which has exactly those semantics — but a bulkhead is
a `Runner`, and a `Runner` holds its slot for exactly one call. A hijacked
connection outlives the call that admitted it, so the ceiling holds its slot
explicitly: `tryAcquire` on admission, and `releaseFor` when the handler
returns — which returns the slot, or hands it to the socket when a handler took
the socket over. Explicit acquire/release on a channel is the shape `hedge`'s
in-flight cap already uses in the resilience domain.

- **The budget is per group, not per socket.** A group on a TCP port and a Unix
  socket, or sharded across listeners, shares one ceiling; that is what an
  operator sizing a server means.
- **A rejection is `ConnLimitReached`**, carrying the ceiling it hit, and is
  counted in `RejectedConns` — a `net` consumer never meets a resilience
  sentinel.
- **No ceiling means no closure on the hot path.** `admit` calls the handler
  directly when `limiter == nil`, so the policy costs nothing when unused; with a
  ceiling the settlement is one deferred call, so a handler that panics still
  gives its slot back.
- **A slot is released when the handler returns**, which is *after* the client
  has seen its response and closed. A client reconnecting immediately against a
  ceiling of one can therefore meet a still-occupied server, and that rejection
  is correct. `TestMaxConns` ("a ceiling that releases its slots") uses a
  ceiling of four for exactly this reason — an earlier version asserted zero
  rejections at a ceiling of one and failed about one run in three.
- **A HIJACKED connection's slot is released when its socket closes**, not when
  its handler returns. `ServeConn` returns at the hijack, and from then on the
  engine neither closes the socket nor waits for it (ADR 0047 §D9), so the only
  event left that says the connection is over is somebody closing it. Hearing it
  takes a `trackedConn` — an accepted socket that reports its own `Close` —
  handed out by a `trackedListener` for every group that serves HTTP **under a
  ceiling** (`StreamGroup.tracksCloses`); no other group can hijack or has a slot
  to hold, and their sockets are not wrapped. The tracker sits **beneath TLS**,
  so `net/http` still receives the concrete `*tls.Conn` and `Request.TLS` is
  populated; on a plaintext listener `net/http` receives the tracker itself,
  which is why it forwards `CloseWrite` (the graceful half-close) and `ReadFrom`
  (sendfile/splice) rather than hiding them. `Close` closes the socket FIRST and
  returns the slot second, so the ceiling never admits a new connection while
  the old one is still open; a socket closed before the engine hands the slot
  over returns it at the hand-over; a second `Close` returns nothing.
  `Test_Server_admit_HijackedConnectionKeepsItsSlot` pins the whole life of the
  slot over plaintext AND TLS, and is mutation-checked four ways.
- **What that costs and what it changes, stated rather than discovered.** A
  ceilinged HTTP group allocates one `trackedConn` per connection (it cannot be
  pooled: a hijacked one outlives the engine's hold on it); `BENCH.md` measures
  no ceilinged group, so its numbers are unchanged. A hijacking handler on a
  ceilinged plaintext group is handed the tracker, never the `*net.TCPConn`
  beneath it. A group at its ceiling can report fewer `ActiveConns` than
  `MaxConns`: the difference is hijacked connections, which hold a slot but are
  no longer the engine's. And a handler that never closes a socket it hijacked
  now keeps a slot as well as a descriptor — that is the ceiling counting a
  connection that is, in fact, still open.

## Socket adoption

`Adopt(name)` takes over a socket the supervisor already bound, which is what
makes a zero-downtime restart possible: the socket survives the exec, so no
connection is lost and no bind races.

- **A named socket the supervisor did not pass fails startup.** Binding one
  instead would lose the very property activation provides, and lose it
  invisibly — the process would look healthy while dropping the connections the
  outgoing one still held.
- **Datagram adoption goes through the raw descriptors.** `sdlisten.Listeners`
  only wraps stream sockets, so `adoptPacket` uses `WithNames` and
  `net.FilePacketConn`. This is done here rather than by widening `sdlisten`,
  which is another domain and would need another ADR.
- **`unsetEnv` is false.** A group may adopt several names; clearing the
  environment on the first lookup would make every later one come back empty.
- **The end-to-end test runs across a real exec.** It cannot be staged
  in-process: `sd_listen_fds(3)` reads from descriptor 3 and a Go test binary
  already holds it. `exec.Cmd.ExtraFiles` places the socket at exactly fd 3 in
  the child, which is what a supervisor does. `LISTEN_PID` is omitted because a
  parent cannot know its child's pid beforehand — the same choice
  `sdlisten.Prepare` makes.

## Concurrency

One goroutine per listener (accept) and one per connection (serve). Both hold an
`inFlight` token released by `defer`. `active` is decremented before the token,
so `active == 0` can briefly precede `inFlight` reaching zero — which is why the
clean drain path waits on the WaitGroup and the expired path does not.

`live` is guarded by its own mutex, held only long enough to copy the socket
slice: `Close` can block, and holding the lock through it would stall every
connection trying to deregister.

## Error range

None of its own. Every failure is a `internal/core/net` sentinel (`0.2.11.*`),
wrapped with the offending group or address. Per ADR 0029 the service layer
declares **no** codes.

## Do NOT

- Perform the TLS handshake in the accept loop.
- Wait on `inFlight` after the drain budget has expired.
- Return a `Conn` (or its `Buffer()`) to a caller that outlives `ServeConn` —
  both are recycled the moment it returns.
- Let a handler's error or panic reach the accept loop.
- Release a hijacked connection's ceiling slot when `ServeConn` returns, or put
  the close tracker above TLS: the first lets every WebSocket escape
  `MaxConns`, the second serves every HTTPS request as plaintext.
- Add a registry of listener types; one canonical engine per family
  (the `proc`/`resilience` no-registry precedent).

## Verification

```
bazel test --config=race //internal/service/net/server:server_test
# Fallback:
cd internal/service && GOWORK=off go test -race -cover ./net/server/...
# expected: coverage ~83%
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0043 — the drain signal — `docs/adr/0043-drain-is-a-signal-not-a-cancellation.md`
- ADR 0047 §D9 — the hijacked-connection hand-over — `docs/adr/0047-sdk-net-websocket.md`
- Contract layer — `internal/core/net/CLAUDE.md`
- The two protocols that depend on both — `internal/service/net/{sse,websocket}/CLAUDE.md`
