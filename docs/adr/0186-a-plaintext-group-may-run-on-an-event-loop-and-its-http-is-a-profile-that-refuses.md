# ADR 0186 — a plaintext group may run on an event loop, and its HTTP is a profile that refuses

- **Status**: Accepted
- **Date**: 2026-10-23
- **Accepted**: 2026-10-23, on the owner's decisions for this work: the SDK is
  enriched first and a downstream consumes its release; one generic interface
  with a standard-library floor everywhere, specialised Linux backends allowed;
  pure Go and the standard library only inside `pkg/v1`, bitwise and SWAR code
  allowed; io_uring and epoll on Linux in the first wave, kqueue and IOCP later
  in measured changes. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)): everything it adds
  is opt-in, and the default engine does not change.
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0029](0029-sdk-net-domain.md) D1 (the tree), D2 (one
  goroutine per connection, now the default rather than the only engine), D3
  (do not reimplement HTTP, now with one restricted exception) and D4 (pooling
  and the connection lifecycle, for the loop engine);
  [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md)
  §4 (the publication table gains two rows, §D10 below)
- **Related**: [ADR 0018](0018-sdk-cross-platform-portability.md) and
  [ADR 0144](0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md)
  (nine platforms, the build bar and the runtime bar, the `x/sys` ban),
  [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md)
  (standard library only), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md)
  (every code in the core, a wire format in the service),
  [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md),
  [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md)
  and [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md)
  (design first, budgets on what the compiler decides, never on absolute
  nanoseconds), [ADR 0130](0130-a-file-tree-is-served-by-name-and-a-failing-accept-waits.md)
  (the accept loop that waits), [ADR 0177](0177-the-linux-init-runtime-bar-runs-on-public-runners.md)
  (the `linux-init` lanes)

## Context

ADR 0029 D2 chose one goroutine per connection on the runtime netpoller and
rejected an event loop for three reasons: TLS is a hard requirement and
`crypto/tls` is written against a blocking `net.Conn`; the netpoller already is
an epoll, and an event loop only saves goroutine stacks past several hundred
thousand idle connections; and every I/O helper in the ecosystem speaks
`net.Conn`. D3 refused to reimplement HTTP because `net/http` carries a decade
of smuggling defences, `Expect: 100-continue`, chunked trailers, HTTP/2 and the
CVE stream.

Those reasons were about memory and about scope. A downstream service now
pushes on a third axis neither weighed: **CPU per request on one vCPU**. It
serves a small JSON API over SQLite on a 1 vCPU, 2 GB machine, with about
15 000 keep-alive connections and every request authenticated by an HS256
token. Its score is the number of users the box holds, which is set by the
microseconds of CPU each request costs. On that machine, per request, the
default engine pays a goroutine wake-up and a park per read, a `net.Conn`
read and write, `net/http`'s parse and its allocations, a token verification
that decodes its claims through `json.Unmarshal` into a map, and
`encoding/json` on the way out. Event-loop servers written in C, and in Go on
raw epoll, pay a fraction of that on the same requests.

The request is to make the SDK cost what those references cost, in pure Go and
the standard library, without touching the default engine, its TLS, its
`net/http` adapter or anything a current consumer relies on.

## Decision

### D1 — An opt-in loop engine of `net/server`, entered by one method

The domain gains a second engine for stream groups, the **loop engine**. It is
an engine of `net/server`, not a second server: `server` keeps the lifecycle
(`Start`, `Serve`, `Shutdown`, `Close`), the bind, the adoption of inherited
sockets, the admission ceiling and the drain, for loop groups as for the
others. The only entry is a method of the stream group:

```go
func (g *StreamGroup) HandleLoop(h corenet.LoopHandler) *StreamGroup
```

published, as every method of the group, through the alias `server.Group`
(ADR 0029 D1). A group served by `HandleLoop` is configured by one more group
option, `Loop(v corenet.LoopValue)`; a group served by `Handle`,
`HandleFunc` or `HandleHTTP` refuses that option, and a loop group refuses
`Use` (there is no `ConnHandler` to decorate) and every option it cannot honour,
each with `LoopOptionUnsupported`. Nothing changes for a group that does not
call `HandleLoop`.

### D2 — Plaintext only; one goroutine per connection stays the default

