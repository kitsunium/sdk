# ADR 0029 — Network domain (`net`): unified inbound + outbound transport

- **Status**: Draft
- **Date**: 2026-09-02
- **Deciders**: @kodflow
- **Related**: ADR 0016 (proc — the one-sibling/many-facades precedent), ADR 0018
  (portability — the build-bar/runtime-bar contract and the `x/sys` ban),
  ADR 0026 (resilience — bulkhead/timeout policies reused here), ADR 0027
  (metrics), ADR 0028 (config — typed load), ADR 0013 (crypto — the redacting
  `Key` precedent), ADR 0005/0006 (error codes)
- **Amends**: widens the `internal/core` purpose statement with an 11th sibling

## Context

The SDK has ten domains and **not one line of network code**. Verified on
`origin/main` (`6fff7e1`):

- `crypto/tls` — **zero occurrences repo-wide**, production or test.
- `net.Listen("tcp", …)` — **never called in production code**. The repo has
  never bound a TCP port. The only listener creation is
  `net.FileListener` (adopting an inherited fd, `service/proc/sdlisten`) and
  `net.ListenUnixgram` (the sd_notify supervisor socket).
- `net.PacketConn` — zero occurrences.
- `http.Server` — zero occurrences outside four `httptest` call sites.

Every outbound path (`writer/nettransport`, `logger/sink/syslog`,
`writer/journald`) is a bespoke `net.Dial` seam, and `nettransport/CLAUDE.md`
states plainly that *"native TLS is a future addition"*.

Two consumers are pushing on this at once:

1. **A server need** — the owner wants one server abstraction covering TCP, UDP,
   Unix stream and datagram, TLS and mTLS, with pluggable, groupable handlers:
   *"la même chose partout, que ça marche parfaitement, et pouvoir y claquer des
   handlers très facilement."*
2. **A client need** — the first real downstream (`5gc-mcp`) calls two upstream
   APIs over HTTPS with mutual authentication. It is writing a throwaway
   `internal/httpx` because the SDK offers nothing, and it reports the exact
   trap the SDK should close: `x509.CertPool.AppendCertsFromPEM` returns a
   boolean that everyone ignores, so an empty or malformed CA bundle silently
   yields a pool that verifies nothing.

Both faces need the *same matter*: TLS identity, per-phase deadlines, size
caps, metrics, structured call logging, and an enforceable policy hook. Building
them as two unrelated packages would duplicate that substrate and let it drift.

## Decision

### D1 — One core sibling, `internal/core/net`, on the `proc` precedent

`net` becomes the **11th `internal/core` sibling** and owns a single error
block, `0.2.11.*`. It follows ADR 0016's shape exactly: **one core package
declaring every port, value type and sentinel; many service implementations;
many `pkg/v1` facades.** `proc` is the model — one `0.2.6.*` block serves
`exec`/`signal`/`reaper`/`rlimit`/`cgroup`/`sdnotify`/`sdlisten` and five public
facades. Service implementations and facades declare **zero** new codes; they
`errs.Wrap` the core sentinels.

**No registry.** Like `proc` and `resilience`, there is one canonical
implementation per primitive; a runtime plug-in registry would be
over-abstraction.

Inside `internal/core/net` the stdlib is imported as `stdnet "net"` so a reader
never has to disambiguate the package's own name.

```
internal/core/net/          ports + value types + every 0.2.11.* sentinel
internal/service/net/
├── tlsid/                  TLS/mTLS material loading (files and memory)
├── client/                 guarded HTTP client + policy-enforcing RoundTripper
└── server/                 the listener engine (stream + datagram + adapters)
pkg/v1/tlsid/               facade — TLS identity
pkg/v1/client/              facade — outbound
pkg/v1/server/              facade — inbound
```

### D2 — Goroutine-per-connection on the runtime netpoller

