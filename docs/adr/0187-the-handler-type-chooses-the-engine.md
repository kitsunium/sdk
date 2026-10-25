# ADR 0187 — the handler type chooses the engine

- **Status**: Accepted
- **Date**: 2026-10-25
- **Accepted**: 2026-10-25, on the owner's decisions of 2026-10-24 for this
  work: the handler's type chooses the engine, and nothing is inferred about a
  handler that the type does not say; QUIC, HTTP/3 and a composite group are a
  programme of their own; TLS stays `crypto/tls` on the goroutine engine, with
  no kTLS and no TLS on the loop; HTTP/2 comes from `net/http`'s `Protocols`,
  for `http.Handler` groups only; the standard library stays wherever it
  exists, and the hot path is the SDK's own handlers and middlewares. It can be
  revisited while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md)
  D1 (how a group's options are judged, and the engine chosen by the
  handler's type rather than by one method alone), D2 (an `http1` handler may
  be served behind `crypto/tls`, §D5 below) and D3 (the portable backend
  carries that TLS path, and the codec runs behind it)
- **Amended by**: [ADR 0188](0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md) §D8 (the
  switches made concrete) and §Consequences (`StateValue` gains four fields)
- **Related**: [ADR 0029](0029-sdk-net-domain.md) (the domain, one goroutine
  per connection), [ADR 0031](0031-policy-zero-values-are-never-inert.md)
  (zero values), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the
  v0 licence for the breaking changes below),
  [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md)
  (design first), [ADR 0174](0174-a-tls-identity-is-checked-on-every-handshake-and-reloaded-in-place.md)
  (`PreHandshake`)

## Context

ADR 0186 gave a stream group a second engine, the loop, entered by one method,
`HandleLoop`, and configured by one option, `Loop(v)`. Three things it left
open now block the next step, a network API a consumer writes in one line and
that serves each handler on the fastest engine of its platform:

- **The engine is chosen by a method, not by the handler.** A consumer who
  wants the loop must know it exists, call `HandleLoop` and pass `Loop(v)`. The
  owner's decision is that the handler's type chooses: a `LoopHandler` runs on
  the loop, a `ConnHandler` or an `http.Handler` on the goroutine engine, and
  nothing is ever inferred about a handler — "this one does not block" — that
  its type does not say.
- **A loop group judges its options by guessing.** At `Start`, ADR 0186 D1
  refuses every option a loop group cannot honour. The code reconstructed
  "the options given" from the fields they set, a non-zero `ReadTimeout`, a
  non-zero `Shards`. An option given with its zero value — `ReadTimeout(0)`,
  `TLS` with a zero identity — set nothing and went through unjudged: a
  refusal rule that an argument can switch off is not a rule.
- **Nothing says a group is meant to be plaintext.** A group without `TLS`
  serves plaintext; whether that was the intent, or a `TLS` option forgotten,
  cannot be told apart.

## Decision

### D1 — The handler's type chooses the engine

A group's engine family is chosen by the type of its handler: a
`corenet.LoopHandler` runs on the loop engine of ADR 0186, a
`corenet.ConnHandler` and an `http.Handler` on the goroutine engine of ADR 0029
D2. The SDK never infers that a handler does not block, and never moves a
handler to another family than its type's. Inside the loop family, the backend
is `LoopValue.Backend`'s (ADR 0186 §D5), unchanged.

### D2 — Every option records its kind; a refusal reads the set

Every `GroupOption` constructor records its `OptionKind` in the set of kinds
the group was given, whatever its argument. `Use` and `PreHandshake` record
theirs too — `OptionMiddleware` on every call of `Use`, even with no
middleware. A refusal at `Start` reads that set and nothing else, so an option
given with its zero value is an option given: on a loop group,
`ReadTimeout(0)` is refused as `ReadTimeout(time.Second)` is
(§Breaking changes).

- `OptionKind` stays a `uint8` iota, not a bit. The set does the shifting; a
  kind is added at the end of the block, after `OptionPreHandshake`, so no
  published value moves and a `LoopOptionHonourer` compiled against an earlier
  release keeps answering for the same options.
- `Listen` and `Loop` record no kind: an address is not an option an engine
  judges, and `Loop` is judged on its own (ADR 0186 §D1).
- A test walks every function of `pkg/v1/net/server/gen_facade.go`, and of
  `internal/service/net/server`, that returns a `GroupOption`, applies each to
  a fresh group with its zero value and requires exactly its kind; a
  constructor the test does not name fails it, so no option lands without its
  kind.
- `OptionMiddleware` stays outside every loop group: there is no
  `ConnHandler` to decorate, and the handler's `Honours` is not asked about
  it. A test holds it on every backend, a handler claiming every kind
  included. Loop middlewares are a separate record (§Deferred).

### D3 — `Plaintext`, `Name` and `Engine`

Three group options are added, each with its own kind (`OptionPlaintext`,
`OptionName`, `OptionEngine`), and every engine honours all three from this
record on, the loop's included.

