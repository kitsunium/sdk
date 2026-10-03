<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/net/

## Purpose

The network domain's contract layer (ADR 0029): the ports, immutable value types
and **every** `0.2.11.*` sentinel for both faces of the domain — inbound
(listeners, connection and datagram handlers, the Server-Sent Events frame as a
value, the WebSocket protocol's vocabulary — opcode, close code, message — the
static file handler's one refusal) and outbound (the guarded HTTP transport).
Reading and writing a wire format is a mechanism and is NOT here (ADR 0160 §4):
the WebSocket frame codec, the close payload, the handshake digest and the
UTF-8 check are `internal/service/net/websocket`'s, the SSE encoder is
`internal/service/net/sse`'s. Concrete behaviour lives in
`internal/service/net/{tlsid,client,server,sse,websocket,static}`; the public
façades are `pkg/v1/net/{tlsid,client,server,sse,websocket,static}`, at the
same paths (ADR 0155).

This package follows the **`proc` shape** (ADR 0016): one core sibling owns one
code block and declares every sentinel; service implementations and façades
`errs.Wrap` them and declare **zero** codes of their own. There is **no
registry** — one canonical implementation per primitive, like `proc` and
`resilience`.

Inside this package the standard library is imported as `stdnet "net"` wherever
it is needed, so a reader never has to disambiguate it from the package's own
name.

## Contents

| File | Surface |
|---|---|
| `codes.go` | the 34 `Code` constants, `0.2.11.1` – `0.2.11.34` |
| `errors.go` | the matching `*errs.Error` sentinels + the local sysexits constants |
| `wrap.go` | `wrapAs(sentinel, cause, fields...)` — origin-wins sentinel wrapping |
| `identity.go` | `IdentityValue` — the opaque, redacting TLS identity + `NewIdentityValue` |
| `identity_params.go` | `IdentityParams` — in-memory TLS material |
| `identity_file_params.go` | `IdentityFileParams` — on-disk TLS material; the twin of `IdentityParams`, loaded by `service/net/tlsid` |
| `client_config.go` | `ClientConfig` — the outbound client's knobs, the counterpart of the inbound `LimitsValue` / `TimeoutsValue` |
| `material.go` | PEM parsing helpers; **the `AppendCertsFromPEM` trap is closed here** |
| `duration.go` | `DurationValue` — config-friendly duration (`"30s"` or nanoseconds) |
| `address.go` | `AddressValue` — one socket to bind |
| `limits.go` | `LimitsValue` — one listener group's resource ceilings; a zero field means the domain default, never unbounded |
| `timeouts.go` | `TimeoutsValue` — per-operation deadlines, one per phase (`Read` / `Write` / `Idle` / `Handshake`) |
| `conn.go` | `Conn` — one accepted stream connection, embedding `net.Conn` — plus the `ConnHandler` port and its `ConnHandlerFunc` adapter |
| `packet.go` | `Packet` — one received datagram — plus the `PacketHandler` port and its `PacketHandlerFunc` adapter |
| `handler_compliance.go` | the compile-time proof that both func adapters satisfy their ports |
| `middleware.go` | `Middleware[H]` + `Chain` — one generic decorator shape for stream and datagram handlers, the first listed outermost |
| `phase.go` | `Phase` — `PhaseNew` / `PhaseStarting` / `PhaseServing` / `PhaseDraining` / `PhaseStopped` + `String` |
| `state.go` | `StateValue` (+ `Degraded`) and `ListenerStateValue` — the server's reported state |
| `policy.go` | `Policy` / `PolicyFunc` — outbound authorisation port |
| `policy_compliance.go` | the compile-time proof that `PolicyFunc` satisfies `Policy` |
| `response.go` | `ResponseValue` — a fully-read outbound response — and `RequestValue`, the request a `Policy` judges |
| `call.go` | `CallValue` — one completed outbound call — and `CallHook`, the observation function port |
| `sse.go` | `SSEEventValue` — the Server-Sent Events frame as a value: `IsZero`, `Validate` (what the format can carry), and `SSEContentType` / `SSEMinRetry` / `SSELastEventIDHeader`; the encoder is `service/net/sse`'s |
| `drain.go` | `WithDrainSignal` / `DrainSignal` — the shutdown signal a long-lived handler observes |
| `websocket.go` | the RFC 6455 vocabulary: `WSGUID`, `WSVersion`, `WSKeyLen`, the header names, `WSMaxControlPayload` (§5.5); the key check and the accept digest are `service/net/websocket`'s |
| `websocket_opcode.go` | `WSOpCode` — the six assigned opcodes, `IsControl` / `Defined` / `String` |
| `websocket_close.go` | `WSCloseCode` — the §7.4.1 registry, `Sendable` / `Echoable`; the close payload's wire form is `service/net/websocket`'s |
| `websocket_message.go` | `WSMessageValue` — the reassembled message and its `OpCode`; its §8.1 UTF-8 rule is checked by the engine |

## Why-this-shape

- **`IdentityValue` is opaque and redacting.** It copies `crypto.Key` /
  `writer.CredentialValue` exactly: value receivers, unexported secret fields,
  and **both** `String()` and `GoString()` returning `"<redacted>"`. `GoString`
  is not optional — `fmt` bypasses `String` for `%#v` and would otherwise dump
  the unexported fields. `ClientConfig` / `ServerConfig` mint a **fresh**
  `*tls.Config` per call so one caller's mutation cannot reach another — and
  "fresh" covers the certificate slice, the ALPN slice and the trust pools, not
  only the outer struct. Sharing those would leave `cfg.Certificates[0] = other`
  and `cfg.RootCAs.AddCert(evil)` reaching every configuration the identity has
  ever minted, including ones already serving traffic. The copy is paid once per
  config — at client construction or listener setup — never per request.
  `NewIdentityValue` copies the caller's `NextProtos` for the same reason:
  retaining it let whoever built the params keep mutating an identity that
  documents itself as opaque and immutable.
- **`parsePool` exists to close a real trap.** `x509.CertPool.AppendCertsFromPEM`
  reports failure through a boolean that is almost universally discarded, so an
  empty, truncated or key-only bundle yields a pool that parses fine and
  **verifies nothing**. Here that is `TLS_MATERIAL_INVALID`. An *absent* bundle
  (nil pool → platform trust store) is deliberately distinct from an *unusable*
  one. The trust-bundle cases of `TestNewIdentityValue`, and `Test_parsePool`,
  are the regression guard: with the boolean ignored, a garbage, key-only or
  truncated bundle would construct without error, and both tests fail.
- **The TLS floor is TLS 1.3 by default and never zero.** A zero-value
  `IdentityValue` still reports TLS 1.3, so a caller who never configured TLS
  does not silently inherit the stdlib default. TLS 1.0/1.1 are refused
  (RFC 8996), not warned about.
- **Mutual TLS without a client CA bundle is refused at construction.** That
  configuration would reject every peer at handshake time; failing at startup is
  the honest behaviour.
- **`Policy` is a port because it is enforced under the call site.** The whole
  value is that implementations run inside the `RoundTripper`, so "read-only"
  is a property of the code rather than a convention. `RequestValue` carries
  `Scheme`/`Host` as well as `Method`/`EscapedPath` because a policy that sees only the
  path cannot defend against a redirect that keeps the path and swaps the origin.
- **`RequestValue` carries the ESCAPED path, never the decoded one.** `url.URL.Path`
  is already percent-decoded, so a policy that judges it accepts `%2e%2e` — which
  the upstream reinterprets as `..`. Judging the decoded form makes a correctly
  written allowlist bypassable. `RawQuery` is carried for the same class of
  reason: a policy often needs to *require* a parameter, not merely permit a path.
- **`UnsafePath` is evaluated before any allowlist pattern.** Go normalises dot
  segments neither in `url.URL` nor in the transport, and an anchored pattern like
  `^/v1/supi/[^/]+$` matches `/v1/supi/..` perfectly well. No caller will think of
  this, so it belongs in the SDK rather than in each consumer.
- **`RequestDenied`'s public message never names the refused path**, so a denial
  cannot leak the private API surface through a log line.
- **`CallHook` is a func, not an interface**, so the domain needs no dependency
  on `logger` or `metrics`; the consumer wires it to whichever it uses.
- **A terminator inside an SSE `ID` or `Name` is refused, never truncated.**
  The format has no escape mechanism: a terminator inside `Data` becomes
  another `data:` line (the encoder's business, in `service/net/sse`), and the
  same absence of escaping makes a terminator inside a single-line field
  unrepresentable. A silently shortened id is a resume token that points at
  the wrong place, so `Validate` refuses it. The check is two `IndexByte`
  calls behind an emptiness test, not `ContainsAny` — `BENCH.md` prints why.
- **An SSE `event:` with no `data:` is refused.** Every client discards a frame
  whose data buffer is empty and discards the event type with it, so a caller
  who names an event would watch it silently not arrive. A retry-only or id-only
  frame is *not* refused — both are meaningful, and an id-only frame is how a
  cursor is advanced without dispatching anything.
- **`WSCloseCode.Sendable` answers for both directions.** A peer that sends 1006
  is committing exactly the protocol error this endpoint must not commit, so one
  predicate governs both and the two cannot drift. `Echoable` exists because
  §5.5.1 says to echo the peer's code and the one code a peer can leave us
  holding — 1005, for a Close with no payload — is a code §7.4.1 forbids on the
  wire.
- **The wire formats are the services', and that is a rule, not a split of
  convenience** (ADR 0160 §4). What stays here is what a second
  implementation would have to share: the opcode, the close code and its
  predicates, the message, the SSE frame and the rules it must satisfy, the
  protocol's constants. Reading or writing bytes — the frame parser that
  refuses rather than tolerates, the word-at-a-time mask, the minimal-length
  rule, the close payload, the accept digest, the UTF-8 check, the SSE
  terminator scan — moved with their tests, fuzz targets and measurements to
  `internal/service/net/websocket` and `internal/service/net/sse`, whose
  `CLAUDE.md` and `BENCH.md` carry the reasoning that used to be here.
- **The drain signal is a context VALUE, never a cancellation.** Cancelling the
  request context would tell every in-flight handler to abandon the response it
  is halfway through, which is the opposite of what a graceful drain exists for.
  A value is additive: a handler that ignores it behaves exactly as before, and
  a handler that holds a connection open indefinitely gets the one piece of
  information it cannot otherwise have. An absent signal is a NIL channel rather
  than an error or a second return value, because receiving from nil blocks
  forever — so a `select` that watches it needs no nil check and simply never
  fires that case.
- **`DurationValue` is a PROMOTION CANDIDATE.** It is domain-neutral and
  stdlib-only, so it belongs in `kernel` or `core/app/config` the moment a second
  domain needs it. It is declared here because the repo's bar for a shared
  primitive is two real consumers arising from an actual duplication, and today
  there is one. The doc comment on the type says so.

## Cost

`BENCH.md` holds this package's one benchmark — `SSEEventValue.Validate`, run
on every frame a stream sends:

| Server-Sent Events | |
|---|---|
| `Validate`, data only | **12.16 ns**, 0 allocs |
| `Validate`, id + event | 36.33 ns, 0 allocs |

The per-byte costs — the WebSocket mask and UTF-8 check, the frame codec, the
SSE terminator scan — moved with the code that pays them, to
`internal/service/net/websocket/BENCH.md` and `internal/service/net/sse/BENCH.md`.

## Error range

`0.2.11.*` — the whole domain, declared here and nowhere else.

| Code | Const / sentinel | Exit |
|---|---|---|
| 0.2.11.1 | `ListenFailed` | 69 |
| 0.2.11.2 | `InvalidAddress` | 64 |
| 0.2.11.3 | `UnsupportedNetwork` | 64 |
| 0.2.11.4 | `ServerClosed` | 69 |
| 0.2.11.5 | `AlreadyStarted` | 70 |
| 0.2.11.6 | `NotStarted` | 70 |
| 0.2.11.7 | `HandlerMissing` | 64 |
| 0.2.11.8 | `HandlerPanic` | 70 |
| 0.2.11.9 | `ConnLimitReached` | 75 |
| 0.2.11.10 | `DrainTimeout` | 75 |
| 0.2.11.11 | `GroupUnknown` | 64 |
| 0.2.11.12 | `GroupDuplicate` | 64 |
| 0.2.11.13 | `SocketAdoptFailed` | 71 |
| 0.2.11.14 | `PacketTooLarge` | 65 |
| 0.2.11.15 | `TLSMaterialInvalid` | 78 |
| 0.2.11.16 | `TLSHandshakeFailed` | 69 |
| 0.2.11.17 | `RequestDenied` | 77 |
| 0.2.11.18 | `ResponseTooLarge` | 65 |
| 0.2.11.19 | `CallFailed` | 69 |
| 0.2.11.20 | `TooManyRedirects` | 69 |
| 0.2.11.21 | `InvalidDuration` | 78 |
| 0.2.11.22 | `UnsafePath` | 77 |
| 0.2.11.23 | `SSEFieldInvalid` | 64 |
| 0.2.11.24 | `SSEFlushUnsupported` | 70 |
| 0.2.11.25 | `SSEStreamClosed` | 69 |
| 0.2.11.26 | `SSEStreamMisconfigured` | 78 |
| 0.2.11.27 | `WSHandshakeFailed` | 65 |
| 0.2.11.28 | `WSUpgradeUnsupported` | 70 |
| 0.2.11.29 | `WSProtocolViolation` | 65 |
| 0.2.11.30 | `WSMessageTooLarge` | 65 |
| 0.2.11.31 | `WSInvalidPayload` | 65 |
| 0.2.11.32 | `WSConnClosed` | 69 |
| 0.2.11.33 | `WSConnMisconfigured` | 78 |
| 0.2.11.34 | `StaticMisconfigured` | 78 |

`0.2.11.35` – `0.2.11.255` reserved.

## Imports allowed

stdlib (`context`, `crypto/tls`, `crypto/x509`, `net`, `net/http`, `slices`,
`strconv`, `strings`, `time`) + `internal/kernel/errs`. `net/http` is here for
`http.Header` in `ResponseValue`, a type the outbound value carries — not for a
status constant. `crypto/sha1`, `encoding/base64`, `encoding/binary`, `math`
and `unicode/utf8` left with the wire formats (ADR 0160 §4). Never
`internal/service/*`, never `pkg/*`, and never
`golang.org/x/sys` or `golang.org/x/net` — the dep-light invariant (ADR 0016 /
ADR 0018) is why the datagram batch path uses raw stdlib `syscall` with cited
ABI constants instead of `x/net/ipv4`.

## Do NOT

- Declare an error code in `internal/service/net/*` or `pkg/v1/net/*`.
  Add it here and wrap it there.
- Add an accessor that returns the raw `[]tls.Certificate`, the private key, or
  the PEM bytes out of `IdentityValue`. `ClientConfig` / `ServerConfig` are the
  only exits, and they hand out a fresh config.
- Drop `GoString()` when adding a new secret-carrying value type — `%#v` is the
  leak that `String()` alone does not close.
- Ignore the boolean from `AppendCertsFromPEM`. That is the bug this package was
  written to prevent.
- Add a plug-in registry. One canonical implementation per primitive (the
  `proc` / `resilience` precedent).
- Put `context` in a struct field or package-level state. It appears only in
  interface signatures, single-method function ports
  (`internal/core/CLAUDE.md`), and the `drain.go` accessors — which take and
  return one, and store nothing.
- Signal a drain by cancelling the request context. See §Why-this-shape.
- Truncate an `ID` or `Name` that cannot carry a terminator: refuse it.
- Put a wire format back here — a parser, an encoder, the mask, the scan. It is
  a mechanism and belongs to the service that speaks it (ADR 0160 §4).

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/net:net_test

# Fallback (go test — quick local iteration)
cd internal/core && GOWORK=off go test -race -cover ./net/...
# expected: coverage ~98%
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- ADR 0047 — WebSocket — `docs/adr/0047-sdk-net-websocket.md`
- ADR 0043 (drain is a signal), ADR 0016 (one-sibling/many-facades),
  ADR 0018 (portability, `x/sys` ban), ADR 0013 (the redacting `Key`
  precedent), ADR 0028 (config decode shape)
