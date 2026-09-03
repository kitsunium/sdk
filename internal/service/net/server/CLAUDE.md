<!-- updated: 2026-09-03T00:00:00Z -->
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
| `conn.go` | the pooled `corenet.Conn` implementation |
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
- Contract layer — `internal/core/net/CLAUDE.md`
