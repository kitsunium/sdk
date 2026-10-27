<!-- updated: 2026-10-27T12:00:00Z -->
# internal/service/net/server/

## Purpose

The inbound engine of the network domain (ADR 0029): one unified listener for
TCP, Unix, TLS and mutual TLS, serving handlers grouped behind shared
middlewares, with an explicit lifecycle and a bounded drain.

Public façade: `pkg/v1/net/server`.

## Contents

| File | Surface |
|---|---|
| `server.go` | `Server`, `New` (which reads the operator's variables and the platform once), `Group`, `State` (and the faults, fallbacks, override and platform it reports), `recordFallback` |
| `operator.go` | the operator's switches (ADR 0188): `capabilityRegistry`, the design's registry with each token's rank and whether it is switchable, its kernel capabilities at `rankKernel`, never an engine (ADR 0189); `kernelSwitches`, what a loop group is told is off — the listed ones, or every one under an unknown token — and `uring-pbuf` off ruling io_uring out; `readOperator`, `KITSUNIUM_NET_ENGINE` and `KITSUNIUM_NET_DISABLE` through the injected `lookup`, R5's reading; `operatorEnv.plan`, min(code, environment) for a loop group; `withLookup`, the tests' injection |
| `stall.go` | R1 (ADR 0188 §D5): the stall watcher (`worker.Every`, `stallPoll`, the engine's clock, `PhaseServing` only), `groupStalled`, `markStalled` — what `Serve` selects on —, `loopStallGrace`, and `faults`, the `State.Faults` builder: the environment's, each degraded listener and loop group, each drained ring and stalled group |
| `health.go` | `RegisterHealth` — the `net/server` readiness check, `readinessOf(State)`, and the process-local liveness check |
| `platform.go`, `platform_{linux,bsd,windows,other}.go` | `readPlatform` and `kernelRelease` per GOOS: uname on Linux, `kern.osrelease` on darwin and the BSDs, `GetVersion` on Windows, empty on illumos and Solaris |
| `operator_internal_test.go` | `TestCapabilityRegistry_matchesTheDesign` (reads `design/net.yaml`, the go_test's `data`), `TestReadOperator`, `TestOperatorPlan` (every code × engine × disable combination: never above the code nor the ceiling, never a ruled-out backend, the highest left), and a loop group started under each switch — `ProbeStage` `epoll` where io_uring is ruled out |
| `stall_internal_test.go` | a fake loop engine walked from healthy to one drained ring to a stall: faults, readiness, liveness at the grace to the nanosecond on a manual clock, no stall during a drain, `Serve` returning `LoopGroupStalled`, `RegisterHealth`, the platform and the fallbacks |
| `lifecycle.go` | `Start`, `Serve`, `Shutdown`, `Close`, the accept loop and its backoff (`acceptDelay`, `backOff`), the drain |
| `loop_group.go` | loop groups: `HandleLoop` (ADR 0186 §D9 step 11), `loopRefusal` — TLS, `Use` and every kind of the group's set neither the engine (`loopHonours`: IdleTimeout, MaxConns, Adopt, Plaintext, Name, Engine, UseLoop) nor the chain (`chainHonours`: a layer or the handler) honours, after `buildLoopChain`; `OnStart(lanes)` on the chain before the engine starts, `Use` whatever the handler claims — the listeners bound or adopted and registered like any group's, the engines in `Shutdown`, `Close` and `State` |
| `option_set.go` | `optionSet` — the kinds a group was given, one bit per `corenet.OptionKind` (ADR 0187 §D2); every `GroupOption`, `Use` and `PreHandshake` add theirs, a zero value included |
| `engine_option.go` | `reconcileName` (Name against the declared name, `GroupNameConflict`) and `engineRefusal` — `PlaintextWithTLS`, and an `Engine` the handler's type or `Loop` contradicts (`EngineUnsupported`), judged at `Start`, never folded back (ADR 0187 §D4) |
| `option_set_internal_test.go` | `TestGroupOption_recordsItsKind` — walks every function returning a `GroupOption` in `pkg/v1/net/server/gen_facade.go` and this package (exempt: `Listen`, `Loop`), each given its zero value records exactly its kind; the set's order and width |
| `engine_option_external_test.go` | `TestEngine`, `TestName` — `Engine`, `Plaintext` and `Name` served, refused and what wins, on both natures |
| `loop_engine_linux.go`, `loop_engine_other.go` | the backend, handed `loopPlan.disabled` (ADR 0189); on Linux `nativeEngine` merges server's `tcp-nodelay` row, read back from each listener (`probeNoDelay`), into the leaf's report: on Linux the leaf `loop` on duplicated descriptors (`F_DUPFD_CLOEXEC`, non-blocking), auto falling back to portable with the reason in `State`; elsewhere the portable backend, an explicit native one refused with `UnsupportedPlatform` |
| `loop_chain.go` | loop middlewares (ADR 0191 §D2): `loopLanes` (`Loops`, zero one), `buildLoopChain` — the layers applied outermost first, each a `LoopLayered` whose `Next` is what it wrapped (`sameHandler`, which takes two values of an uncomparable type as the same rather than panic), then the same middlewares over `loopProbe` and every callback sent through by `sendProbe` with `probeIO` — `LoopMiddlewareInvalid` naming the callback; `chainHonours` |
| `loop_outbound.go` | R3, ADR 0194: `jobRing` (SPSC, preallocated, `head` and `tail` a cache line each), `outboundLane` (a lane's `LoopOutbound`: `Send` never blocks, `LoopOutboundFull` counted; one worker goroutine `run`; completions back through a second ring and one `Post` of `runCompletions` by key in `outboundLanes`, a key never given twice and released at `signal`; `Send` keeps a copy of its first `LoopIO`, so it allocates nothing), `outbound` (`signal`, `wait`), `startOutbound`/`findOffloader` (the chain's `LoopOffloader` by `Next`), `startOffloading` (workers before the engine, `group.loopBusy` = `Pending` so the drain keeps waiting connections), `offloadedEngine` (workers stop after the engine; `State.OutboundRefused`) |
| `loop_outbound_alloc_internal_test.go` | (race off) `Send` at 0 allocations a call |
| `loop_outbound_internal_test.go` | the ring's layout, a lane released at stop; every loop backend present, `-race`: a pending answer and pipelined fast/deferred/pending answers behind it in order, a full queue's 503 + `Retry-After` with the connection kept and counted, connections closed first, the holdback ceiling, Pending without a worker, an answer delivered during the drain |
| `loop_chain_internal_test.go` | the chain served on every backend (order, OnStart before any callback, the lane count), every broken layer refused by name, the honours merged, `UseLoop` refused on a goroutine group and recording its kind |
| `loop_chain_conformance_internal_test.go` | P2's conformance: N connections × N tokens through `http1.Routes` and `http1.Bearer` behind `http1.HandleLoop`, two lanes, every backend present, `markRecycled` on — each answer its own subject, a 401 in between, every kept `Auth` still its token's after the buffers were reused |
| `loop_portable.go` | the portable loop backend, `Loops` lanes (`portableGroup`, its acceptors handing connections to the lanes in turn, the group's `live` the lanes' sum, each `portableLoop` its own handler goroutine, connections, post queue and `Loop` index; `markRecycled`, a test's switch that fills each released buffer with 0xAA — ADR 0191), its report every row `unprobed` (`loop.Unprobed`) and `tcp-nodelay` the netpoller's default, cleared per connection when switched off: a reader and a writer goroutine per connection on the netpoller, every callback on one handler goroutine, the reader's buffer held until the batch ends |
| `loop_group_internal_test.go` | loop groups on every backend the platform has: split lines, a 4 MiB response byte-exact, close, spill, drain, and every refusal — a zero-valued option included; `TestLoopGroup_middlewareRefused`, `Use` refused on every backend whatever the handler claims |
| `loop_option_external_test.go` | `TestLoop` — the `Loop` option refused before or after the handler, on both natures, and what wins over it |
| `stream_group.go` | `StreamGroup` — `Handle`, `HandleFunc`, `HandleHTTP`, `Use`, `UseLoop` (kind `OptionLoopMiddleware`, every call); `claimHandler`, the one-handler rule every declaration takes |
| `listen.go` | listener construction; TLS/mTLS wrapping; family validation; the `bindReport` (exclusive, bind wait) |
| `exclusive.go`, `exclusive_{windows,other}.go` | ADR 0193: `listenControl` — `SO_REUSEPORT` when sharded, `SO_EXCLUSIVEADDRUSE` on Windows for unicast TCP/UDP (`exclusiveApplies`, multicast and AF_UNIX excluded) before bind; `exclusiveOf` reads it back from an adopted socket, never fatal; `bindRetrying` — Windows rebinds a `WSAEADDRINUSE` TCP address eight times at most, 25 ms doubling to 1 s, the wait in `BindWait` (UDP, other platforms and a port the server already holds — `Server.rebindAttempts`/`holdsPort` — one attempt); `closeConn` — Windows' graceful close, `CloseWrite` then a read to zero bounded by 500 ms and 64 KiB |
| `exclusive_internal_test.go`, `exclusive_windows_internal_test.go` | the retry and the applicability rule everywhere; on Windows both sockets exclusive, a second bind refused, a plain socket read as not exclusive, a restart on the same address after three echoed connections |
| `conn.go` | the pooled `corenet.Conn` implementation, the `hijacked` hand-over flag, `refused` (the ceiling turned it away: closed at once), and `closeGracefully`, the engine's own close (ADR 0193) |
| `pool.go` | per-connection recycling + the live-socket registry |
| `options.go` | `Option` / `GroupOption` — including the two HTTP-only group options, `ReadHeaderTimeout` and `MaxHeaderBytes`, `Loop`, refused by every group no loop handler serves, and `Plaintext`, `Name` and `Engine` (ADR 0187); each records its kind |
| `packet_group.go` | `PacketGroup` — the datagram mirror of `StreamGroup` |
| `packet.go` | the pooled `corenet.Packet` implementation; `Reply` goes through the socket's `datagramSink` |
| `packet_listen.go` | datagram socket construction (exclusive like a listener, bound once, ADR 0193); family validation; ceilings; `boundPacketConn` (its source, sink and `done`), `handoverSource` |
| `packet_bound.go` | `newBoundPacket` (source with GRO, sink per `BatchReplies`), `handoverPackets` (Shutdown's drain wait, after the stream listeners are closed and within `handoverBudget`, 250 ms, a drain still running then has GRO switched off — ADR 0192 §D4), `stopGROPackets` (Close's), `recordGSOOff` / `gsoFaults` (the `gso` faults of `State`) |
| `packet_loop.go` | the datagram read loop and dispatch: one `Packet` a datagram, a coalesced message split by `serveMessage`, a cut one counted (`Datagrams.TruncatedPackets`); the sink opened before the batch and flushed after it |
| `batch.go` | the `datagramSource` and `datagramSink` seams, reusable read slots (`segment`, `count` for a coalesced message), `datagramCounters`, the reply queue's sizes |
| `batch_portable.go` | one datagram per syscall — the floor everywhere; `portableSink`, `Reply`'s default, one `WriteTo` a reply |
| `reply_queue.go` | `queuedSink`: `BatchReplies` without `sendmmsg` — queued, one `WriteTo` each at the batch's end (darwin, every non-Linux, a Unix datagram socket) |
| `datagram_source_{linux,other}.go` | `newDatagramSource` — the batched reader for a socket exposing a raw descriptor (Linux only), UDP GRO on when asked, the portable one otherwise —, `newDatagramSink` — the portable sink unless `BatchReplies`, then `multiWriter` or `queuedSink` — and `batchAvailable`, which feeds `State` |
| `raw_conn_provider_linux.go` | `rawConnProvider` — the raw-descriptor contract the batched reader needs, declared rather than asserting `*net.UDPConn` |
| `kernel_len_linux.go` | `setKernelLen` — a kernel length field written at the width the architecture gives it (`uint64` or `uint32`) |
| `multireader_linux.go` | `recvmmsg` batched reader: `prepare` restores every capacity before each call, `fill` reads lengths and `msg_flags`, `enableGRO`/`slotSize` (65 535-byte slots under GRO), `groSegment` (the `UDP_GRO` control message), `handover`/`drain`/`stopGRO` — a read returning once `stopGRO` has begun is dropped, its segment size possibly lost (ADR 0192 §D1–§D4) |
| `multiwriter_linux.go` | `multiWriter`, `BatchReplies` on Linux: one `sendmmsg` a batch resumed from its count, runs to one peer of one size as `UDP_SEGMENT` sends (`gsoSegments`, `maxSegment`), `EIO` switching GSO off, `EMSGSIZE` counted, `EINVAL` sent alone (ADR 0192 §D5) |
| `sendmmsg_linux_{amd64,386,other}.go` | `sysSendmmsg`: the syscall's number, absent from `syscall` on amd64 and 386 |
| `multireader_mmsghdr_linux.go` | the cited `struct mmsghdr` layout |
| `sockaddr_linux.go` | kernel sockaddr decoding for the batched path |
| `http_adapter.go` | the `net/http` adapter (ADR 0029 D3) |
| `http_bounds.go` | `httpBounds` — the header phase's own deadline and the header size cap, kept out of `core/net`'s protocol-neutral `Timeouts`/`Limits`; each unset value keeps the adapter's earlier behaviour (the read timeout for the header phase, net/http's 1 MiB cap); `idleOrRead`, the wait between requests |
| `http_read_deadline.go` | `perReadBody` / `deadlineBody` — the read bound applied per read of a request body, cleared at its end (#454) |
| `conn_waiter.go` | `connWaiter` — the completion channel and the hijack flag one `ServeConn` blocks on |
| `http_listener.go` | the channel-fed bridge listener |
| `reuseport_{linux,bsd,other}.go` | the cited `SO_REUSEPORT` constant per family |
| `family_{windows,other}.go` | `platformLacks` — the two unix families Windows cannot open (`unixgram`, `unixpacket`), refused with `UNSUPPORTED_PLATFORM` before the OS is asked |
| `truncation_{windows,other}.go` | `datagramTruncated` — Windows' `WSAEMSGSIZE` (hand-defined, cited), the read that FAILS where Unix truncates |
| `stream_group_limiter.go` | the per-group connection ceiling — a reject-mode semaphore whose slot a hijacked connection keeps until it closes |
| `conn_tracked.go` | `trackedConn` — the socket that reports its own `Close`, so a hijacked connection's slot comes back when it ends |
| `listen_tracked.go` | `trackedListener` — hands out `trackedConn`s beneath TLS, for a group that serves HTTP under a ceiling |
| `adopt.go` | adoption of listeners inherited from a supervisor |
| `handshake.go` | the bounded TLS handshake — `handshakeHandler`, and `defaultHandshakeTimeout` (10 s) for a group that sets none |
| `swallow.go` | `swallowErr` — the recorded discard of a non-actionable cleanup error |
| `load_gate_linux_test.go` | the network merge gate's L1 and L2 (plan P8, `tools/netgate`): paired (ADR 0190): `BenchmarkLoadL1/pair=goroutine:loop` (P6) and `pair=loop:loop` (the calibration), `BenchmarkLoadL2/pair=handwritten:chain` (P2: the same auth and routing through `http1.Routes` and `http1.Bearer`, `chainL2`) and `pair=handwritten:handwritten` (its calibration) — one child (`TestLoadChild`, this binary re-run, `KITSUNIUM_NETGATE_CHILD=L2:a,b`) serves both engines side by side, 1000 kept-alive connections each, a 1 KiB body, and a round sends `b.N` requests in 2×`pairedWindows` windows, A, B, B, A, …; the metric is ITS CPU (getrusage at each window's ends), reported as `a:cpu-ns/req`, `b:cpu-ns/req` and `ratio:cpu-ns/req`, one paired sample a round; `TestLoadL1L2_wired` serves each engine for three requests, `TestLoadPaired_wired` two side by side |
| `load_l3_linux_internal_test.go` | L3, paired like L1 and L2 (ADR 0190): `BenchmarkLoadL3/pair=recvmmsg:recvmmsg` (P4a's calibration) and `pair=recvmmsg:gro` (P4a's gate: GRO off, engine A; GRO on, engine B; `l3Sources`), one receiver (`TestLoadL3Child`, this binary re-run in the namespace) reading both engines' sockets, each through its own source, slots of `slotSize` and a coalesced message counted by `count`, windows A, B, B, A, … (`l3Windows`, b.N datagrams an engine) over the veth pair of `scripts/ci/net-veth.sh`, 1200-byte datagrams from 4 GSO flows (`UDP_SEGMENT`, 54 segments a send) at 200 000 a second; every window metered by one function (`l3Meter`, `l3Round`): `cpu-ns/datagram` — `l3CPU`: the receiver's rusage, the sender's and /proc/stat's irq and softirq time, over the datagrams sent, its parts reported as `rcv-`, `snd-`, `irq-cpu-ns/datagram` (ADR 0192 §D6) —, `datagrams/syscall` and `lost/op`, `a:`, `b:` and `ratio:`; a window below the receiver's own CPU or a message cut short fails the round (`TestL3CPU_check`, `TestL3Round_symmetric`) |
| `multireader_gro_linux_internal_test.go` | GRO on the reader: a burst split, a shorter last segment, the segment size after a plain datagram (capacities restored), `MSG_CTRUNC` cut, a caller's `IP_PKTINFO` with GRO off (no control room, never cut) and on (room beside `UDP_GRO`), `slotSize`, the handover drain (split, then GRO off, then `net.ErrClosed`), `stopGRO`, `groSegment`'s walk |
| `multiwriter_linux_internal_test.go` | `gsoSegments` (54 at 1200 on both families), the flush over loopback at the cap (54 in one message, 55 in two, a shorter last, past an Ethernet frame alone), a reply outside a batch, a scripted `sendmmsg` for `EIO`/`EINVAL`/`EMSGSIZE` and a partial count, `EINVAL` remembered across batches (`segmentCeiling`, GSO off after `gsoInvalidLimit` in a row), `sendmmsg` refused (`ENOSYS`/`EPERM`: `WriteTo` each, kept off when one leaves), `encode` and `pendingReply.addr` |
| `reply_queue_internal_test.go`, `packet_bound_internal_test.go`, `batch_replies_external_test.go` | the queued sink (copy, order, failures counted, full queue), the handover wait and its budget, the `gso` faults, the sink a group's option picks, `BatchReplies` end to end and refused by a stream group |
| `{batch,conn,linux}_compliance.go` | compile-time interface assertions (`KTN-IFACE-ASSERT-PLACEMENT`) |

## Why-this-shape

- **Goroutine per connection on the runtime netpoller** (ADR 0029 D2). Not an
  event loop: TLS is a hard requirement and `crypto/tls` is written against
  blocking `net.Conn`, the netpoller already *is* an epoll, and every io helper
  in the ecosystem speaks `net.Conn`.
  It stays the default engine and the only one that serves TLS. ADR 0186 adds
  an opt-in, plaintext-only loop engine beside it, entered by one method,
  `StreamGroup.HandleLoop`, with this package keeping the lifecycle, the bind,
  the adoption, the admission and the portable backend.
- **`conn` embeds `stdnet.Conn`**, so reads and writes reach the socket with no
  wrapper on the hot path, and `io.Copy(c, c)` is a working echo server.
- **`Group` returns the group, not `(group, error)`.** A declaration mistake is
  recorded in `declErr` and surfaced by `Start`. This is the trade that keeps
  wiring a server to five statements; it is only acceptable because `Start`
  reports every recorded error, and `TestServer_Start` pins that it does.
- **A stream group declares one handler, once** (ADR 0186 §D1). `Handle`,
  `HandleFunc`, `HandleHTTP` and `HandleLoop` each claim the group through `claimHandler`; a
  second claim, of any of them and in any order — a `nil` handler included —
  keeps the first handler, records `HandlerAlreadySet` naming the group on the
  group's own `declErr`, and `Start` returns it before binding anything. The
  group records it, not the server, because a handler is declared on the group
  after `Group` has released the server's lock. Replacing used to be the rule,
  and `HandleHTTP` followed by `Handle` left the HTTP adapter installed beside
  the new handler, so the group served neither cleanly; `HandleHTTP` now claims
  before it builds its adapter. `HandleHTTP`'s body is `mountHTTP`'s so the
  method stays inlinable. A `PacketGroup` keeps its replacing `Handle`.
- **`Loop(v)` is judged once the handler is known** (ADR 0186 §D1). The
  option records its value and that it was given — a zero `LoopValue`
  included — and `Start` judges it in `groupDeclErr`, before anything is
  bound: a `HandleLoop` group runs on it, any other group refuses it with
  `LoopOptionUnsupported` and `option=Loop`. A missing handler (`HandlerMissing`) and a second one
  (`HandlerAlreadySet`) win, because they explain the refusal. A datagram group
  records the refusal at declaration, since its shim would otherwise drop it.
- **Every option records its kind, and a refusal reads the set** (ADR 0187
  §D2). `kinds`, an `optionSet`, holds one bit per `OptionKind` given — by every
  `GroupOption`, by `Use` on every call, by `PreHandshake` — whatever the
  argument. `loopRefusal` walks it in the block's order, so `ReadTimeout(0)`,
  `Shards(0)` or `TLS` with a zero identity on a loop group is refused as its
  non-zero form is; guessing "given" from non-zero fields let them through.
  `OptionKind` stays an iota and new kinds go after `OptionPreHandshake`;
  `Listen` and `Loop` record none. A new `GroupOption` without its kind fails
  `TestGroupOption_recordsItsKind`, which reads the facade's file (the
  go_test's `data`; `pkg/v1/net/server` exports it).
- **`Plaintext`, `Name` and `Engine` are honoured by every engine, and
  contradictions are refused, never folded back** (ADR 0187 §D3, §D4).
  `engineRefusal` runs in `startRefusal` (each group of `groupDeclErr`) for every group with a handler,
  before `loopRefusal`: `Plaintext` with `TLS` is `PlaintextWithTLS`;
  `EngineGoroutine` with `Loop` or `HandleLoop`, `EngineLoop` with a
  goroutine handler, or a value outside the block is `EngineUnsupported`.
  `EngineAuto`, the zero, lets the handler's type choose. A datagram group's
  shim is judged the same way at declaration (`judgePacketOptions`).
  `Group` and `PacketGroup` apply their options before the duplicate check,
  so `Name` may name a group declared with `""`; an empty `Name`, or one that
  differs from a declared name, records `GroupNameConflict`. `Plaintext` is
  not yet required of a plaintext HTTP group: that lands with the simple API
  (ADR 0187 §D7). `MaxConns(0)` keeps "no ceiling": zero means the default for
  options added from ADR 0187 on only.
- **A loop group is a group like the others, served by another engine** (ADR
  0186 §D1, §D3). It binds and adopts through the same `listen` and
  `adoptStream`, without TLS and without sharding, and its listeners join
  `s.listeners` and `s.states`, so `Close` and `Shutdown` close them as any
  other. The engine (`loopEngine`) drains within `Shutdown`'s budget, and its
  overrun is the drain's. `State.Loops` reports it. `server` imports the leaf
  `loop` on Linux only. The portable backend stays here, because it needs
  `net.Conn` and the netpoller; it serialises every callback onto one
  goroutine, and each reader waits for the batch's end before it reuses its
  buffer, so `buf` stays valid until `OnBatchEnd` as the contract says. Its
  `LoopIO` and its lifecycle are two types (`portableLoop`,
  `portableEngine`), because `LoopIO.Close` names a connection.
- **A loop chain is checked once, at Start, and costs nothing after** (ADR
  0191 §D2). `UseLoop`'s middlewares are applied in `loopRefusal`, before
  anything is bound, and the chain the engine serves is `loopChain`. Each
  layer must say what it wraps (`LoopLayered`); then the same middlewares
  are built again over a probe and the eight callbacks sent through once, so
  a layer that keeps one fails `Start` by name. A middleware is therefore
  called twice; wrapping the served handler in a recorder instead would make
  every callback of every connection pay for a property proved once. A
  goroutine group refuses `UseLoop` (R6).
- **Every backend runs `Loops` lanes** (ADR 0191 §D1). The portable backend
  shards like a native one, so `OnStart(loopLanes(v))` is called once by
  `bindLoopGroup` before the engine starts and a fallback to portable keeps
  the count the chain was told. Its lanes share the listeners, the group's
  counters and its capability report; `MaxConns` and soft pressure read the
  lanes' summed `live`.
- **The operator's switches only restrict, and are read before any probe**
  (ADR 0188, R5). `New` reads `KITSUNIUM_NET_ENGINE` and
  `KITSUNIUM_NET_DISABLE` once, through `lookup` (`os.LookupEnv` unless a test
  injects a map), and caches the answer; nothing reads the environment again.
  The vocabulary is `design/net.yaml`'s `Capability` blocks, which
  `capabilityRegistry` is held to by a test reading the design. An empty value
  is unset; an unknown token, or a floor named in DISABLE, caps every group at
  portable and is a fault. A loop group runs on `operatorEnv.plan` — the highest
  backend at or below its code's that the environment allows: the leaf is asked
  for `epoll` explicitly, or the portable backend starts without it, so a
  ruled-out io_uring is never set up; `auto` keeps its fallback to portable, an
  explicit backend lowered refuses on failure, an explicit native backend off
  Linux is refused first, whatever the environment. A goroutine-engine group is
  the floor: the environment changes nothing on it, and never TLS, `Plaintext`
  or a limit on any group. A group the environment lowered is overridden, not a
  fault and not a fallback.
- **`State` says what the server runs without** (ADR 0188 §D4). `Faults` lists
  the environment's unknown tokens, every degraded listener and loop group, and,
  while serving, each ring the watchdog drained (`ring`) and each group every
  ring of which is drained (`stall`), naming the runbook
  (`KITSUNIUM_NET_DISABLE=iouring`); a drain stops every ring on purpose, so
  nothing of it is a fault. `Degraded()` reads `Faults`. `Fallbacks` counts the
  loop groups that started below their code's choice because the better
  backend failed (the engine's `State().Degraded` at start), with the last one;
  `Platform` is read once by `New`.
- **No restart in process: a stall stops `Serve` and fails liveness** (R1). A
  watcher ticks every `stallPoll` on the engine's clock while a loop group runs
  and the server serves; the first group whose every ring is drained is
  recorded, `Serve` drains and returns `LoopGroupStalled` naming it, and the
  `net/server` liveness check `RegisterHealth` registers fails once
  `loopStallGrace` (30 s) has passed since — for a process that runs without
  `Serve`. Readiness, from `State`, fails at once. **A receive that never
  completes on a live ring is not watched**: only the io_uring accept watchdog
  drains a ring, and a silent peer is `IdleTimeout`'s to bound. The portable
  backend has no ring and never stalls.
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
  `Shutdown` forever. `TestServer_Shutdown_ReportsAnExpiredBudget` is the
  regression guard, and it went from a 120-second hang to 1.1 seconds when
  fixed.
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
- **Every capacity is restored before every call** (ADR 0192 §D1). The kernel
  writes the lengths it used back into each header; a message without control
  data leaves `msg_controllen` at zero, and the next coalesced message would
  find no room for its segment size. `prepare` resets `msg_namelen`,
  `msg_controllen` and `msg_flags` on every header before every `recvmmsg`.
- **UDP GRO wherever the cmsg reader is** (§D3). Only the batched reader
  parses `UDP_GRO`, so only it switches the option on; its slots hold a whole
  coalesced message (65 535 bytes, 1 MiB a socket at the default batch), and
  `serveMessage` hands each datagram of it to the handler as its own `Packet`.
  `MSG_CTRUNC`, and `MSG_TRUNC` under GRO, drop and count the message.
- **The handover drains with GRO on, then switches it off** (§D4). The plan's
  first wording was the reverse; `udp_recvmsg` writes the segment size only
  while `UDP_GRO` is on, so a message queued coalesced and read after the
  switch arrives as one datagram the length of all of them —
  `Test_multiReader_handover` pins the order.
- **`Reply` is synchronous unless the group asks** (§D5). `BatchReplies`
  queues a batch's replies and sends them at its end: `multiWriter` on Linux
  (`sendmmsg`, GSO runs), `queuedSink` elsewhere; a reply is then reported
  sent when queued and a failure counted in `Datagrams.ReplyFailures`. A stream
  group refuses the option. The sinks are mutex-guarded: a kept `Packet` may
  reply from another goroutine, and outside a batch that reply is written at
  once.
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
- **An oversized datagram is counted on every platform, and the platforms
  disagree about what it is.** Unix truncates it in silence and the probe byte
  past the ceiling is filled; Windows FAILS the read with `WSAEMSGSIZE`. The
  portable reader handed that error up and the read loop skipped it, so on
  Windows a datagram past `MaxPacketSize` was neither delivered nor counted —
  `OversizedPackets` read zero for drops that happened (ADR 0095). The reader now
  turns the errno into a slot filled to the probe byte, which `dispatch` drops
  and counts like any other. `WSAEMSGSIZE` lives in `internal/syscall/windows`,
  which nothing outside the stdlib may import, so it is declared and cited in
  `truncation_windows.go`.
- **`Test_newDatagramSource` is the only test that proves the batched path is
  in use.** Every behavioural datagram test passes identically on the
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

**The read bound is per read of the body, not `http.Server.ReadTimeout` (#454).**
`ReadTimeout` used to become `http.Server.ReadTimeout`, a budget for the WHOLE
request: a peer sending a 20-byte body two bytes every 100 ms under a 500 ms
bound was cut off after the eighth byte, against `TimeoutsValue`'s "every
field is a PER-OPERATION bound". The adapter now leaves `http.Server.ReadTimeout`
at zero and wraps the handler (`http_read_deadline.go`, `perReadBody`): each
request body is a `deadlineBody` that sets the read deadline through
`http.ResponseController` (the connection's on HTTP/1, the stream's on HTTP/2)
before every read, and CLEARS it once the body has ended — at EOF net/http
starts a background read watching for the peer going away, and a deadline
left from the last body read would expire under it and cancel the request's
context while the handler still works. The header phase keeps
`ReadHeaderTimeout` (falling back to the read budget), and `IdleTimeout` falls
back to the read budget (`idleOrRead`), the fallback net/http applied itself
while `ReadTimeout` was set. Time a handler spends between reads is its own.
`TestReadTimeoutBoundsEachReadOfAnHTTPBody` pins the slow body, a stall, and a
handler slower than the bound between and after its reads.

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
  `State().ActiveConns`. The drain SIGNAL is what reaches it instead.
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
- **The serving goroutine is a `kernel/concur/worker.LoopDaemon`**, so its owner and
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
- **The same rule holds for a platform that cannot shard at all.** An explicit
  count is a degradation on Windows; auto-sizing is not, and reports nothing.
  It used to return the reason with `degraded=false`, so `State` contradicted
  itself on every auto-sized Windows listener (ADR 0095). `shardsFor` takes the
  platform's answer as a parameter, so `Test_resolveShards` runs every case both
  ways on every lane rather than on Windows alone.
- **Only the first shard appears in `State`**, carrying the real shard count. N
  rows for one address would read as N addresses.
- **On this machine it buys nothing.** `BENCH.md` §4 reports the null result and
  the two tests that prove it is a null result rather than a broken comparison.

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