- **`Plaintext()`** declares that the group serves no TLS. With `TLS(id)`, in
  either order and whatever the identity, `Start` fails with
  `PlaintextWithTLS`.
- **`Name(n)`** names the group in `State`, logs and errors. A group declared
  with an empty name takes it, and it must then be unique like any other; one
  declared with a name must repeat it. An empty `Name`, or a different one,
  fails `Start` with `GroupNameConflict`.
- **`Engine(e)`** names the engine family: `EngineGoroutine` or `EngineLoop`.
  Its zero value, `EngineAuto`, lets the handler's type choose (D1), which is
  what a group without the option does. `Engine` picks the family;
  `Loop(LoopValue{Backend: …})` picks the backend inside the loop family.

**Zero means the default, for new options only.** `EngineAuto` is the zero
`EngineValue`, and every option added from this record on gives its zero value
the default's meaning (ADR 0031), unless no default can be defensible: then
the zero is refused, the other half of ADR 0031. `Name("")` is that
exception — an empty name names nothing, and a group declared without a name
already has the default — so it fails `Start` with `GroupNameConflict`
rather than meaning "no `Name`"; a caller with no name to give omits the
option. An option published before this record keeps the meaning it has:
`MaxConns(0)` still means no ceiling, and the timeouts' zero still means
unset.

### D4 — A contradiction is refused, never folded back (R6)

An option the group cannot honour, or two options that contradict each other,
fail `Start` with a typed error declared through `errs.Define`'s `0.2.11.*`
block. The SDK never serves the group on another engine, without TLS or with
an option dropped instead:

| Combination | Refusal |
|---|---|
| `Engine(EngineGoroutine)` with `Loop(v)`, or on a group served by `HandleLoop` | `EngineUnsupported` |
| `Engine(EngineLoop)` on a group served by `Handle`, `HandleFunc` or `HandleHTTP`, or on a datagram group | `EngineUnsupported` |
| an `EngineValue` outside the block | `EngineUnsupported` |
| `Plaintext()` with `TLS(id)` | `PlaintextWithTLS` |
| `Name("")`, or a `Name` other than the declared non-empty one | `GroupNameConflict` |
| `Loop(v)` on a goroutine-engine or datagram group, `Use` or an unhonoured kind on a loop group | `LoopOptionUnsupported` (ADR 0186, unchanged) |

ADR 0186's precedence holds: `HandlerMissing` and `HandlerAlreadySet` win over
an option's refusal, because they explain it. An option specific to the loop —
`Loop(v)`, an explicit backend, the loop middlewares to come — combined with
TLS is refused at `Start` too, once §D5 lets an `http1` handler take TLS. A
datagram group refuses every kind it does not honour: from this record on for
`Engine`, the others with the simple API's datagram entry (§D6).

### D5 — An `http1` handler with TLS: `crypto/tls`, then the SDK's codec (D-N6)

ADR 0186 D2 refused TLS on every loop group. For an `http1` handler that
refusal is lifted, and this amends D2 and D3: `crypto/tls` performs the
handshake on the goroutine engine, then the cleartext bytes go through the
SDK's own `http1` codec, on the portable backend split into `Loops` lanes. One
parser serves both paths, so the profile, its refusals, its canonical target
and the lane identity a handler is given are the same with and without TLS.
ALPN offers `http/1.1` only. The portable backend therefore accepts TLS, which
ADR 0186 D3 did not let it do; the native backends do not (no kTLS, no TLS on
the loop, as the owner decided). A raw `LoopHandler` given TLS stays refused
with `LoopTLSUnsupported`, and `http1.HandleLoop(h)` exposes `Unwrap()` so
that the codec can serve `h` on that path. This lands with the simple API,
not with this record.

### D6 — The simple API: `srv.HTTP`, `srv.TCP`, `srv.UDP` (D-N7)

The entry point a consumer writes is `srv.HTTP(addr, h any, …)`,
`srv.TCP(addr, h any, …)` and `srv.UDP(addr, h any, …)`, built on `Server`,
`Group` and `PacketGroup`, resolved at `Start`, a group named `network+addr`
unless `Name` says otherwise. Documentation calls it **the simple API**, so it
is never confused with `pkg/v1/net/server/gen_facade.go`, the generated
facade. Its typed errors:

- a handler implementing two interfaces of the entry point without `Engine`;
- a nil handler, or a typed nil;
- an `Engine` the handler cannot run on;
- a type no engine of the entry point serves (`UDP` with a `LoopHandler`,
  `HTTP` with a `PacketHandler`);
- an HTTP handler passed to `TCP`;
- `Plaintext()` with `TLS(id)`.

### D7 — HTTP without TLS requires `Plaintext()` (D-N8), staged