The server keeps **one goroutine per accepted connection**, scheduled by Go's
own netpoller. It does **not** adopt an event-loop architecture
(`gnet`, `cloudwego/netpoll`).

This is not a convenience choice, it is a correctness one:

1. **TLS and mTLS are hard requirements.** `crypto/tls` is written against
   blocking `net.Conn` semantics. The event-loop libraries either do not support
   TLS at all or bolt on a partial re-implementation that lags the stdlib on
   protocol and CVE coverage. A TLS stack we do not control is a security
   liability the SDK will not take on.
2. **The netpoller already *is* the event loop.** Go's runtime multiplexes every
   socket through `epoll`/`kqueue` and parks the goroutine; the "one goroutine
   per connection" cost is a ~4 KiB growable stack and a scheduler entry, not a
   thread and not a syscall. The event-loop libraries' advantage is avoiding
   *goroutine stack* memory, and it only becomes material past several hundred
   thousand **idle** connections — a regime none of the owner's projects are in.
3. **Interoperability is the product.** `net/http`, `crypto/tls`, `database/sql`
   drivers and every `io.Reader`/`io.Writer` in the ecosystem speak `net.Conn`.
   An event loop forfeits all of it and forces a bespoke handler dialect.

The optimisation budget goes where it actually pays at our scale: accept-path
contention, per-connection allocation, and datagram syscall amortisation (D5).

### D3 — Do not reimplement HTTP; adapt to it

`net/http` is a mature, heavily optimised HTTP/1.1 + HTTP/2 implementation with
a decade of hardening. Rewriting it would be a **regression**, not an
optimisation: we would inherit responsibility for request smuggling defences,
`Expect: 100-continue`, chunked trailers, HTTP/2 flow control and HPACK,
`CONNECT`, and the entire CVE stream — to lose, not gain, throughput.

The domain therefore ships an **adapter**: our unified listener, our limits, our
TLS identity, our metrics and our drain, with `http.Server` mounted on top. The
bridge is a `net.Listener` implementation whose `Accept` pops from a channel fed
by our accept path; `http.Server.Serve` consumes it. Cost is one channel
hand-off per **connection** (not per request), which is noise next to a TCP
handshake — and D9 measures it rather than asserting it.

```go
group.HandleHTTP(mux)   // http.Handler served over our listener, limits, TLS, drain
```

### D4 — Reuse the SDK; invent nothing that exists

| Need | Reused |
|---|---|
| per-connection object pooling | `internal/kernel/recycler.Pool[T]` |
| read/write scratch buffers | `internal/kernel/buffer` (`*[]byte`, 64 KiB discard cap) |
| accept/poll goroutine lifecycle | `internal/kernel/worker` (`Start`, `Every`, `Stop`) |
| hot-swappable limits | `internal/kernel/snapshot.Value[T]` |
| connection ceiling | `pkg/v1/resilience.NewBulkhead` (channel semaphore, reject mode) |
| per-connection / per-request deadline | `pkg/v1/resilience.NewTimeout` |
| inbound admission rate | `pkg/v1/resilience.NewRateLimiter` |
| counters / histograms | `pkg/v1/metrics` |
| structured logs | `pkg/v1/logger` |
| typed errors | `pkg/v1/errs` |
| typed configuration | `pkg/v1/config` (`json` tags — the only mapping mechanism) |
| socket activation | `pkg/v1/sdlisten` — the server **adopts inherited listeners** |
| shutdown signal | `pkg/v1/signal.Notify` |

Two gaps in the reused surface are recorded as consequences, not worked around
silently:

- **`sdlisten.Listeners()` returns `[]net.Listener` only.** Datagram socket
  activation is unimplemented upstream. The server adopts inherited *stream*
  listeners today and builds `net.FilePacketConn` from `sdlisten.Files()` for
  the datagram case; promoting that into `sdlisten` is left to a follow-up.
