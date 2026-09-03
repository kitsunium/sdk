<!-- updated: 2026-09-02T00:00:00Z -->
# internal/core/net/

## Purpose

The network domain's contract layer (ADR 0029): the ports, immutable value types
and **every** `0.2.11.*` sentinel for both faces of the domain — inbound
(listeners, connection and datagram handlers) and outbound (the guarded HTTP
transport). Concrete behaviour lives in `internal/service/net/{tlsid,client,server}`;
the public façades are `pkg/v1/{tlsid,client,server}`.

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
| `codes.go` | the 22 `Code` constants, `0.2.11.1` – `0.2.11.22` |
| `errors.go` | the matching `*errs.Error` sentinels + the local sysexits constants |
| `wrap.go` | `wrapAs(sentinel, cause, fields...)` — origin-wins sentinel wrapping |
| `identity.go` | `IdentityValue` — the opaque, redacting TLS identity + `NewIdentity` |
| `identity_params.go` | `IdentityParams` — in-memory TLS material |
| `identity_file_params.go` | `IdentityFileParams` — on-disk TLS material; the twin of `IdentityParams`, loaded by `service/net/tlsid` |
| `client_config.go` | `ClientConfig` — the outbound client's knobs, beside the inbound `LimitsValue` / `TimeoutsValue` |
| `material.go` | PEM parsing helpers; **the `AppendCertsFromPEM` trap is closed here** |
| `duration.go` | `DurationValue` — config-friendly duration (`"30s"` or nanoseconds) |
| `address.go` | `AddressValue` — one socket to bind |
| `policy.go` | `Policy` / `PolicyFunc` — outbound authorisation port |
| `request.go` | `RequestValue` — what a `Policy` is shown |
| `call.go` | `CallValue` — one completed outbound call |
| `hook.go` | `CallHook` — the observation function port |

## Why-this-shape

- **`IdentityValue` is opaque and redacting.** It copies `crypto.Key` /
  `writer.CredentialValue` exactly: value receivers, unexported secret fields,
  and **both** `String()` and `GoString()` returning `"<redacted>"`. `GoString`
  is not optional — `fmt` bypasses `String` for `%#v` and would otherwise dump
  the unexported fields. `ClientConfig` / `ServerConfig` mint a **fresh**
  `*tls.Config` per call so one caller's mutation cannot reach another.
- **`parsePool` exists to close a real trap.** `x509.CertPool.AppendCertsFromPEM`
  reports failure through a boolean that is almost universally discarded, so an
  empty, truncated or key-only bundle yields a pool that parses fine and
  **verifies nothing**. Here that is `TLS_MATERIAL_INVALID`. An *absent* bundle
  (nil pool → platform trust store) is deliberately distinct from an *unusable*
  one. `TestNewIdentityRejectsUnusableTrustBundle` is the regression guard, and
  it has been mutation-checked: reintroducing the ignored boolean fails it.
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
  `Scheme`/`Host` as well as `Method`/`Path` because a policy that sees only the
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
- **`DurationValue` is a PROMOTION CANDIDATE.** It is domain-neutral and
  stdlib-only, so it belongs in `kernel` or `core/config` the moment a second
  domain needs it. It is declared here because the repo's bar for a shared
  primitive is two real consumers arising from an actual duplication, and today
  there is one. The doc comment on the type says so.

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

`0.2.11.23` – `0.2.11.255` reserved.

## Imports allowed

stdlib (`crypto/tls`, `crypto/x509`, `net`, `time`, `strconv`) +
`internal/kernel/errs`. Never `internal/service/*`, never `pkg/*`, and never
`golang.org/x/sys` or `golang.org/x/net` — the dep-light invariant (ADR 0016 /
ADR 0018) is why the datagram batch path uses raw stdlib `syscall` with cited
ABI constants instead of `x/net/ipv4`.

## Do NOT

- Declare an error code in `internal/service/net/*` or `pkg/v1/{tlsid,client,server}`.
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
  interface signatures and single-method function ports (`internal/core/CLAUDE.md`).

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
- ADR 0016 (one-sibling/many-facades), ADR 0018 (portability, `x/sys` ban),
  ADR 0013 (the redacting `Key` precedent), ADR 0028 (config decode shape)