The loop engine speaks **plaintext TCP and Unix stream sockets only**. A loop
group given `TLS(id)` fails `Start` with `LoopTLSUnsupported`: D2's first
reason holds unchanged, and the SDK still owns no TLS stack. One goroutine
per connection on the netpoller remains the default engine and the only one
that serves TLS, mutual TLS, `net/http`, WebSocket, SSE and the static tree.
A loop handler never sees a `net.Conn`: it receives an opaque reference and
the bytes, so D2's third reason is honoured by scope rather than broken.

### D3 — The loop is a leaf below `server`, and it speaks no HTTP

- **Import direction.** `internal/service/net/loop` imports `internal/core/net`
  and the kernel, and nothing else of the SDK. It never imports `server`.
  `server` imports `loop`, and hands it listeners it has already bound or
  adopted, through one hand-off type, a duplicated descriptor. Bind, adoption,
  admission and the lifecycle stay in `server`.
- **The portable backend stays in `server`.** On a platform or a
  configuration without a native backend, a loop group runs on the default
  engine's accept path and goroutines, adapted to the loop contract and
  serialised onto one handler thread. It needs the netpoller and `net.Conn`,
  so it belongs to `server`, not to the leaf.
- **No HTTP vocabulary in the engine.** The loop knows bytes, references,
  deadlines, write pressure and the order of writes. A "request", a status, a
  header, `Connection: close` or `ReadHeaderTimeout` are `http1`'s, which
  projects them onto those primitives. The loop raises a primitive *soft
  pressure* flag on a reference from the accept on; `http1` turns it into
  `Connection: close`.

### D4 — The contract, declared in `design/net.yaml` before any code

The ports and values are declared in `internal/core/net` through
`design/net.yaml` (ADR 0163), and every sentinel is in the `0.2.11.*` block
(ADR 0029 D8, ADR 0160 §2). What a second implementation must honour:

- **`LoopConnRef`**, a comparable value with no behaviour, naming a
  connection by ring, slot and generation. An operation on a stale generation
  does nothing and returns `LoopStaleRef`; a slot and its memory are reused
  only once every operation of the previous generation has completed.
- **`LoopIO`**, the port each loop implements, with an `allocs: 0` budget
  (ADR 0165) when the bytes belong to the caller's heap or mapping:
  `Write(ref, p)` copies into the connection's output and is refused with
  `LoopOutputFull` at its ceiling; `Close(ref)`; `SetDeadline(ref, d)` on a
  coarse per-loop timer wheel; `Pressure()`; and `Post(op PostFunc, arg
  uintptr)`, the one call another goroutine may make, through a preallocated
  queue and no closure, each posted operation run exactly once on the loop,
  refused with `LoopPostQueueFull` when the queue is full. `Write`, `Close` and
  `SetDeadline` belong to the loop's thread; under the `loopdebug` build tag a
  call from another thread is refused with `LoopWrongThread`.
- **`LoopHandler`**, the port the consumer implements: `OnOpen(io, ref)`,
  `OnData(io, ref, buf) int` (the bytes consumed), `OnTimeout(io, ref)`,
  `OnWritable(io, ref)` (below a low-water mark of the output),
  `OnBatchEnd(io)` (once per loop iteration, every ring included) and
  `OnClose(io, ref, err)`. `buf` stays valid until `OnBatchEnd` returns; the
  unconsumed tail is kept in a per-connection spill, bounded, refused with
  `LoopSpillFull` at its ceiling. An optional capability,
  `Honours(OptionKind) bool`, lets `server` refuse an option the handler does
  not declare.
- Writes on one reference leave in the order they were made. Every sentinel
  the engine returns is preallocated.

The loop honours `IdleTimeout`, `MaxConns` (refusing with `ConnLimitReached`,
the meaning `LimitsValue` publishes), `Adopt` and the drain itself; it does not
read `ReadHeaderTimeout`, which only the `http1` adapter honours.

### D5 — Backends: io_uring and epoll on Linux, portable everywhere else

| Backend | Where | Wave |
|---|---|---|
| io_uring | `linux && (amd64 \|\| arm64 \|\| riscv64 \|\| loong64)`, after a staged probe of the running kernel | 1 |
| epoll | every Linux architecture, and every io_uring probe failure | 1 |
| portable | every other GOOS, and `Backend: portable` anywhere | 1 |
| kqueue | darwin and the BSDs | deferred |
| IOCP | windows | deferred |