- **`pkg/v1/metrics` is label-free in v1** (ADR 0027 deferred labels).
  Per-listener and per-group dimensions are therefore encoded **into the
  instrument name** (`net.server.<group>.conns.accepted`). When ADR 0027 grows
  labels, the names collapse into labels — a facade-internal change.

### D5 — Optimisations are implemented *and measured*, never assumed

| Technique | Mechanism | Status |
|---|---|---|
| `SO_REUSEPORT` shards | N independent listeners on one address, one accept loop each — removes accept-mutex contention | **spiked green** (below) |
| datagram batch read | `recvmmsg` — N datagrams per syscall | **spiked green** (below) |
| per-connection reuse | `recycler.Pool` + `kernel/buffer`; zero steady-state allocation on the serve path | to measure |
| `TCP_NODELAY`, backlog, keep-alive | `net.ListenConfig` / `net.TCPConn` | stdlib defaults `TCP_NODELAY` on; backlog + keep-alive are ours |
| per-phase deadlines | read-header / read-body / write / idle, set per phase, not once per connection | design |
| no `any` on the hot path | typed structs and concrete generics throughout | design |

**Both risky mechanisms were spiked against the real kernel before this ADR was
written**, because ADR 0018 forbids declaring a syscall done on a green
compile:

```
REUSEPORT OK: two listeners bound to 127.0.0.1:40235
RECVMMSG OK: received 3 datagrams in ONE syscall
```

#### The `x/sys` collision — and why the ban survives

`golang.org/x/sys` is **banned SDK-wide** (ADR 0016 §Dependency discipline,
ADR 0018). The idiomatic batch-read path, `golang.org/x/net/ipv4`'s
`PacketConn.ReadBatch`, transitively imports `golang.org/x/sys/unix` — so the
obvious implementation is not available to us.

The ban holds, and ADR 0018's own precedent supplies the answer: *"native
backends use raw stdlib `syscall` with the ABI constants and struct layouts
hand-defined and cited in the source."* The spike confirms this is sufficient:

- `syscall.SYS_RECVMMSG` (299), `syscall.Msghdr`, `syscall.Iovec` and
  `syscall.MSG_WAITFORONE` **are** in the stdlib on Linux. Only `struct mmsghdr`
  itself must be declared in-tree (it is `Msghdr` plus a `uint32` length and tail
  padding — 64 bytes, asserted by `unsafe.Sizeof`).
- `syscall.SO_REUSEPORT` is **absent** from the stdlib and is hand-defined with
  its header citation (`15` on Linux per `include/asm-generic/socket.h`; `0x200`
  on Darwin and the BSDs).
- The fd is reached through `(*net.UDPConn).SyscallConn()` and driven inside
  `RawConn.Read`, so returning `false` on `EAGAIN` parks the goroutine on the
  **netpoller** exactly like a normal read. Batching does not cost us
  integration with the runtime.

Per ADR 0018 both mechanisms are build-tag split with an `_other.go` floor:
batch read degrades to a `ReadFrom` loop, and `SO_REUSEPORT` degrades to a
single listener with N accept goroutines. Degradation is **reported in
`State()`**, never silent. Note this is a *different mechanic for the same
capability*, not a missing capability, so it does **not** return
`UnsupportedPlatform`.

### D6 — TLS identity is an opaque, redacting value type

Modelled on `internal/core/crypto.Key` and `internal/core/writer.CredentialValue`
— value receivers, every secret field unexported, a validating constructor that
defensive-copies, and `String()`/`GoString()` returning a constant marker so
neither `%v`/`%s` nor `%#v` can leak material.

The constructor **fails loudly where the stdlib is silent**: a PEM bundle that
yields no usable certificate is `TLS_MATERIAL_INVALID`, never an empty pool that
verifies nothing. `MinVersion` defaults to **TLS 1.3** and rejects anything below
TLS 1.2. Material loads from **files or memory** — the in-memory path is what
lets a consumer source certificates from a secret manager.

