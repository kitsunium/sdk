<!-- updated: 2026-11-01T12:00:00Z -->
# internal/service/net/server/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/server/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why-this-shape; §The datagram path; §The HTTP adapter; §Sharded accept; §Connection ceiling.

## Purpose

The inbound engine of the network domain (ADR 0029): one unified listener for
TCP, Unix, TLS and mutual TLS, serving handlers grouped behind shared
middlewares, with an explicit lifecycle and a bounded drain.

Public façade: `pkg/v1/net/server`.

## A failing accept waits

An `Accept` that fails for any reason but its listener closing — the process out
of file descriptors, typically, with connections still queued — used to be
retried AT ONCE: one core spinning per accept loop, measured at 1.16 s of CPU per
second on Linux by a review of a downstream framework, and N cores with N shards.
`acceptLoop` now waits first (ADR 0130):

- **The curve is net/http's**: 5 ms, doubling, held at 1 s, started over by the
  next accepted connection — drawn from the SDK's one curve,
  `kernel/backoff.Value` (ADR 0103), not a fourth hand-written copy. Temporary or not,
  every failure but `net.ErrClosed` waits.
- **The wait is on the engine's clock** — `Server.clk`, `clock.System` unless an
  internal test sets a manual one — so `TestAFailingAcceptWaitsOnTheEnginesClock`
  asserts each wait to the nanosecond and that no `Accept` happens while the
  clock stands still. The stall watcher's tick and the liveness grace are on it
  too (ADR 0188); the drain's poll is still on the wall clock.
- **A listener closed during a wait ends it at once.** The loop is not blocked in
  `Accept` then, so closing the socket alone would not reach it; `boundListener`
  carries a `closed` channel its `Close` closes (zero-value safe, created under a
  `sync.Once`), which the wait selects on —
  `TestShutdownDuringABackoffReturnsAtOnce` shuts down while the clock never
  moves.
- **Every wait is counted** in `State.AcceptBackoffs`, beside the other counters:
  a count that keeps rising is a server that cannot accept, visible without a
  profiler.

## Socket adoption

`Adopt(name)` takes over a socket the supervisor already bound, which is what
makes a zero-downtime restart possible: the socket survives the exec, so no
connection is lost and no bind races.

- **An adopted socket gets exactly the layers a bound one does**, from the
  same function (`layered`): the close tracker when the group has a ceiling,
  then TLS when it carries an identity. The adopted path used to add the
  tracker and never TLS, so `Adopt` + `TLS` served the inherited socket in
  plaintext and reported nothing — the supervisor hands over a raw socket, and
  TLS is this process's layer to add. `TestAdopt_ServesTheInheritedSocket` now
  runs the adoption across an exec under a TLS identity too.
- **A named socket the supervisor did not pass fails startup.** Binding one
  instead would lose the very property activation provides, and lose it
  invisibly — the process would look healthy while dropping the connections the
  outgoing one still held.
- **Datagram adoption goes through the raw descriptors.** `sdlisten.Listeners`
  only wraps stream sockets, so `adoptPacket` uses `WithNames` and
  `net.FilePacketConn`. This is done here rather than by widening
  `internal/service/proc/systemd/listen`, which is another domain and would
  need another ADR. This package imports it as `sdlisten`, the name the
  comments here use: it declares its own `listen` (`listen.go`), and an import
  may not share a name with a package-level declaration.
- **`unsetEnv` is false.** A group may adopt several names; clearing the
  environment on the first lookup would make every later one come back empty.
- **The end-to-end test runs across a real exec.** It cannot be staged
  in-process: `sd_listen_fds(3)` reads from descriptor 3 and a Go test binary
  already holds it. `exec.Cmd.ExtraFiles` places the socket at exactly fd 3 in
  the child, which is what a supervisor does. `LISTEN_PID` is omitted because a
  parent cannot know its child's pid beforehand — the same choice
  `sdlisten.Prepare` makes.
- **Windows has nothing to adopt.** `sdlisten` refuses there with
  `UNSUPPORTED_PLATFORM`, so `Start` fails with `SOCKET_ADOPT_FAILED` whose
  `cause` field names it — never a socket of its own. `ExtraFiles` is itself unsupported on
  Windows, so `TestAdopt` runs its children there with the environment alone and
  still requires the refusal; `TestAdopt_ServesTheInheritedSocket`, whose
  fixture is a child holding an inherited socket, skips there and says why.

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
wrapped with the offending group or address — `LoopGroupStalled` included,
and `RegisterHealth` on a nil registry is `core/app/health.InvalidCheck`. Per
ADR 0029 the service layer declares **no** codes.

The one exception is a platform refusal, and it is the SDK's shared one: a
family this platform has no socket for — `unixgram` and `unixpacket` on
Windows, whose AF_UNIX is stream-only — is `core/proc.UnsupportedPlatform`
(ADR 0018 §(a)), carrying `network` and `goos`, returned before the OS is
asked. `LISTEN_FAILED` would read as a bind an operator could fix, and no
address makes this one bind. `TestWindowsHasNoUnixDatagramOrSeqpacketSocket` is
the measurement behind it and fails the day Windows opens either family.

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
- Read `KITSUNIUM_NET_*` anywhere but `New`, or probe a backend before reading
  them: a switched-off io_uring must never be set up. Add a token to
  `capabilityRegistry` without the design's block, or restart a stalled loop
  group in process (R1).

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
- ADR 0186 — the opt-in loop engine and its HTTP/1.1 profile — `docs/adr/0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md`
- ADR 0187 — the handler type chooses the engine; the kind set, `Plaintext`, `Name`, `Engine` — `docs/adr/0187-the-handler-type-chooses-the-engine.md`
- ADR 0189 — the kernel capabilities switched off by name, the merged report, the `capability` fault, `CheckCapabilities` at bind — `docs/adr/0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md`
- ADR 0188 — the operator's switches, `State`'s faults, fallbacks, override and platform, readiness and liveness — `docs/adr/0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md`
- ADR 0191 — `Loops` lanes on every backend, `OnStart`, `UseLoop` and the probe chain — `docs/adr/0191-a-loop-middleware-forwards-every-callback-and-the-http1-chain-judges-one-canonical-target.md`
- ADR 0192 — UDP GRO and the restored capacities, the handover drain, `BatchReplies` and GSO replies, L3's machine CPU — `docs/adr/0192-a-datagram-socket-reads-coalesced-and-its-replies-may-leave-per-batch.md`
- Contract layer — `internal/core/net/CLAUDE.md`
- The two protocols that depend on both — `internal/service/net/{sse,websocket}/CLAUDE.md`