`LoopValue.Backend` is `auto` (the zero), `iouring`, `epoll` or `portable`.
`auto` takes the best backend the probe reaches and reports the fallback and
its reason in `State()`, never silently — the `batchDegradation` precedent of
ADR 0029 D5 — and, as there, a different mechanic for the same capability is
not `UnsupportedPlatform`. An explicit backend the platform cannot run is
refused at `Start`: `UnsupportedPlatform` (`0.2.6.1`, ADR 0018 §(a)) where the
GOOS has no such mechanic, `LoopBackendUnavailable` where the kernel's probe
refused it. Every syscall, constant and structure layout is the standard
library's `syscall` or declared in-tree and cited (ADR 0018, the `x/sys` ban);
each package keeps an `_other.go` floor so the nine-platform build bar stays
green.

**Capacity beyond the descriptor limit is io_uring's alone.** io_uring
accepts into direct descriptors held in a per-ring table, which the process's
descriptor table does not count, so a loop may serve more connections than
`RLIMIT_NOFILE` by sharding them across rings, each table no larger than
min(`RLIMIT_NOFILE`, `IORING_MAX_FIXED_FILES`). epoll and the portable backend
need one process descriptor per connection and stay below `RLIMIT_NOFILE`;
past it they refuse and back off as ADR 0130 does. The documentation says so;
no backend pretends otherwise.

`LoopValue` carries `Backend`, `Loops` (locked OS threads, one by default,
chosen by the consumer), `Rings` (descriptor shards inside one loop), and the
batch, spill, output, soft-connection and idle-eviction bounds. `Rings` has one
source: the explicit value, otherwise ⌈max(`MaxConns`, `SoftConns`) /
min(`RLIMIT_NOFILE`, `IORING_MAX_FIXED_FILES`)⌉, and 1 when both are zero or
less. The SDK calls neither `setrlimit` (the Go runtime already raises the soft
limit to the hard one) nor `GOMAXPROCS`, which is the consumer's policy;
`State()` reports both values, and where `Rings` came from.

### D6 — The restricted HTTP/1.1 profile, `http1`

D3 stands for the default engine: `HandleHTTP` mounts `net/http`. The loop
gains one exception, `internal/service/net/http1`, a codec (parse and append on
`[]byte`) and an adapter onto `LoopHandler`, published as `pkg/v1/net/http1`.
It is a **profile**, not an HTTP server, and its scope is fixed by what it
refuses:

- **Refused, answered and closed, never handed to the handler:**
  - any `Transfer-Encoding`, chunked included, and therefore trailers —
    `400` and close;
  - `Expect: 100-continue` — `417` and close;
  - a bare CR or LF, a NUL, CR or LF in a value, obs-fold, a space before
    `:`, a name outside `tchar`;
  - a `Content-Length` that is not a number, too long, or present twice with
    different values; a second `Authorization`, `Host` or `Content-Length`;
  - a head or a body past the profile's size bounds.
- **Framing depends on neither the method nor the route.** The bytes a request
  consumes are its head plus its `Content-Length`, computed by the codec. An
  early answer (`401`, `404`, `400`) drains the declared body or closes;
  pipelined requests are framed the same way.
- Out of the profile: HTTP/2, `CONNECT`, `Upgrade`, TLS. A program that needs
  any of them uses `HandleHTTP`.

The profile's request value and handler port are declared in `core/net`, under
names that do not collide with the outbound `RequestValue` the `Policy` port
already judges.

### D7 — Memory and the connection lifecycle, amending D4

ADR 0029 D4 reuses `recycler.Pool` for per-connection objects and
`kernel/buffer` for scratch, and gives each connection a goroutine. The loop
engine does not, and why is part of the decision:

- Everything the kernel reads or writes — rings, provided buffers, payloads —
  comes from `syscall.Mmap`, never from the Go heap: the collector may move
  nothing the kernel holds and must scan nothing it does not need to.
- The connection table holds no pointers; the identifier the kernel echoes is
  an integer. Control state (lengths, heads, tails, states) stays in
  pointer-free Go slices, where the race detector sees it.
- Output buffers come from a shared mmap slab cut into size classes, attached
  at a connection's first `Write` and returned once drained, so an idle
  connection holds no mapped payload. A global ceiling on spill and output
  sheds load by name.
- An idle connection has a budget, in Go heap, mapped and kernel memory,
  fixed in the design and held by a hand-written test in a dedicated lane
  without the race detector.
- A loop is one goroutine locked to its OS thread, which creates and owns its
  rings. It is started and stopped by `server`'s lifecycle; no connection has
  a goroutine of its own.

### D8 — Observability