### D7 — Outbound policy is enforced in the `RoundTripper`

The single most important client requirement: read-only is a **code guarantee,
not a convention**. `Policy.Allow` is evaluated inside the `RoundTripper`, below
every call site, so no forgotten check can bypass it. A caller cannot construct a
request that skips the policy, because the policy lives under the transport.

The client also ships per-phase timeouts (dial, TLS handshake, response headers,
total), a response-body size cap via `io.LimitReader`, a redirect cap, default
headers, and a `CallHook` fired once per outbound call with method, host, path,
status, byte count and duration.

### D8 — Error block `0.2.11.*`

Core PP octet **11** is the next free core slot (2–10 allocated, 16/17 reserved
for `logger`). Every sentinel is declared in `internal/core/net`; service and
facade layers wrap, never define.

| Code | Const | Reason | Exit |
|---|---|---|---|
| 0.2.11.1 | `CodeListenFailed` | `LISTEN_FAILED` | 69 |
| 0.2.11.2 | `CodeInvalidAddress` | `INVALID_ADDRESS` | 64 |
| 0.2.11.3 | `CodeUnsupportedNetwork` | `UNSUPPORTED_NETWORK` | 64 |
| 0.2.11.4 | `CodeServerClosed` | `SERVER_CLOSED` | 69 |
| 0.2.11.5 | `CodeAlreadyStarted` | `ALREADY_STARTED` | 70 |
| 0.2.11.6 | `CodeNotStarted` | `NOT_STARTED` | 70 |
| 0.2.11.7 | `CodeHandlerMissing` | `HANDLER_MISSING` | 64 |
| 0.2.11.8 | `CodeHandlerPanic` | `HANDLER_PANIC` | 70 |
| 0.2.11.9 | `CodeConnLimitReached` | `CONN_LIMIT_REACHED` | 75 |
| 0.2.11.10 | `CodeDrainTimeout` | `DRAIN_TIMEOUT` | 75 |
| 0.2.11.11 | `CodeGroupUnknown` | `GROUP_UNKNOWN` | 64 |
| 0.2.11.12 | `CodeGroupDuplicate` | `GROUP_DUPLICATE` | 64 |
| 0.2.11.13 | `CodeSocketAdoptFailed` | `SOCKET_ADOPT_FAILED` | 71 |
| 0.2.11.14 | `CodePacketTooLarge` | `PACKET_TOO_LARGE` | 65 |
| 0.2.11.15 | `CodeTLSMaterialInvalid` | `TLS_MATERIAL_INVALID` | 78 |
| 0.2.11.16 | `CodeTLSHandshakeFailed` | `TLS_HANDSHAKE_FAILED` | 69 |
| 0.2.11.17 | `CodeRequestDenied` | `REQUEST_DENIED` | 77 |
| 0.2.11.18 | `CodeResponseTooLarge` | `RESPONSE_TOO_LARGE` | 65 |
| 0.2.11.19 | `CodeCallFailed` | `CALL_FAILED` | 69 |
| 0.2.11.20 | `CodeTooManyRedirects` | `TOO_MANY_REDIRECTS` | 69 |

0.2.11.21–255 reserved.

### D9 — Performance is proven, not claimed

`*_bench_test.go` + `BENCH.md` per package, comparing:

- our TCP server against a bare `net.Listener` accept loop — the surcharge must
  be indistinguishable;
- our HTTP adapter against `net/http` standing alone — same;
- batch datagram read against a `ReadFrom` loop;
- `SO_REUSEPORT` sharded accept against a single shared listener.

**If `SO_REUSEPORT` shows no gain on the measuring machine, `BENCH.md` will say
so.** A measurement that contradicts the design is reported, not buried.

## Public API

### `pkg/v1/tlsid` — shared by both faces