An HTTP group without TLS must say `Plaintext()`, checked on the HTTP engine
the group resolves to, whatever entry point declared it. **It is staged.**
From this record, `Plaintext()` is declared, honoured by every engine and
checked against `TLS`, but an existing `HandleHTTP` group without it still
starts. The requirement lands with the simple API, which every new HTTP group
goes through; it is a breaking change of its own then, listed below.

### D8 — The environment only restricts (R5)

The operator's switches (`KITSUNIUM_NET_ENGINE`, `KITSUNIUM_NET_DISABLE`,
in their own change) only lower what the code chose, never raise it:

- the engines form a lattice, `iouring` > `epoll` > `portable` >
  `goroutine`, and the effective backend is min(code, environment);
- an unknown token gives min(code, `portable`), with no optional capability,
  never above the code's choice;
- an empty value is "not set";
- `epoll` off Linux is `portable`;
- for an `http1` handler, `goroutine` is `portable` plus the codec (§D5);
- the environment never touches TLS, `Plaintext` or any limit.

### D9 — `Loops`: zero is one (R8)

`LoopValue.Loops` of zero means one loop; `LoopsAuto`, when it lands, is a
sentinel, never the zero value.

### D10 — Gates are placed at the merge (R7)

Every gate this programme adds — functional, capability, ratio of a benchmark
— is a required check of the pull request and of the merge queue, never a
step of the release: every merge publishes (ADR 0186 §D9), so a gate after the
merge holds nothing.

## Consequences / Semantics

- `core/net` gains `EngineValue` (`EngineAuto`, `EngineGoroutine`,
  `EngineLoop`), the kinds `OptionPlaintext` (16), `OptionName` (17) and
  `OptionEngine` (18), and three sentinels, `EngineUnsupported` (`0.2.11.56`),
  `PlaintextWithTLS` (`0.2.11.57`) and `GroupNameConflict` (`0.2.11.58`).
  `net/server` gains `Plaintext`, `Name` and `Engine`; `pkg/v1/net/server`
  re-exports all of them.
- A program that gives no new option and no zero-valued option to a loop group
  sees no difference.
- `Group` and `PacketGroup` apply their options before they check the name for
  a duplicate, so `Name` can set it.

## Breaking changes

Deliberate, and allowed while v0 (ADR 0040):

1. **On a loop group, an option given with its zero value is refused like any
   other it cannot honour.** `ReadTimeout(0)`, `WriteTimeout(0)`,
   `ReadHeaderTimeout(0)`, `MaxHeaderBytes(0)`, `HandshakeTimeout(0)`,
   `ReadBufferSize(0)`, `MaxPacketSize(0)`, `BatchSize(0)`, `Shards(0)` and
   `PreHandshake(nil)` now fail `Start` with `LoopOptionUnsupported` unless the
   handler honours them; `TLS` with a zero identity fails it with
   `LoopTLSUnsupported`; `Use()` with no middleware with
   `LoopOptionUnsupported`. They passed before because they set no field. A
   program that builds its option list conditionally drops the option instead
   of passing its zero.
2. **Staged, with the simple API:** an HTTP group without TLS and without
   `Plaintext()` fails `Start` (§D7); a datagram group refuses every kind it
   does not honour, not only `Loop` and `Engine` (§D4).

## Alternatives considered

- **Keep guessing from non-zero values.** Rejected: an option's argument could
  switch its own refusal off, and every new option would need its own "was it
  given" field.
- **Make `OptionKind` a bit flag.** Rejected: it would renumber every published
  kind and cap the block at the width of the type; the set does the shifting.
- **Fall back to the goroutine engine when the loop cannot serve an option.**
  Rejected (R6): a group that runs elsewhere than it asked, silently, is the
  configuration error this record exists to surface.
- **Require `Plaintext()` now.** Rejected for this step: every existing
  `HandleHTTP` group without TLS would fail `Start` before the simple API
  gives consumers the one-line way to say it.

## Deferred

Each is a separate record:

- **Loop middlewares**: `Middleware[LoopHandler]` through `UseLoop` and its own
  kind, the chain merging the layers' `Honours`.
- **Widening the `http1` profile** (chunked framing, trailers,
  `100-continue`), an amendment of ADR 0186 D6 under ADR 0040, after its step
  12.
- **kqueue and IOCP** backends, each conditioned on a measurement.
- **Native HTTP/2**, after measurement; until then HTTP/2 is `net/http`'s, for
  `http.Handler` groups only.
- QUIC, HTTP/3 and the composite group, their own programme.

## References

- [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md) D1–D3, D5, D9.
- [ADR 0029](0029-sdk-net-domain.md) D2; [ADR 0031](0031-policy-zero-values-are-never-inert.md); [ADR 0040](0040-changing-a-published-shape-while-v0.md).
- Impl: `internal/core/net/` (`EngineValue`, the kinds, the sentinels),
  `internal/service/net/server/` (the kind set, `Plaintext`, `Name`,
  `Engine`, the refusals), `pkg/v1/net/server/`, `design/net.yaml`.