`State()` gains, for each loop group, the backend in use, the probe stage
reached, the reason for any fallback, `Loops`, `Rings` and where `Rings` came
from, the effective `RLIMIT_NOFILE` and `GOMAXPROCS`, per-ring atomic
counters refreshed at `OnBatchEnd`, and the p99 of the batch duration. The
engine writes one line to standard error at start, naming the backend, the
probe stage and any fallback reason. Exact fields are the design's.

### D9 — The public surface, and the order it is published in

Every merge to `main` publishes a release, and an alias publishes a type the
moment its target changes: a method added to `svcserver.StreamGroup` or a
field added to `corenet.StateValue` is public at once, through `server.Group`
and `server.State`. The order below is therefore an order of publication, and
each step is coherent on its own.

| # | Change | Published surface |
|---|---|---|
| 1 | this record | none |
| 2 | `internal/kernel/swar` — first-match byte search, bounded reads only | **not published** (§D10) |
| 3 | `internal/kernel/jsonenc` — one JSON string and integer appender, an ordered-key builder; the real JSON emitters migrate | re-exported by `service/data/codec/json` and `pkg/v1/data/codec/json` (§D10) |
| 4 | `security/token` — the `VerifierInto` port, `VerifyScratch`, `ClaimsBuf` and their codes | `pkg/v1/security/token` |
| 5 | `core/net` — `LoopConnRef`, `LoopIO`, `LoopHandler`, `PostFunc`, `OptionKind`, the `http1` request value and handler port, the new sentinels | none (internal) |
| 6 | facades of step 5's values and ports | `pkg/v1/net/server` aliases, so a consumer can implement `LoopHandler` in public (ADR 0090) before it can serve one |
| 7 | `LoopValue` and its `Backend` constants | `pkg/v1/net/server` |
| 8 | `GroupOption Loop(v)` | `pkg/v1/net/server`; refused with `LoopOptionUnsupported` by every group until step 11 |
| 9 | the `State` fields of §D8 | `server.State`, by alias; zero while no loop group exists |
| 10 | engines: `internal/service/net/loop` (epoll, io_uring, conformance), the portable backend in `server`, the `http1` codec | none (internal; reached by internal tests only) |
| 11 | `StreamGroup.HandleLoop`, alone, once every `linux-init` lane and the Ubuntu lane of `e2e-cross` are green and name both Linux backends | `server.Group.HandleLoop` |
| 12 | the `http1` facade, with step 11 or after it | `pkg/v1/net/http1` |

Steps 2 to 4 are independent releases and do not wait on the loop. Step 4's
port is the SDK's only port documented as **not** safe for concurrent use:
`Verifier` stays concurrent and immutable, and the mutable HMAC state lives in
a scratch the caller owns, one per loop, refused with a typed error when it
belongs to another verifier or is already in use. The new `security/token`
codes are in its own `0.2.13.*` block.

New `0.2.11.*` sentinels, values allocated by the design change that declares
them, from `0.2.11.35` on: `LoopTLSUnsupported`, `LoopOptionUnsupported`,
`LoopBackendUnavailable`, `LoopStaleRef`, `LoopOutputFull`, `LoopSpillFull`,
`LoopPostQueueFull`, `LoopWrongThread`, and the `http1` profile's refusals.

### D10 — Two kernel rows for ADR 0159 §4

| Kernel package | Publication |
|---|---|
| `swar` | **not published.** A word-at-a-time scan is a measurement a domain applies with a bound (`hasless` is valid only for n ≤ 128; a borrowing mask is never walked bit by bit). Published alone it invites the unbounded use its own document warns against — the reason §4 keeps `pathchain`, `plugin` and `flock` unpublished. |
| `jsonenc` | published through the codec domain: `service/data/codec/json` and `pkg/v1/data/codec/json` re-export its appenders, as the logger publishes `buffer`. It registers no codec, and `codec/json.Marshal` does not change. |

Both pass the kernel's rule — the standard library only, and a vocabulary any
domain may import: the logger, the OTLP emitters, `redact` and the token
formats each write JSON strings today. `jsonenc` escapes U+2028 and U+2029,
replaces invalid UTF-8 with U+FFFD and does no HTML escaping, and says so.

## Consequences / Semantics

- The default engine, `HandleHTTP`, TLS, WebSocket, SSE and the static tree
  are untouched. A program that never calls `HandleLoop` sees new symbols and
  nothing else.
- `internal/service/net` gains two engines beneath the `net` domain, `loop`
  and `http1`, both answering to `core/net` (ADR 0160 §1, ADR 0163 §11); they
  declare no codes. `pkg/v1/net` gains `http1`.