```go
type Identity = corenet.IdentityValue // opaque; String/GoString are "<redacted>"

type Params struct {
    CertPEM           []byte // leaf + intermediate chain
    KeyPEM            []byte
    RootsPEM          []byte // CAs used to verify the peer
    ClientCAPEM       []byte // server side: CAs allowed to present client certs
    ServerName        string
    MinVersion        uint16 // 0 -> TLS 1.3; below TLS 1.2 is refused
    NextProtos        []string
    RequireClientCert bool   // server side: turns TLS into mTLS
}

type FileParams struct {
    CertFile, KeyFile, RootsFile, ClientCAFile string
    ServerName                                 string
    MinVersion                                 uint16
    NextProtos                                 []string
    RequireClientCert                          bool
}

func New(p Params) (Identity, error)      // in-memory material
func Load(p FileParams) (Identity, error) // from disk

func (i Identity) ClientConfig() *tls.Config
func (i Identity) ServerConfig() *tls.Config
func (i Identity) IsZero() bool
```

### `pkg/v1/client` — outbound

```go
type RequestInfo = corenet.RequestValue // { Method, Scheme, Host, Path string }
type CallInfo    = corenet.CallValue    // { Method, Host, Path, Status, Bytes, Duration, Err }
type Policy      = corenet.Policy       // interface { Allow(RequestInfo) error }
type PolicyFunc  = corenet.PolicyFunc
type CallHook    = corenet.CallHook     // func(CallInfo)

type Config struct {
    BaseURL         string   `json:"base_url"`
    DialTimeout     Duration `json:"dial_timeout"`
    HandshakeTimeout Duration `json:"handshake_timeout"`
    ResponseTimeout Duration `json:"response_timeout"` // response headers
    TotalTimeout    Duration `json:"total_timeout"`
    MaxResponseSize int64    `json:"max_response_size"`
    MaxRedirects    int      `json:"max_redirects"`
    DefaultHeaders  map[string]string `json:"default_headers"`
}

type Option func(*options)
func WithIdentity(id tlsid.Identity) Option
func WithPolicy(p Policy) Option
func WithCallHook(h CallHook) Option
func WithLogger(lg logger.Logger) Option
func WithMeter(m metrics.Meter) Option
func WithConfig(cfg Config) Option

func New(opts ...Option) (*Client, error)
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error)
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error)
func (c *Client) HTTP() *http.Client   // escape hatch: policy still enforced

// Ready-made policies
func AllowMethods(methods ...string) Policy
func AllowPaths(anchored ...*regexp.Regexp) Policy
func DenyPaths(anchored ...*regexp.Regexp) Policy
func AllowAll() Policy
func Policies(ps ...Policy) Policy // conjunction: every policy must allow
```

`Client.HTTP()` returns an `*http.Client` whose `Transport` is the guarded
`RoundTripper`. Handing that to a third-party SDK keeps the guarantee — which is
precisely why the check lives in the transport.

### `pkg/v1/server` — inbound

