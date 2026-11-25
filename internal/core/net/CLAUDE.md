<!-- updated: 2026-10-26T12:00:00Z -->
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

**The ports are generated from the design** (ADR 0163): `Conn`, `ConnHandler`,
`Packet`, `PacketHandler`, `Policy`, `LoopIO`, `LoopHandler`,
`LoopOptionHonourer`, `LoopStarter`, `LoopSpillChecker`, `LoopLayered`, `HTTP1Handler`,
`HTTP1Batcher`, `HTTP1BodyReader`, `HTTP1BodyAborter`, `HTTP1BearerVerifier` and `HTTP1AccessLogger` are declared, with their doc comments,
under `ports:` in `design/net.yaml`, and `kit gen` writes them into
`gen_ports.go`. A port or its doc comment changes in the design, then `kit
gen`, then `make api` — never in `gen_ports.go`, whose header digests `make
api-check` verifies. They moved there from `conn.go`, `packet.go` and
`policy.go`, content moved and never deleted; each file keeps its port's func
adapter.

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
| 0.2.11.35 | `LoopTLSUnsupported` | 78 |
| 0.2.11.36 | `LoopOptionUnsupported` | 78 |
| 0.2.11.37 | `LoopBackendUnavailable` | 69 |
| 0.2.11.38 | `LoopNoDescriptors` | 71 |
| 0.2.11.39 | `LoopStaleRef` | 70 |
| 0.2.11.40 | `LoopOutputFull` | 75 |
| 0.2.11.41 | `LoopSpillFull` | 75 |
| 0.2.11.42 | `LoopPostQueueFull` | 75 |
| 0.2.11.43 | `LoopWrongThread` | 70 |
| 0.2.11.44 | `HandlerAlreadySet` | 64 |
| 0.2.11.45 | `HTTP1RequestMalformed` | 65 |
| 0.2.11.46 | `HTTP1TransferEncoding` | 65 |
| 0.2.11.47 | `HTTP1ContentLengthInvalid` | 65 |
| 0.2.11.48 | `HTTP1FieldDuplicated` | 65 |
| 0.2.11.49 | `HTTP1ExpectRefused` | 65 |
| 0.2.11.50 | `HTTP1HeadTooLarge` | 65 |
| 0.2.11.51 | `HTTP1TargetTooLong` | 65 |
| 0.2.11.52 | `HTTP1BodyTooLarge` | 65 |
| 0.2.11.53 | `HTTP1MethodUnsupported` | 65 |
| 0.2.11.54 | `HTTP1ResponseInvalid` | 64 |
| 0.2.11.55 | `HTTP1Misconfigured` | 78 |
| 0.2.11.56 | `EngineUnsupported` | 78 |
| 0.2.11.57 | `PlaintextWithTLS` | 78 |
| 0.2.11.58 | `GroupNameConflict` | 64 |
| 0.2.11.59 | `LoopGroupStalled` | 69 |
| 0.2.11.60 | `LoopCapabilityRefused` | 78 |
| 0.2.11.61 | `LoopMiddlewareInvalid` | 78 |
| 0.2.11.62 | `ProtocolsRefused` | 78 |
| 0.2.11.63 | `LoopOutboundFull` | 75 |
| 0.2.11.64 | `HandlerAmbiguous` | 64 |
| 0.2.11.65 | `HandlerUnsupported` | 64 |
| 0.2.11.66 | `PlaintextRequired` | 78 |
| 0.2.11.67 | `PacketOptionUnsupported` | 78 |
| 0.2.11.68 | `OptionValueInvalid` | 78 |
| 0.2.11.69 | `MetricsRefused` | 78 |
| 0.2.11.72 | `ProxyHeaderInvalid` | 65 |
| 0.2.11.73 | `CompressInvalid` | 78 |
| 0.2.11.80 | `CORSMisconfigured` | 78 |

`0.2.11.74` – `0.2.11.255` reserved, `0.2.11.70` and `.71` held by L2 and unassigned. The `HTTP1*` refusals carry their
answer's status in `http:` (400, 413, 414, 417, 431, 501).

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

## Reference in NOTES.md

Moved unchanged to `internal/core/net/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Contents`; `Why-this-shape`; `Reference`.