- **The runtime bar grows.** `./net/loop` joins `e2e-cross`'s `SERVICE_PKGS`;
  `linux-init` gains an Ubuntu noble VM at a pinned kernel of 6.8 or later. The
  expected backends are compiled into the test binary and a backend missing on
  a lane fails it: under Linux, a test binary given no expectation fails,
  unless told `none`. The repository's required checks do not change; reading
  those lanes on the head commit is a precondition of merging steps 10 to 12.
- The conformance suite runs once on every backend present, with faults
  injected: a tiny slot table, a two-buffer ring, buffer exhaustion, partial
  consumption, stale-generation writes, recycled buffers filled with a marker,
  `EMFILE` under a lowered `RLIMIT_NOFILE` in a re-executed child, partial and
  blocked sends larger than the socket buffer, a full completion queue, and
  each probe stage failed in turn.
- Budgets are on allocations, inlining, escapes and `nsop.maxDelta` against
  the base (ADR 0165), never on absolute nanoseconds. The comparison with C
  references is a diagnostic benchmark outside CI, never a gate.
- io_uring brings in-tree ABI layouts that must follow the kernel's uapi by
  hand, frozen by `Sizeof` and `Offsetof` tests — the cost of the `x/sys` ban,
  accepted by ADR 0018.
- A loop handler runs on a locked OS thread and must not block it: a blocking
  call stalls every connection of that loop. That is the price of the model,
  and its documentation says it first.

## Breaking changes

None. Every symbol is an addition; `LimitsValue`, `TimeoutsValue` and every
published option keep their meanings, and `Verifier` keeps its concurrent
contract.

## Alternatives considered

- **A separate public loop lifecycle** — a `loop.Server` with its own
  `Start` / `Shutdown`. Rejected: two lifecycles would each own bind,
  adoption, admission and drain, and drift exactly as ADR 0029 feared two
  copies of TLS plumbing would. One `Server`, one drain, one `State`.
- **Lift ADR 0029 D2 for TLS too.** Rejected: an event loop over `crypto/tls`
  would need a non-blocking TLS record layer the SDK does not own.
- **A vendored event-loop library** (`gnet`, `cloudwego/netpoll`). Rejected:
  ADR 0156 admits no module into `pkg`, and the libraries reach `x/sys`.
- **IOCP now.** Rejected until proven: no measured consumer runs the loop on
  Windows, and an IOCP backend is a second completion model to keep correct
  for nobody. The portable backend serves Windows meanwhile.
- **Event ports on illumos and Solaris.** Rejected: `port_create` and
  `port_getn` live in libc, which the standard library's `syscall` does not
  export there; reaching them means `x/sys` (banned) or a hand-rolled
  `//go:cgo_import_dynamic` trampoline, runtime plumbing the SDK does not own
  (the reasoning of ADR 0144). The portable backend serves both.
- **A native Go SQLite engine**, so the downstream could stay pure Go end to
  end. Rejected: it is outside this domain, and a reimplementation risks silent
  corruption — the WAL's checksums and salts, POSIX lock interplay with other
  SQLite processes, B-tree balancing — for a consumer that can bind the C
  amalgamation outside `pkg/v1`.
- **Reimplement HTTP/1.1 fully on the loop.** Rejected: D3's reasons stand. The
  profile refuses what D3 named as the risk instead of handling it.

## Deferred

- kqueue (darwin, the BSDs) and IOCP (windows), each in its own measured change.
- TLS on the loop, and HTTP/2, `CONNECT` and `Upgrade` in the `http1` profile.
- `DEFER_TASKRUN` as io_uring's default: an option until it passes the whole
  conformance suite.
- `ring`'s multi-producer mode (ADR 0159), should the `Post` queue want it.

## References

- [ADR 0029](0029-sdk-net-domain.md) D1–D5, D8; [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) §4.
- `io_uring_setup(2)`, `io_uring_enter(2)`, `io_uring_register(2)`;
  Linux `include/uapi/linux/io_uring.h` (6.8).
- `epoll_ctl(2)`, `epoll_wait(2)`, `eventfd(2)`.
- RFC 9110 §10.1.1 (`Expect`), RFC 9112 §6 (message body length) and §11
  (request smuggling).
- Impl, to come: `internal/core/net/`, `internal/service/net/{loop,http1}/`,
  `internal/service/net/server/`, `pkg/v1/net/{server,http1}/`,
  `internal/kernel/{swar,jsonenc}/`, `internal/core/security/token/`.