```go
// Two handler natures.
type Conn interface {
    net.Conn
    ID() uint64
    Group() string
    Buffer() []byte // connection-scoped scratch, valid until ServeConn returns
}
type Packet interface {
    Data() []byte   // valid until ServePacket returns
    From() net.Addr
    To() net.Addr
    ID() uint64
    Group() string
    Reply(b []byte) (int, error)
}

type ConnHandler interface   { ServeConn(ctx context.Context, c Conn) error }
type PacketHandler interface { ServePacket(ctx context.Context, p Packet) error }

type ConnHandlerFunc   func(ctx context.Context, c Conn) error
type PacketHandlerFunc func(ctx context.Context, p Packet) error

// One generic middleware shape for both natures.
type Middleware[H any] func(next H) H
func Chain[H any](h H, mws ...Middleware[H]) H // mws[0] is outermost

// Lifecycle
func New(opts ...Option) (*Server, error)
func (s *Server) Group(name string, opts ...GroupOption) (*StreamGroup, error)
func (s *Server) PacketGroup(name string, opts ...GroupOption) (*PacketGroup, error)
func (s *Server) Start(ctx context.Context) error    // bind + accept, returns once bound
func (s *Server) Serve(ctx context.Context) error    // Start, block, then Shutdown
func (s *Server) Shutdown(ctx context.Context) error // stop accepting, drain to ctx deadline
func (s *Server) Close() error                       // immediate
func (s *Server) State() State
func (s *Server) Addrs() []net.Addr

func (g *StreamGroup) Use(mws ...Middleware[ConnHandler])
func (g *StreamGroup) Handle(h ConnHandler)
func (g *StreamGroup) HandleFunc(f ConnHandlerFunc)
func (g *StreamGroup) HandleHTTP(h http.Handler)     // the D3 adapter
func (g *PacketGroup) Use(mws ...Middleware[PacketHandler])
func (g *PacketGroup) Handle(h PacketHandler)

// Options
func Listen(network, addr string) GroupOption   // tcp, tcp4/6, udp, unix, unixgram, unixpacket
func Adopt(names ...string) GroupOption         // systemd socket activation
func TLS(id tlsid.Identity) GroupOption
func MaxConns(n int) GroupOption
func Shards(n int) GroupOption                  // 0 = auto (GOMAXPROCS when SO_REUSEPORT is available)
func Backlog(n int) GroupOption
func BatchSize(n int) GroupOption               // datagram recvmmsg batch
func ReadTimeout(d time.Duration) GroupOption
func WriteTimeout(d time.Duration) GroupOption
func IdleTimeout(d time.Duration) GroupOption
func HandshakeTimeout(d time.Duration) GroupOption
func MaxPacketSize(n int) GroupOption
func WithLogger(lg logger.Logger) Option
func WithMeter(m metrics.Meter) Option
func WithDrainTimeout(d time.Duration) Option
func WithConfig(cfg Config) Option

// Ready-made middlewares
func Recover() Middleware[ConnHandler]  // HANDLER_PANIC, never kills the process
func LogConns(lg logger.Logger) Middleware[ConnHandler]
func RateLimit(r resilience.Runner) Middleware[ConnHandler]
```

Ergonomics target:

```go
srv, _ := server.New(server.WithLogger(lg), server.WithMeter(m))

api, _ := srv.Group("api",
    server.Listen("tcp", ":8443"),
    server.Listen("unix", "/run/api.sock"),
    server.TLS(id), server.MaxConns(10_000), server.IdleTimeout(30*time.Second))
api.Use(server.Recover(), server.LogConns(lg))
api.HandleHTTP(mux)

dns, _ := srv.PacketGroup("dns", server.Listen("udp", ":53"), server.BatchSize(64))
dns.Handle(resolver)

_ = srv.Serve(ctx) // drains on ctx cancellation
```

### Concurrency model

- One goroutine per accept shard; one per connection; one per datagram-read
  shard. Datagram handlers run on the read goroutine by default (no hand-off,
  no allocation) and on a worker pool only when `BatchSize > 1` **and** the
  handler is declared blocking.
- `Shutdown` closes listeners first (unblocking `Accept`), flips the phase to
  `Draining`, then waits for the active-connection count to reach zero or for
  `ctx` to expire — mirroring `service/proc/exec.handle.Stop`, the SDK's only
  existing ctx+grace precedent. Overrun is `DRAIN_TIMEOUT`; connections are then
  closed hard.
- Per-connection state is pooled; handlers **must not** retain `Conn`, `Packet`
  or `Buffer()` past return — the same contract `logger.Builder` already carries.

### Configuration

`Config` uses `json` tags only, because `pkg/v1/config` decodes through a JSON
round-trip. A `Duration` value type marshals both `"30s"` and raw nanoseconds,
since `time.Duration` alone would force operators to write `30000000000` in YAML.

## Consequences

