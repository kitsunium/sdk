<!-- updated: 2026-11-01T12:00:00Z -->
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
| `operator.go` | the operator's switches (ADR 0188): `capabilityRegistry`, the design's registry with each token's rank and whether it is switchable, its kernel capabilities at `rankKernel`, never an engine (ADR 0189); `kernelSwitches`, what a loop group is told is off — the listed ones, or every one under an unknown token — and `uring-pbuf` off ruling io_uring out; `readOperator`, `KITSUNIUM_NET_ENGINE` and `KITSUNIUM_NET_DISABLE` through the injected `lookup`, R5's reading; `operatorEnv.plan`, min(code, environment) for a loop group, on the platform's poller — kqueue beside epoll at one rank, each switched off by its own token, `kqueuePlan` on darwin and the BSDs, `Override.Applied` naming the platform's own at that rank (ADR 0222); `withLookup`, the tests' injection |
| `stall.go` | R1 (ADR 0188 §D5): the stall watcher (`worker.Every`, `stallPoll`, the engine's clock, `PhaseServing` only), `groupStalled`, `markStalled` — what `Serve` selects on —, `loopStallGrace`, and `faults`, the `State.Faults` builder: the environment's, each degraded listener and loop group, each drained ring and stalled group |
| `metrics.go` | `RegisterMetrics` (#847, ADR 0188 amended) — the server's counters on an `AsyncMeter` as observables, mirroring `RegisterHealth`: `metricInstruments` (the `Metric…` constants, design-declared: the server's `MetricAcceptBackoffs`, `MetricDatagramsTruncated`, `MetricGSOOversized`, `MetricReplyFailures`, `MetricSuppressedLogs`, `MetricDegraded`; a goroutine stream group's `MetricConnsActive`/`MetricConnsTotal`/`MetricConnsRejected`, attribute `group`, from `connCounters` — `MetricConnsTotal` the connections the group served (`admitted`, counted by `admit` before the handler runs, so it never falls), a connection turned away by the ceiling or a refused PROXY header out of it although `StateValue.TotalConns` counts it; a loop ring's `MetricLoopBatches`, `…Completions`, `…BufferStarved`, `…BatchesCut`, attributes `group`, `loop`, `ring`; a loop group's `MetricLoopRingsDrained`, `…OutboundRefused`, `…BatchP99` in seconds), `metricsReader` — one `State` read per collection, the first callback of a round taking it —, `claimMeter` (`MetricsRefused`: a nil meter, or the same meter twice; another meter is another destination), units in UCUM. The caller's attributes ride on every series. `metrics_external_test.go` (a Prometheus export after a ceiling of 3 met 4 connections: `net_server_conns_total` 3, `…_rejected_total` 1, State's `TotalConns` 4) |
| `health.go` | `RegisterHealth` — the `net/server` readiness check, `readinessOf(State)`, and the process-local liveness check |
| `platform.go`, `platform_{linux,bsd,windows,other}.go` | `readPlatform` and `kernelRelease` per GOOS: uname on Linux, `kern.osrelease` on darwin and the BSDs, `GetVersion` on Windows, empty on illumos and Solaris |
| `operator_internal_test.go` | `TestCapabilityRegistry_matchesTheDesign` (reads `design/net.yaml`, the go_test's `data`), `TestReadOperator`, `TestOperatorPlan` (every code × engine × disable combination: never above the code nor the ceiling, never a ruled-out backend, the highest left), and a loop group started under each switch — `ProbeStage` `epoll` where io_uring is ruled out |
| `stall_internal_test.go` | a fake loop engine walked from healthy to one drained ring to a stall: faults, readiness, liveness at the grace to the nanosecond on a manual clock, no stall during a drain, `Serve` returning `LoopGroupStalled`, `RegisterHealth`, the platform and the fallbacks |
| `lifecycle.go` | `Start`, `Serve`, `Shutdown`, `Close`, the accept loop and its backoff (`acceptDelay`, `backOff`), the drain: readiness false, `waitGrace` (`WithDrainGrace`, ADR 0196 §D2), then the listeners |
| `drain_internal_test.go` | plan P6 (ADR 0196): the drain on every backend, `LoopsAuto` on — clients that stop dialling 150 ms after readiness turned false under a 400 ms grace, none cut short, the grace serving what came after readiness turned false, kept connections ending cleanly — each case on the lane's line echo and on h2c (`drain_proto_internal_test.go`, `drainProtos`: a GET answered `ok` with no reset, a kept connection's GOAWAY(NO_ERROR) then its end, #961); arrivals that never stop until a dial is refused, every connection dialled before the close's own 20 ms window reading its echo (skipped on Windows, where the portable backend takes no queue) —, `LoopsAuto`'s snapshot in `State`, and the grace ended by the caller's ctx; `laneEchoH`, a handler that keeps nothing per connection, since a `LoopConnRef` names a connection within its lane only |
| `loop_portable_take_unix.go`, `loop_portable_take_other.go` | the portable drain's take (ADR 0196 §D3): `ownListeners` duplicates each listener server bound (`File`, `FileListener`), so server's close after the grace leaves it listening; `takeQueue` accepts on its own non-blocking descriptor to `EAGAIN` (`acceptCloexec` under `RawConn.Control`, `FileConn`), holding what it accepted but could not wrap for want of a descriptor (`portableTake`) and waiting each tick while a lane has connections to close; on Windows the listeners stay server's and nothing is taken |
| `loop_portable_accept_linux.go`, `loop_portable_accept_unix.go` | `acceptCloexec`: `accept4(SOCK_CLOEXEC)` on Linux, `accept` then `CloseOnExec` under `syscall.ForkLock` elsewhere |
| `loop_group.go` | loop groups: `HandleLoop` (ADR 0186 §D9 step 11), `loopRefusal` — a declared `unixpacket` address first, before anything is bound (`LoopOptionUnsupported` naming `network=unixpacket`, #974: every backend reads a byte stream and would cut a record; an inherited one is refused once adopted, `refuseUnixpacketListeners`), then TLS, `Use` and every kind of the group's set neither the engine (`loopHonours`: IdleTimeout, MaxConns, Adopt, Plaintext, Name, Engine, UseLoop) nor the chain (`chainHonours`: a layer or the handler) honours, after `buildLoopChain`; `OnStart(lanes)` on the chain before the engine starts, `Use` whatever the handler claims — the listeners bound or adopted and registered like any group's, the engines in `Shutdown`, `Close` and `State` |
| `option_set.go` | `optionSet` — the kinds a group was given, one bit per `corenet.OptionKind` (ADR 0187 §D2); every `GroupOption`, `Use` and `PreHandshake` add theirs, a zero value included |
| `engine_option.go` | `reconcileName` (Name against the declared name, `GroupNameConflict`) and `engineRefusal` — `PlaintextWithTLS`, and an `Engine` the handler's type or `Loop` contradicts (`EngineUnsupported`), judged at `Start`, never folded back (ADR 0187 §D4) |
| `option_set_internal_test.go` | `TestGroupOption_recordsItsKind` — walks every function returning a `GroupOption` in `pkg/v1/net/server/gen_facade.go` and this package (exempt: `Listen`, `Loop`), each given its zero value records exactly its kind; the set's order and width |
| `engine_option_external_test.go` | `TestEngine`, `TestName` — `Engine`, `Plaintext` and `Name` served, refused and what wins, on both natures |
| `loop_engine_linux.go`, `loop_engine_bsd.go`, `loop_engine_native.go`, `loop_engine_other.go` | `nativePoller` names the platform's readiness poller — epoll on Linux, kqueue on darwin and the BSDs (ADR 0222), none elsewhere — and `operator.plan` reads it (on darwin and the BSDs `kqueuePlan`: kqueue only when the code names it, `auto` the portable backend until ADR 0222 §D6's gate holds); `startLoopEngine` refuses the other platforms' backends with `UnsupportedPlatform` (kqueue on Linux, io_uring and epoll on the BSDs, all three elsewhere), then `startNativeEngine` (`loop_engine_native.go`, shared) starts the leaf or the portable backend; `dupHandoff` is `F_DUPFD_CLOEXEC` on Linux, `Dup` under the fork lock on the BSDs (OpenBSD takes no raw `fcntl`). The backend, handed `loopPlan.disabled` (ADR 0189); on Linux `nativeEngine` merges server's `tcp-nodelay` row, read back from each TCP listener (`probeNoDelay`; `unprobed`, reason `unix socket`, on a group with no TCP listener, #974), into the leaf's report: on Linux the leaf `loop` on duplicated descriptors (`F_DUPFD_CLOEXEC`, non-blocking), auto falling back to portable with the reason in `State`; elsewhere the portable backend, an explicit native one refused with `UnsupportedPlatform` |
| `loop_chain.go` | loop middlewares (ADR 0191 §D2): `loopLanes` (`Loops` once `bindLoopGroup` resolved it with `loop.ResolveLoops`, ADR 0196 §D1), `buildLoopChain` — the layers applied outermost first, each a `LoopLayered` whose `Next` is what it wrapped (`sameHandler`, which takes two values of an uncomparable type as the same rather than panic), then the same middlewares over `loopProbe` and every callback sent through by `sendProbe` with `probeIO` — `LoopMiddlewareInvalid` naming the callback; `chainHonours` |
| `loop_outbound.go` | R3, ADR 0194: `jobRing` (SPSC, preallocated, `head` and `tail` a cache line each), `outboundLane` (a lane's `LoopOutbound`: `Send` never blocks, `LoopOutboundFull` counted; one worker goroutine `run`; completions back through a second ring and one `Post` of `runCompletions` by key in `outboundLanes`, a key never given twice and released at `signal`; `Send` keeps a copy of its first `LoopIO`, so it allocates nothing), `outbound` (`signal`, `wait`), `startOutbound`/`findOffloader` (the chain's `LoopOffloader` by `Next`), `startOffloading` (workers before the engine, `group.loopBusy` = `Pending` so the drain keeps waiting connections), `offloadedEngine` (workers stop after the engine; `State.OutboundRefused`) |
| `loop_outbound_alloc_internal_test.go` | (race off) `Send` at 0 allocations a call |
| `loop_outbound_internal_test.go` | the ring's layout, a lane released at stop; every loop backend present, `-race`: a pending answer and pipelined fast/deferred/pending answers behind it in order, a full queue's 503 + `Retry-After` with the connection kept and counted, connections closed first, the holdback ceiling, Pending without a worker, an answer delivered during the drain |
| `loop_chain_internal_test.go` | the chain served on every backend (order, OnStart before any callback, the lane count), every broken layer refused by name, the honours merged, `UseLoop` refused on a goroutine group and recording its kind |
| `loop_chain_conformance_internal_test.go` | P2's conformance: N connections × N tokens through `http1.Routes` and `http1.Bearer` behind `http1.HandleLoop`, two lanes, every backend present, `markRecycled` on — each answer its own subject, a 401 in between, every kept `Auth` still its token's after the buffers were reused |
| `loop_portable.go` | the portable loop backend, `Loops` lanes (`portableGroup`, its acceptors handing connections to the lanes in turn, the group's `live` the lanes' sum, each `portableLoop` its own handler goroutine, connections, post queue and `Loop` index; `markRecycled`, a test's switch that fills each released buffer with 0xAA — ADR 0191), its report every row `unprobed` (`loop.Unprobed`) and `tcp-nodelay` the netpoller's default, cleared per connection when switched off: a reader and a writer goroutine per connection on the netpoller, every callback on one handler goroutine, the reader's buffer held until the batch ends; a batch costs what it touched — the timers are scanned once a `portableTick`, sooner for a deadline set before it, and only the spills whose head was consumed are compacted (`TestPortable_batchCostIndependentOfOpen`, its rounds measured with no scan due and failing on one: the scan of 10 000 connections takes the whole tick under -race and coverage, fell due again at every round, and the fastest round was the scan, #779); once a lane stops, `closeAll`'s drainer outlives the handshakes still running (`negotiating`) and `discard`s what reaches it — a connection handed over and never opened closed and uncounted |
| `loop_portable_stop_internal_test.go` | a stopped lane's buffered events emptied without waiting: an open's connection closed, `live` released, a data event's connection left to `closeAll` |
| `loop_portable_sendfail_internal_test.go` | a failed send is the error `OnClose` reports (#971): the writer records it (`portableConn.failed`) with the flight it ended and before it closes the socket, and `finish` reads it in place of a clean end — the kernel hands a socket error to one call only, so the reader ends on EOF, or on the writer's close, and either, taken first, closed the connection cleanly (the runner's blackhole, portable: "ended cleanly 972ms after the peer vanished"); a fake connection whose close waits for the lane to take the reader's end fails both ways on the code before |
| `loop_group_internal_test.go` | loop groups on every backend the platform has: split lines, a 4 MiB response byte-exact, close, spill, drain, and every refusal — a zero-valued option included; `TestLoopGroup_middlewareRefused`, `Use` refused on every backend whatever the handler claims |
| `loop_option_external_test.go` | `TestLoop` — the `Loop` option refused before or after the handler, on both natures, and what wins over it |
| `stream_group.go` | `StreamGroup` — `Handle`, `HandleFunc`, `HandleHTTP`, `Use`, `UseLoop` (kind `OptionLoopMiddleware`, every call); `claimHandler`, the one-handler rule every declaration takes |
| `bufsize.go`, `bufsize_{unix,windows,other}.go` | #973, ADR 0187 amended: the `ReceiveBuffer` and `SendBuffer` options. `listenControl` sets SO_RCVBUF and SO_SNDBUF before bind on every socket of the group (`bufferControl`, `socketSpec`), a datagram group's and a stream group's listeners, whose accepted sockets inherit them, on both engines (`buffers_linux_internal_test.go` reads an accepted socket back over NETLINK_INET_DIAG, the one way past io_uring's direct descriptors); an adopted socket takes them after the fact (`adoptedBuffers`); what the kernel holds is read back into `ListenerStateValue.ReceiveBuffer` and `SendBuffer` (`socketBuffers`): Linux doubles and clamps to rmem_max and wmem_max, the BSDs and darwin answer ENOBUFS past their ceiling and keep the size they had (`bufferRefusal`), and neither is a fault. A negative value fails Start (`valueRefusalBuffers`) |
| `pktinfo_linux.go`, `pktinfo_other.go` | #972: `udp-pktinfo`, class A-kernel. A UDP socket bound to a wildcard address asks for IP_PKTINFO or IPV6_RECVPKTINFO (`enableDestination`, after bind, the same place as GRO and the drop count, so a bound and an adopted socket take the one path); `multiReader.readControl` reads it into `datagram.local` (`pktinfoOf`), `packet.To` answers it with the socket's port, and `Reply` leaves from it: `pinWriter` (one sendmsg, built once) off the batch, `multiWriter` beside UDP_SEGMENT in a batch. The source is the kernel's `ipi_spec_dst` on IPv4, so a broadcast or a multicast destination answers from the interface's address; on IPv6 the destination itself unless it is a group, a mapped broadcast or `::`. The interface index stays zero, so asymmetric routing still applies. A source the kernel refuses (EINVAL) is sent again from its own choice, not lost. A socket bound to one address does not ask, reports that address and has no row; `KITSUNIUM_NET_DISABLE=udp-pktinfo` restores To and the source of the kernel; off Linux the row reads unprobed with `destination address unavailable` |
| `keepalive.go`, `keepalive_linux.go`, `keepalive_other.go` | #971, the goroutine and portable halves (ADR 0189's rows for the native loop live in `net/loop`): the `KeepAlive` and `UserTimeout` options on `TimeoutsValue`. Goroutine and portable: `applyKeepAlive` configures the listener's `ListenConfig`, the standard library applying it at Accept, and `applyAcceptedKeepAlive` per accepted connection on the portable lane, whose accepts do not go through it; zero keeps the standard library's 15 s, negative sets none. `TCP_USER_TIMEOUT` on the listener before bind (`userTimeoutControl`, Linux only), inherited by every accepted socket: `UserTimeout`, else the group's write bound, else none (`resolveUserTimeout`). The operator's `tcp-keepalive` and `tcp-user-timeout` switches apply to these engines too (`keepAliveOf`, `userTimeoutOf`). The loop's `Config.UserTimeout` takes the same resolution |
| `compress.go` | #853: `Compress`, a middleware for `HandleHTTP` handlers. `compressWriter` holds the status and up to `MinBytes` of the body so a small body stays identity; the answer is then chosen on the allowlist of types, by `CompressValue` or the defaults, which exclude `text/html` (BREACH) and `text/event-stream`. A response without a body (HEAD, 1xx, 204, 304), one with a Content-Encoding or Content-Range, or marked `Cache-Control: no-transform` is never compressed. A handler that names no type gets net/http's sniff on its bytes first, so the compressed body is not typed gzip. `Vary: Accept-Encoding` on every answer of an allowlisted type; a strong ETag is weakened; `Flush` decides, compressing a stream whatever its length, and reaches the client through gzip's sync point. Gzip writers recycled in the middleware's `sync.Pool`, one per level; a panicking handler's partial stream is not closed, net/http aborting it. `Unwrap` gives the writer beneath to `http.ResponseController`. Sendfile is not reached through this writer: on large identity files mount it under `Precompressed` in `net/static`, which needs no `Compress` |
| `proxy.go` | #841: the `ProxyProtocol` option, PROXY protocol v1 and v2 under TLS and the close tracker (`layered`). `proxyListener.Accept` closes a peer outside `Trusted` before a byte is read, in the accept loop and with no goroutine; every other socket is a `proxyConn`. `admitProxied` reads the header on the connection's own goroutine (`establish`, exact reads, nothing past the header consumed, bounded by the timeout on the server's clock), then Admission on the announced address — a proxied group's Admit runs there, not in the accept loop. A header refused counts in `ProxyRefused` and the connection closes at once (`c.refused`). `proxyConn` forwards the socket's `CloseWrite`, `ReadFrom`, `WriteTo` and `Unwrap`. A v2 payload past 2 048 B is refused before it is read; a LOCAL command and an UNSPEC family or a v1 UNKNOWN keep the socket's address; UDP and Unix families are refused on a stream listener. Trust is by address, so a Unix group refuses the option; loop and datagram groups refuse it |
| `steer.go`, `steer_linux.go`, `steer_other.go`, shards in `lifecycle.go` (`bindPacketShards`) | #977: `Shards` on a datagram group, `Shards(0)` GOMAXPROCS as for a stream, a group without it one socket with no SO_REUSEPORT, a Unix datagram group refusing `Shards(n > 1)` by name. `n` sockets per address through SO_REUSEPORT, the port of an address given as `:0` pinned from the first (`pinPort`), one read loop, reader and sink each, `ListenerStateValue.Shards` the count on the first row (the stream rule), `handoverPackets` draining each as before. `Steer(corenet.SteerCPU)` attaches `SO_ATTACH_REUSEPORT_CBPF` (`cpu mod n`, three instructions) to the first socket once all are bound; `reuseport-cbpf`, class A-code on the datagram side, refused — EPERM under seccomp, EINVAL before 4.5 — leaves the hash and the row absent, `KITSUNIUM_NET_DISABLE=reuseport-cbpf` keeps the hash and the row disabled. Default stays 1: the 1.8× gate on the veth lane decides before `Shards(0)` becomes the datagram default (a `net:default-change`). `shards_linux_internal_test.go`: `TestShards_deliversExactlyOnce` runs 100 000 datagrams of 1 200 bytes from 64 source ports, each echoed byte-exact and read once, on `Shards(4)`, on `Shards(1)` and on a group without the option, the base whose one socket `Shards(1)` is (`TestShards_defaultOneSocket`) |
| `rxq.go`, `rxq_linux.go`, `rxq_other.go` | #973, ADR 0189 amended: `udp-rxq-ovfl`, class A-kernel. `probeKernelDrops` asks each bound datagram socket for SO_RXQ_OVFL unless the operator switched it off, `multiReader.countDrops` adds what the kernel's count grew by to `DatagramStateValue.KernelDrops` (modular, the kernel's counter is 32 bits), `datagramCapabilities` is the rows: this one, `udp-pktinfo`'s (`probeDestination`) and `reuseport-cbpf`'s (`steerCapability`). Under GRO a coalesced train is one drop. Linux only; elsewhere the row reads absent |
| `spill_body_internal_test.go` | #844, on every backend: a Content-Length past the spill answers 413 on its head, an explicit `Body` past the spill fails Start naming `body` (`spillRefusal`, `corenet.LoopSpillChecker`), and a body reader takes 1 MiB on the default spill |
| `backlog.go`, `backlog_unix.go`, `backlog_other.go`, `somaxconn_{linux,bsd,other}.go` | #978, ADR 0187 amended: the `Backlog` option — the depth of a listener's accept queue. `listen` binds through the standard library, then `applyBacklog` calls listen(2) again on the bound socket (`setBacklog`: `syscall.Listen` through `RawConn.Control`, every Unix; a second call sets the backlog anew, so the stdlib's own listener — `File`, `SyscallConn`, the drain's `ownListeners` — is kept as it is), clamps what it reports to the host's somaxconn (`hostSomaxconn`: `/proc/sys/net/core/somaxconn`, `kern.ipc.somaxconn` or `kern.somaxconn`; unknown elsewhere) and says why when the clamp bit (`bindReport.backlog`, `backlogReason`: `ListenerStateValue.Backlog`, `Degraded`); zero keeps the host's, reported (`defaultBacklog`, capped at the stdlib's 65 535). Windows keeps SOMAXCONN_HINT's and says so. Both engines honour it (`loopHonours`); a datagram group refuses it (`PacketOptionUnsupported`), a negative value fails Start (`valueRefusal`: `OptionValueInvalid`). The loop's accepted sockets share the listener's queue. `socketSpec` is what `listen` is given beyond the address. `backlog_external_test.go`, `backlog_linux_internal_test.go` (a listener nobody accepts from, `Backlog(1)`, completes at most two connects: Linux queues backlog + 1), `backlog_group_linux_internal_test.go` (the group's level, on every engine: the kernel's maximum read back with TCP_INFO's `tcpi_sacked`; a `Backlog(1)` group with its accept path held — the goroutine engine in `Admit` under the `Admission`'s mutex, a loop at its batch end — completes at most two connects past what the engine took itself, io_uring's 32 armed accepts and the portable lane's 512-event channel; `Backlog(0)` and no option are the host's somaxconn; a `Backlog` past it is `Degraded` in State; the ADR 0196 drain with `Backlog(8)` and 384 peers dialled while the loop is held serves every connection the kernel queued on io_uring and epoll (`drainSmallQueue`): past eight SYN_RECV requests the kernel answers with SYN cookies, a final ACK that finds the queue full is dropped while the peer believes itself connected, and its request, retransmitted only on its own RTO, can come after the grace — such a peer, refused or reset once `Shutdown` began with its line never read, is counted lost, not failed, and the group must open and close exactly the peers it served (#1353; `TestBacklog_drainLossIsTheKernels` makes the loss with a 300 ms grace); the portable backend's acceptor drains the queue into its lane, so the held queue never fills there) |
| `listen.go` | listener construction; TLS/mTLS wrapping (the group's `serverTLS`); family validation; the `bindReport` (exclusive, shared, bind wait) |
| `multipath.go`, `multipath_{linux,other}.go` | #969, ADR 0189 amended 2026-11-13 (the owner's decision): `plainTCPListens` — a loop group that opts into `uring-send-zc`, the row not switched off, on a plan that may run io_uring (auto, io_uring), binds with `SetMultipathTCP(false)` (`socketSpec.plainTCP`), because a socket accepted from Go's default MPTCP listener refuses `SEND_ZC`; every other group keeps Go's default. `multipathOf` reads `SO_PROTOCOL` back (`IPPROTO_MPTCP`, Linux; false elsewhere) into `ListenerStateValue.MultipathTCP`, bound or adopted. Tests: `TestPlainTCPListens` (every platform), `TestMultipathTCP_sendZCGroupListensPlain` (Linux: the opted-in group false, a plain loop group, a goroutine group, a switched-off and an epoll-capped one as Go's default reads here) |
| `share.go`, `listen_reuse.go` | #1330, ADR 0029 amended 2026-11-12: `reuseMode` (`socketSpec.reuse`: none, the group's own shards, or shared with other processes — `reuseOf`); `probeAddress` — the plain bind, closed at once, before an address's SO_REUSEPORT sockets (`mustProbe`); `bindRefusal` — `AddressInUse` for a bind refused with EADDRINUSE (`bindConflict`, per platform in `exclusive_{windows,other}.go`), `ListenFailed` otherwise; `shareRefusal`/`shareRefusalFor` — where `ShareAddress` cannot apply, `packetReuseRefusal` a datagram group's (unixgram `Shards`, then `ShareAddress`). `share_internal_test.go` (a free address left free is checked by a serial test: beside the parallel ones, another socket took the freed port in the window, #1404), `share_linux_internal_test.go`, `share_external_test.go` (two processes on one address) |
| `exclusive.go`, `exclusive_{windows,other}.go` | ADR 0193: `listenControl` — `SO_REUSEPORT` when sharded, `SO_EXCLUSIVEADDRUSE` on Windows for unicast TCP/UDP (`exclusiveApplies`, multicast and AF_UNIX excluded) before bind; `exclusiveOf` reads it back from an adopted socket, never fatal; `bindRetrying` — Windows rebinds a `WSAEADDRINUSE` TCP address eight times at most, 25 ms doubling to 1 s, the wait in `BindWait` (UDP, other platforms and a port the server already holds — `Server.rebindAttempts`/`holdsPort` — one attempt) — through `bindRetryingAfter`, which never binds again once the caller's ctx ended, even when the wait's timer was ready at the same select (#1352: a NetBSD guest that ran the goroutine late took the timer and bound a second time), nor once its deadline passed, its cancel run or not (`rebindWait` reads `ctx.Deadline`: on illumos the cancel's goroutine had not run 15 ms past the deadline); `closeConn` — Windows' graceful close, `CloseWrite` then a read to zero bounded by 500 ms and 64 KiB |
| `blackhole_linux_internal_test.go` | #971's blackhole, root and the veth pair of `scripts/ci/net-veth.sh` only (they skip elsewhere; their lane is `net-kernel-gate.yml`'s bench job, after the rounds): the client's socket made in the pair's far namespace (`dialFromFar`: a locked thread `setns`, never unlocked), 1 MiB queued, the far leg set down — the connection fails within 3 s of a 1 s `UserTimeout` on io_uring direct and on process descriptors, epoll, portable and the goroutine engine, and stays open 60 s with `tcp-user-timeout` switched off (the control) |
| `exclusive_internal_test.go`, `exclusive_windows_internal_test.go` | the retry and the applicability rule everywhere; on Windows both sockets exclusive, a second bind refused, a plain socket read as not exclusive, a restart on the same address after three echoed connections |
| `conn.go` | the pooled `corenet.Conn` implementation, the `hijacked` hand-over flag, `refused` (the ceiling turned it away: closed at once), and `closeGracefully`, the engine's own close (ADR 0193) |
| `simple.go` | the simple API (ADR 0197): `Server.HTTP`/`TCP`/`UDP` — `simpleGroup` (listen + `defaultName` `network+"+"+addr`, which `Name` replaces), `mountHTTPEntry`/`mountTCPEntry` (`pickEngine` by the handler's interfaces and `Engine`, `pickRefusal`: `HandlerAmbiguous`, `EngineUnsupported`, `HandlerUnsupported`), `isNil` (typed nil), `servesHTTP` + `plaintextRefusal` (`PlaintextRequired` for every HTTP group without TLS); `packetHonours` (in `server.go`) refuses a datagram group's other options with `PacketOptionUnsupported` |
| `simple_external_test.go` | each entry's engine choice and default name, `Name` replacing it, a datagram served, and every refusal: nil, typed nil, both interfaces, wrong `Engine`, wrong type, HTTP on TCP, no `Plaintext`, `Plaintext` with TLS, datagram options |
| `loop_tls.go` | ADR 0198: the http1 codec with TLS — `tlsCodec` (the handler `Unwrap`s to an `HTTP1Handler`, TLS given), `loopTLSRefusal`/`tlsCodecRefusal` (`LoopTLSUnsupported`: a raw loop handler, a zero identity, a native backend, `UseLoop`), `tlsCodecHonours` (TLS, `HandshakeTimeout`, `Protocols`), `codecProtocols` (a `Protocols` list judged by `checkProtocols`, then `h2` refused: `ProtocolsRefused` unless it is `http/1.1` alone), `loopLayers` (listeners in TLS, ALPN `http/1.1` alone; the identity's `NextProtos` judged by `offered` — `ProtocolsRefused` when they name `h2` or nothing served, ADR 0195), `tlsFloor` (`loopLayers` floors `codecTLS`: `MinVersion` raised to TLS 1.2 when lower, zero included; `serverTLS` floors the goroutine engine's listeners the same way), `loopBackend` (portable), `handshake` — the portable acceptor's `negotiate` runs it on a goroutine per connection before the lane sees it; #964: the h2 adapter (`multiVersion`, a `LoopHTTPCodec`) offers `codecVersions` — Protocols, or h2 then http/1.1 — narrowed by the identity, and `codecServes` hands the adapter the offered list, so the preface serves nothing ALPN could not negotiate; Protocols naming h2 is refused for the http1 adapter alone |
| `loop_tls_internal_test.go` | the codec over TLS on two portable lanes, ALPN `http/1.1` alone, a silent peer closed at `HandshakeTimeout` and never counted, P2's N×N-token conformance over portable+TLS, each refusal, `tlsFloor` (a TLS 1.0 configuration floored refuses a TLS 1.1 client it otherwise accepts) |
| `loop_tls_alpn_external_test.go` | the codec over TLS against its identity's `NextProtos`: `h2` alone, `h2` beside `http/1.1` and a foreign protocol refused with `ProtocolsRefused`; `http/1.1` among others served, `http/1.1` negotiated; `Protocols("http/1.1")` served, `Protocols` naming `h2` (alone or beside `http/1.1`), an unknown version or nothing refused with `ProtocolsRefused`; a client capped at TLS 1.1 fails the handshake, one at TLS 1.2 completes it |
| `loop_tls_h2_internal_test.go` | #964: the h2 adapter over TLS — ALPN `[h2 http/1.1]` in State, a Go h2 client and an HTTP/1.1-only one through one Routes+Bearer chain on two lanes; `Protocols("h2")` (crypto/tls's http/1.1 fallback then 400); epoll and io_uring refused naming Loop; a stalled partial preface closed by IdleTimeout |
| `http_counts_external_test.go`, `http_counts_internal_test.go`, `loop_http2_counts_internal_test.go` | #966: requests per version and HTTP/2 errors in State on net/http's engine (TLS group, ALPN in State) and on every loop backend; the classification of net/http's CountError names |
| `pool.go` | per-connection recycling + the live-socket registry |
| `options.go` | `Option` / `GroupOption` — including the three HTTP-only group options, `ReadHeaderTimeout`, `MaxHeaderBytes` and `MaxBodyBytes` (#845), `Loop`, refused by every group no loop handler serves, and `Plaintext`, `Name` and `Engine` (ADR 0187); each records its kind; `WithDrainGrace` (ADR 0196) |
| `packet_group.go` | `PacketGroup` — the datagram mirror of `StreamGroup` |
| `packet.go` | the pooled `corenet.Packet` implementation; `Reply` goes through the socket's `datagramSink` |
| `packet_listen.go` | datagram socket construction (exclusive like a listener, bound once, ADR 0193); family validation; ceilings; `boundPacketConn` (its source, sink and `done`), `handoverSource` |
| `packet_bound.go` | `newBoundPacket` (source with GRO, sink per `BatchReplies`), `handoverPackets` (Shutdown's drain wait, after the stream listeners are closed and within `handoverBudget`, 250 ms, a drain still running then has GRO switched off — ADR 0192 §D4), `stopGROPackets` (Close's), `recordGSOOff` / `gsoFaults` (the `gso` faults of `State`) |
| `packet_lanes.go`, `packet_lanes_{linux,other}.go`, `packet_loop_lane.go` | a datagram group `HandleLoop` serves (#852): `packetLoopRefusal` (a second handler `HandlerAlreadySet`, `Use` and a `unixgram` address `LoopOptionUnsupported`), `startReader` gathering the lanes, `startPacketLanes` (`LoopStarter.OnStart(lanes)`, then each lane on io_uring — `startPacketLane`: a `F_DUPFD_CLOEXEC` descriptor to `loop.StartPacket`, server's counters handed over — unless the environment rules it out, `laneAllowed`), `awaitLane`, `laneCapability` (the `uring-multishot-recvmsg` row of `Datagrams.Capabilities`: on, absent with the first stage refused, unprobed off Linux, disabled), and the epoll lane: `readLoop` with a nil handler, `dispatchLoop` (`OnPacket` a datagram, `OnBatchEnd` once a batch, a panic contained), `packetLoopLane.Reply` through the batch's sink — always batched for a loop group —, from the destination its peer reached in the batch (`sourceFor`, `replyFrom`; #972 joined to #852, ADR 0192 amended 2026-11-11), the io_uring lane doing the same in `loop`. `pktinfo_loop_linux_internal_test.go` (a reply leaves from 127.0.0.2 on both lanes, from `OnPacket` and from `OnBatchEnd`; a reply to an unseen peer leaves from the kernel's choice). `Server.startLane` is the seam a test refuses the probe through. `packet_lanes_internal_test.go` (a refused probe serves 1 000 datagrams once on the epoll lane, the row absent, no fault; the operator's switch; `Shards(3)`'s lanes; the refusals), `packet_lanes_linux_internal_test.go` (on io_uring: the row on, 2 000 datagrams once through Shutdown's handover; Close frees the port) |
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
| `http_adapter.go` | the `net/http` adapter (ADR 0029 D3); its `http.Server.ErrorLog` is the server's (`errorLog`, set at bind) |
| `http_counts.go` | #966: `httpCounts` (requests per version, one atomic add a request through `versionCounter`; HTTP/2 errors from `HTTP2Config.CountError`, classified by `netHTTPErrorType` into `corehttp2.ErrorType` — the scope and the RFC code's name, the frame layer's `frame_` names by their parser's code, `conn_close_lost_ping`, anything else `other`); `findHTTPCounter` (a loop chain's `corenet.LoopHTTPCounter`); `foldHTTPCounts`, State's `HTTP1Requests`, `HTTP2Requests` and `HTTP2Errors` on every row of the group. net/http answers a header list past `MaxHeaderBytes` 431 without CountError, and a frame past its bound with GOAWAY and none: neither is counted on that engine |
| `error_log.go` | net/http's own log through the server's logger (#846, ADR 0030 amended): `WithLogger` (`Option`; nil is none), `errorLogWriter` — the `io.Writer` behind each HTTP group's `http.Server.ErrorLog`, one warn record a line with `source=net/http`, `group` and, for the lines after the first (a recovered panic's stack), `detail` clipped at `errorLogDetailMax` —, `errorLogBudget` — `errorLogCap`, ten records per `errorLogWindow` second, shared by every group, on the server's clock; the lines over it, and every line when no logger was given, are counted in `StateValue.SuppressedLogs`, never written to stderr. The engine handshakes TLS itself before net/http sees the connection, so net/http's own `TLS handshake error` line is the engine's to write (ADR 0030 amended 2026-11-13): `handshakeLog` gives a TLS group a writer — `source=net/http` on an HTTP group, `net/server` on any other —, `handshakeFailed` writes net/http's line (without `http: ` off an HTTP group) under the same budget, skipping a negotiation the engine's own context cancelled; `handshakeHandler.log` and the portable codec's `portableGroup.hsLog` call it. `error_log_external_test.go` (the standard logger, stderr's stand-in, stays empty), `error_log_handshake_external_test.go` (a plaintext request on an HTTP, a connection and a loop codec TLS group: one record each), `error_log_internal_test.go` (10 000 lines in one instant; 10 000 real failed handshakes in one second of a frozen clock) |
| `stream_group_protocols.go` | `HTTP2` (kind `OptionHTTP2`, `http2Refusal`, `effectiveHTTP2`, State's bounds), h2c on a Plaintext group (`servesH2C`); `Protocols` (kind `OptionProtocols`) and `httpProtocols` (ADR 0195): `protocolsRefusal` computes, once at `Start`, the versions an HTTP group serves and the ALPN list its TLS listeners offer (`offered`: the identity's `NextProtos` the group serves, in the identity's order), refusing with `ProtocolsRefused`; `serverTLS` — a listener's `*tls.Config`; `http2Config` — HTTP/2's bounds, set: 100 streams, 16 KiB frames, 4 KiB HPACK tables, 256 KiB a stream's window and 1 MiB the connection's, and `SendPingTimeout`/`PingTimeout` from `h2Silence` (header bound, else idle) |
| `loop_http_codec.go` | the multi-version loop adapter (ADR 0221): `findHTTPCodec`, `codecListRefusal` (Protocols judged, `SetHTTP` before the spill check), `applyHTTPCodec` (the operator's `http2-loop`, `loopBusy` = `InFlight`), `httpCodecEngine` (the drain's announcement, State's `http2-loop` row) |
| `loop_http2_internal_test.go`, `loop_http2_idle_linux_internal_test.go` | HTTP/2 on the loop on every backend: one chain for both versions, Pending and reset, Protocols and the switch, the drain, the defences, 100 streams and windows, a body past the stream window, the idle cost against HTTP/1.1's — the median of three settled readings of each, `TestIdleHeapPerConn_release` reproducing #1354's low reading |
| `protocols_h2c_external_test.go`, `protocols_http2bounds_external_test.go` | h2c by prior knowledge on a Plaintext group (#861); `HTTP2` (#965): the window raised against latency (the latency's share of the upload at least 8x smaller, the same upload with no latency subtracted from both: on linux/386 TLS alone is 1.4 s of it), State's bounds, the refusals |
| `protocols_{external,internal}_test.go` | ALPN h1 / h2 / both and the identity's order, each refusal, the adapter's `Protocols`/`HTTP2` and no identity in its `TLSConfig`, an unfinished HEADERS block closed by the idle or read bound, one behind an open stream closed by the PING bound, an unread stream held to its window while another stream on the connection sends 768 KiB |
| `http_bounds.go` | `httpBounds` — the header phase's own deadline and the header size cap, kept out of `core/net`'s protocol-neutral `Timeouts`/`Limits`; each unset value keeps the adapter's earlier behaviour (the read timeout for the header phase, net/http's 1 MiB cap); `idleOrRead`, the wait between requests; the request-body bound (#845): `bodyBytes` (zero selects `defaultMaxBodyBytes`, 1 MiB, the http1 profile's and the framework's; `noBodyBound`, -1, states none), `limitBody` — a `Content-Length` past it answered 413 with `Connection: close` on the head, before the handler runs and before a body byte is read, a body of unknown length wrapped in `http.MaxBytesReader` so the handler's read ends in `*http.MaxBytesError` (`net/http` writes no 413 of its own), a request with no body (`http.NoBody`) left unwrapped so the adapter allocates per request what `net/http` alone does (#966, `TestLimitBody_bodilessRequestKeepsNoBody`), a CONNECT left unbounded: its body is a tunnel — an RFC 8441 WebSocket's frames — whose total the bound would cut (#967, `TestXConnectBeyondTheBodyBound` in `net/websocket`) —, `bodyRefusal` (below -1 is `OptionValueInvalid`, naming the option), `reportedBodyBound` (`ListenerStateValue.MaxBodyBytes`, zero where `net/http` does not serve). A loop group refuses the option (`LoopOptionUnsupported`: its bound is `HTTP1LimitsValue.Body`), a datagram group `PacketOptionUnsupported`. The framework's engine states `-1`: each endpoint bounds its own body (`app.MaxBody`). `max_body_default_external_test.go`, `max_body_external_test.go` |
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
| `load_gate_test.go` | Linux and darwin (`load_gate_hooks_*_test.go`). `unsharded` (and `unshardedEnv` in `load_l3_linux_internal_test.go`): the children a test re-runs by name drop Bazel's shard variables, `server_test` running in eight shards since the suite outgrew `.bazelrc`'s 60 s (four until the L2 follow-up's suites); the network merge gate's L1 and L2 (plan P8, `tools/netgate`): paired (ADR 0190): `BenchmarkLoadL1/pair=goroutine:loop` (P6, active: L1's loop engine on `LoopsAuto`, ADR 0196 §D5) and `pair=loop:loop` (the calibration), `BenchmarkLoadL2/pair=handwritten:chain` (P2: the same auth and routing through `http1.Routes` and `http1.Bearer`, `chainL2`) and `pair=handwritten:handwritten` (its calibration) — one child (`TestLoadChild`, this binary re-run, `KITSUNIUM_NETGATE_CHILD=L2:a,b`) serves both engines side by side, 1000 kept-alive connections each, a 1 KiB body, and a round sends `b.N` requests in 2×`pairedWindows` windows, A, B, B, A, …; the metric is ITS CPU (getrusage at each window's ends), reported as `a:cpu-ns/req`, `b:cpu-ns/req` and `ratio:cpu-ns/req`, one paired sample a round; `TestLoadL1L2_wired` serves each engine for three requests, `TestLoadPaired_wired` two side by side. `startLoadChildIn` runs the child under a prefix and on an address of the caller's (`KITSUNIUM_NETGATE_BIND`, loopback when unset), the variables passed through `env` since sudo and `ip netns exec` reset them; `benchPairedSized` is a round whose answers are larger, `b.N/scale` requests |
| `load_gate_hooks_linux_test.go`, `load_gate_hooks_darwin_test.go` | the loads a platform has of its own, behind `serveLoadOS`, `loadGroupOptionOS`, `loadChildEnvOS`, `reportLoadRows` and `loadL1Pairs`: on Linux L1big and Sparse, L1's pairs `goroutine:loop` and `loop:loop`; on darwin L1's pairs `goroutine:loop-kqueue` and `loop-kqueue:loop-kqueue` (#976, the loop on kqueue named, `auto` not choosing it yet), run by net-kernel-gate's `bench-darwin` job on macos-15 and judged by `netgate -only kqueue,cal-L1-kqueue`, and `TestLoadL1Kqueue_wired` |
| `load_big_linux_test.go` | L1big: L1's request answered with 64 KiB (`loadBigBodySize`), a round an eighth of L1's requests (`loadBigScale`, `benchPairedSized`): `BenchmarkLoadL1big/pair=loop:loop-sendzc` (#969: the same in-memory body sent by `IORING_OP_SEND` and by `IORING_OP_SEND_ZC`) and `pair=goroutine-static:loop-files` (#982: net/static on the goroutine engine against `http1.Files` on the loop, one file `assets/big.bin` in the child's tree, `Outbound` as deep as the load's connections so no request is refused 503). The child listens with plain TCP (`bigChildEnv`, `GODEBUG=multipathtcp=0`): `loop-sendzc` binds plain TCP itself (`plainTCPListens`, #969), and the variable gives its partner `loop` the same transport, so the pair differs by the send alone. `reportRows` writes `uring-send-zc`'s and `busy-poll`'s rows on the child's stderr. `TestLoadL1big_wired` |
| `load_sparse_veth_linux_test.go` | Sparse (#979): the sparse keep-alive load paired over the veth pair — the child on its far end through `KITSUNIUM_NETGATE_L3_EXEC` and `KITSUNIUM_NETGATE_BIND` (`startLoadChildIn`), GRO switched on there by `net-veth.sh gro` so the veth's receive side has a NAPI instance —, `BenchmarkLoadSparse/pair=loop:loop-busypoll`: 1 000 connections an engine, 4 000 requests a round at 1 000 a second, each engine's CPU a request and the 99th percentile of a request's round trip (`percentile99`, by rank), and their ratios; skipped without the pair. `TestLoadSparsePaired_wired` (loopback) and `TestPercentile99` |
| `load_gate_h2_test.go` | #963: L1h2, L1 over h2c by prior knowledge — `h2LoadConn` writes pre-encoded HEADERS eight streams at a time, counts END_STREAM and credits the connection window, so the client's cost stays flat; `h2Side` beside L1's `loadSide` behind `pairSide`; `BenchmarkLoadL1h2` (`pair=goroutine-h2:loop-h2`, `loop-h2:loop-h2`, `loop:loop-h2`) and `TestLoadL1h2_wired` |
| `load_l3_linux_internal_test.go` | L3, paired like L1 and L2 (ADR 0190): `BenchmarkLoadL3/pair=recvmmsg:recvmmsg` (P4a's calibration) and `pair=recvmmsg:gro` (P4a's gate: GRO off, engine A; GRO on, engine B; `l3Sources`), one receiver (`TestLoadL3Child`, this binary re-run in the namespace) reading both engines' sockets, each through its own source, slots of `slotSize` and a coalesced message counted by `count`, windows A, B, B, A, … (`l3Windows`, b.N datagrams an engine) over the veth pair of `scripts/ci/net-veth.sh`, 1200-byte datagrams from 4 GSO flows (`UDP_SEGMENT`, 54 segments a send) at 200 000 a second; every window metered by one function (`l3Meter`, `l3Round`): `cpu-ns/datagram` — `l3CPU`: the receiver's rusage, the sender's and /proc/stat's irq and softirq time, over the datagrams sent, its parts reported as `rcv-`, `snd-`, `irq-cpu-ns/datagram` (ADR 0192 §D6) —, `datagrams/syscall`, `lost/op` and `kernel-drops/op` (the socket's SO_RXQ_OVFL count, #973, which cannot pass the loss: a check, no threshold), `a:`, `b:` and `ratio:`; a window below the receiver's own CPU or a message cut short fails the round (`TestL3CPU_check`, `TestL3Round_symmetric`) |
| `load_l3_uring_linux_internal_test.go` | #852: L3's engine `uring-recvmsg`, the io_uring datagram lane with GRO on (`listenL3URing`, `l3Lane` counting on the lane's thread and adding once a batch, its receives the batches), the pair `recvmmsg:uring-recvmsg` — reported, gated once P4a's 0.75 holds —, and `TestLoadL3_wiredURing` (one GSO send over loopback counted whole, none cut; skips where the lane is refused) |
| `load_sparse_linux_test.go` | a sparse keep-alive load, not a gate (ADR 0202): `BenchmarkSparseKeepAlive/wait=` default, off, 1ms and 4ms, sends a fixed 1,000 requests a second over 10,000 connections, each to one chosen at random among those not waiting, through L2's chain on one loop at `GOMAXPROCS=2`; the child (`TestLoadSparseChild`) reports its CPU, context switches, loop batches and completions, and its busiest thread's time on CPU and wakeups; `KITSUNIUM_SPARSE_CPU` pins it with taskset, `KITSUNIUM_SPARSE_PROFILE` writes its CPU profile; `TestLoadSparse_wired` keeps it serving |
| `multireader_gro_linux_internal_test.go` | GRO on the reader: a burst split, a shorter last segment, the segment size after a plain datagram (capacities restored), `MSG_CTRUNC` cut, a caller's `IP_PKTINFO` with GRO off (no control room, never cut) and on (room beside `UDP_GRO`), `slotSize`, the handover drain (split, then GRO off, then `net.ErrClosed`), `stopGRO`, `groSegment`'s walk |
| `multiwriter_linux_internal_test.go` | `gsoSegments` (54 at 1200 on both families), the flush over loopback at the cap (54 in one message, 55 in two, a shorter last, past an Ethernet frame alone), a reply outside a batch, a scripted `sendmmsg` for `EIO`/`EINVAL`/`EMSGSIZE` and a partial count, `EINVAL` remembered across batches (`segmentCeiling`, GSO off after `gsoInvalidLimit` in a row), `sendmmsg` refused (`ENOSYS`/`EPERM`: `WriteTo` each, kept off when one leaves), `encode` and `pendingReply.addr` |
| `reply_queue_internal_test.go`, `packet_bound_internal_test.go`, `batch_replies_external_test.go` | the queued sink (copy, order, failures counted, full queue), the handover wait and its budget, the `gso` faults, the sink a group's option picks, `BatchReplies` end to end and refused by a stream group |
| `loop_portable_peer_{linux,other}.go` | `portablePeer` (#974, ADR 0148): the credentials of a Unix connection's peer, `SO_PEERCRED` once at open and kept on the `portableConn` (reading costs an allocation, `Peer` only returns them); elsewhere the zero value, so `LoopPeer` answers false: nothing the standard library alone can verify |
| `{batch,conn,linux,loop_portable}_compliance.go` | compile-time interface assertions (`KTN-IFACE-ASSERT-PLACEMENT`) |

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
- **The portable backend counts as the loop does** (#975): `bytesIn` and
  `bytesOut`, `outputRefused`, `spillClosed`, `timeouts`, `idleClosed` and the
  `pendingOutput` gauge are atomics of each lane, fed where the events happen
  (`deliver`, the writer's flights, `Write`'s refusal, `timer`), read by
  `portableEngine.State`. The gauge is written by Write and taken off per flight
  and, when the writer ends, for what it left behind; `Slab*` are zero there,
  its buffers living on the Go heap.
- **`conn` forwards `ReadFrom` and `WriteTo` to the socket's own** (#970): an
  `io.Copy` with a file or another socket reaches sendfile or splice, and
  `(*conn).Unwrap` (`corenet.ConnUnwrapper`) gives the socket under the engine's
  layers to `net.Buffers.WriteTo`, so writev too. A bound is per OPERATION
  (`TimeoutsValue`): one deadline over the whole copy, as the audit suggested,
  would be the lifetime budget `conn`'s comment refuses, and would end a long
  copy to a healthy peer. So a copy INTO a connection whose group bounds writes
  goes by the socket's own copy only from a source the kernel sends itself —
  `kernelSource`: a `syscall.Conn`, a file or a socket, alone or under an
  `io.LimitedReader`, which is what the standard library's sendfile and splice
  take — one `zeroCopyStep` (1 MiB) at a time under an `io.LimitedReader`, the
  write deadline installed before each step (`boundedReadFrom`; a step short
  with no error is the source's end); any other source, and a copy OUT of a
  connection whose group bounds reads, takes the generic copy. `connWriter` and
  `connReader` hide the methods, so that copy reaches `Write` and `Read` and
  cannot come back in. A direction no bound governs installs no deadline. The
  copy allocates a constant of the call, not of its bytes: Go 1.27's own
  `io.Copy` to a bare `*net.TCPConn` allocates too (`os.File.WriteTo`,
  `File.SyscallConn` and its `Read` closure), the floor `BenchmarkCopyFile/
  bare_TCPConn` measures; a bounded copy pays the standard library's sendfile
  set-up once a step.
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
  differs from a declared name, records `GroupNameConflict`. Every HTTP group
  without TLS must give `Plaintext` (`PlaintextRequired`, ADR 0197), and a
  datagram group refuses every option it does not honour
  (`PacketOptionUnsupported`). `MaxConns(0)` keeps "no ceiling": zero means the default for
  options added from ADR 0187 on only.
- **A loop group is a group like the others, served by another engine** (ADR
  0186 §D1, §D3). It binds and adopts through the same `listen` and
  `adoptStream`, without TLS and without sharding (an adopted socket stays one listener; `Shards(0)` binds one `SO_REUSEPORT` listener per loop for an address, `loop_shards.go`, #856), and its listeners join
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
- **`LoopsAuto` is read once** (ADR 0196 §D1). `bindLoopGroup` resolves (`loop.ResolveLoops`)
  `group.loop.Loops` before `OnStart` and keeps the snapshot, with its source,
  for every backend and `State`: since Go 1.25 `GOMAXPROCS` follows the CPU
  limit, and two reads could size the chain and the engine apart.
- **Readiness turns false a grace before anything closes** (ADR 0196 §D2).
  `Shutdown` stores `PhaseDraining`, which `readinessOf` refuses, then
  `waitGrace` keeps every listener accepting and every connection served for
  `WithDrainGrace`, or until ctx ends; `Serve` adds the grace to its drain
  budget. A loop group then takes its listen queue before its listener closes
  (the leaf's drain), and so does the portable backend on Unix, on the
  listeners it duplicated at Start (`ownListeners`, `takeQueues`), its
  acceptors admitting until each closes and a lane stopping only once none
  runs; a connection that has sent nothing closes once heard or after
  `portableQuiet`. The goroutine engine, and the portable backend on Windows,
  close their listener with the server's, after the grace.
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
handler slower than the bound between and after its reads. Its peer paces its
writes with its own sleeps, so it is serial — beside the package's parallel
tests, on illumos, a write reached the server more than the bound after the
read waiting for it (#1406) — and a failure dates every write and every
read's deadline.

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
- **The shards of a port 0 share the port the first drew** (#1331, ADR 0029
  amended 2026-11-11). Each bind of `host:0` draws a port of its own, so every
  engine that shards binds the first socket, then the others on its port, the
  host as asked: `withBoundPort` (`listen.go`), through `pinnedAddr` for the
  streams (`bindShards`) and a loop group (`bindLoopAddress`), `pinPort` for
  the datagrams (`bindPacketShards`). `shards_port_linux_internal_test.go`
  counts, in `/proc/net`'s tables, N sockets on the port State reports, for
  the three, on `127.0.0.1:0` and `[::1]:0`.
- **An address another process holds is refused, not shared** (#1330, ADR
  0029 amended 2026-11-12). SO_REUSEPORT on every shard used to let a second
  copy of a program start on the address a first one served and take half its
  connections, silently. Before an address's sockets are opened with
  SO_REUSEPORT, `probeAddress` (`share.go`) binds it once plainly — the
  standard library's options, no SO_REUSEPORT — and closes it at once
  (`mustProbe`: several sockets, a named port, no `ShareAddress`), on the three
  engines that shard (`bindShards`, `bindLoopAddress`, `bindPacketShards`). A
  held address fails Start with `AddressInUse` (`0.2.11.96`, the "address"
  field), and so does any bind refused with EADDRINUSE (`bindRefusal`,
  `bindConflict` per platform: `syscall.EADDRINUSE`, `WSAEADDRINUSE`), a single
  listener's included; every other refusal stays `ListenFailed`. A stream
  probe listens for the instant between bind and close, which the standard
  library does not separate. The window between the probe and the shards is
  left: a process binding with SO_REUSEPORT in it joins them. On the BSDs and
  darwin a listener on the wildcard address does not stop a bind on a precise
  one (SO_REUSEADDR), so the probe refuses there what a single listener's bind
  refuses, no more. Port 0 is not probed (#1331's one port is unchanged).
- **`ShareAddress` shares on purpose** (#1330), for a progressive restart (ADR
  0196 §D4): `reuseShared` sets SO_REUSEPORT on every socket of the
  group's addresses, one included, no probe, and `bindReport.shared` makes the
  row `Shared`. `shareRefusal` judges it at Start, before anything is bound:
  `UnsupportedPlatform` where `reusePortSupported` is false (Windows, which
  holds its addresses with SO_EXCLUSIVEADDRUSE, Solaris, illumos), then a Unix
  socket's address — `LoopOptionUnsupported` on a stream group of either
  engine, `PacketOptionUnsupported` on a datagram group. `shareRefusalFor`
  takes the platform's answer, so `Test_shareRefusalFor` runs the Windows
  branch everywhere. `share_external_test.go` starts a second process (the
  test binary as a holder, guarded by role and depth, bounded by its lifetime
  and stopped by closing its stdin): the default options refused with
  `AddressInUse` naming the address, a stream group, `Shards(1)` and a
  datagram group's `Shards(2)`; `ShareAddress` joining and serving.
  `share_linux_internal_test.go` holds a port with a SO_REUSEPORT socket and
  checks each engine refused, then joining with `ShareAddress`, and that a free
  named port still gets its N shards, counted in `/proc/net`.
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