- 11th core sibling; `internal/core` purpose statement widened. `docs/error-codes.yaml`,
  the root `CLAUDE.md` ledger and every touched package `CLAUDE.md`/`README.md`
  are updated in the same change set (rule 11).
- **The SDK gains its first `crypto/tls` and `net/http` dependencies.** Both are
  stdlib, so the dep-light invariant is untouched and `pkg` consumers pull no
  vendor code.
- New emitter package ⇒ `audit_srcs` filegroup in `internal/core/net/BUILD.bazel`
  **and** an entry in `//:audit_sources`, else the block is unaudited.
- Windows: no `SO_REUSEPORT`, no Unix datagram sockets, no fd inheritance.
  Sharding degrades to N accept goroutines; the rest returns the uniform
  `UnsupportedPlatform` per ADR 0018. Every package keeps an `_other.go` floor so
  the cross-compile matrix stays green on all 8 GOOS.
- `sdlisten` datagram activation and `metrics` labels are recorded gaps (D4).

## Alternatives considered

- **Event-loop server (`gnet`, `cloudwego/netpoll`).** Rejected — D2: no
  trustworthy TLS, no `net.Conn` interoperability, and the memory advantage only
  materialises past ~10^5 idle connections. It would also add a vendor
  dependency to a domain that is otherwise pure stdlib.
- **Reimplementing HTTP.** Rejected — D3: strictly a regression in correctness
  and security for no throughput gain.
- **`golang.org/x/net/ipv4` for `ReadBatch`.** Rejected — it transitively imports
  `golang.org/x/sys`, banned SDK-wide. Raw stdlib `syscall` with cited constants
  is the ADR 0018 precedent and the spike proves it suffices.
- **Two separate domains (`server` and `client`) with duplicated TLS/timeout
  plumbing.** Rejected — they share the substrate; two copies would drift, and
  the redaction/validation rules would end up enforced in one and not the other.
- **Two separate ADRs.** Rejected — the decision *is* the shared substrate. The
  layering, the code block and the value types are one choice; splitting them
  across two records would duplicate context and invite divergence. Follow-up
  native backends get their own ADR when they land.
- **Putting the TLS identity in `internal/core/crypto`.** Rejected — `crypto`
  owns algorithm registries (AEAD/Hasher/Signer/MAC/KDF). A `*tls.Config`
  builder is transport plumbing that happens to carry key material; it belongs
  with the transport that consumes it. The *redaction pattern* is reused
  verbatim.
- **A plug-in registry of transports.** Rejected — one canonical implementation
  per primitive (the `proc`/`resilience` no-registry precedent).
- **`Conn` as a concrete pooled struct rather than an interface.** Rejected —
  assigning a pointer to an interface does not allocate, so the interface is
  free, and it keeps the pooled type unexported and swappable.

## Delivery order

Sequenced so the real consumer is unblocked first.

1. `core/net` value types + the `0.2.11.*` block; `service/net/tlsid`;
   `pkg/v1/tlsid`.
2. `service/net/client` + `pkg/v1/client` — the policy-enforcing transport.
3. `service/net/server` stream engine (TCP/Unix/TLS/mTLS) + `pkg/v1/server`.
4. Datagram engine with batch read; `SO_REUSEPORT` sharding.
5. HTTP adapter; socket activation; benchmarks + `BENCH.md`.

## References

- Impl: `internal/core/net/`, `internal/service/net/{tlsid,client,server}/`,
  `pkg/v1/{tlsid,client,server}/`.
- ADR 0016 (one-sibling/many-facades), ADR 0018 (portability, `x/sys` ban),
  ADR 0026 (resilience policies), ADR 0027 (metrics), ADR 0028 (config).
- Linux `include/asm-generic/socket.h` — `SO_REUSEPORT` = 15.
- `recvmmsg(2)`, `struct mmsghdr` — `<sys/socket.h>`.
