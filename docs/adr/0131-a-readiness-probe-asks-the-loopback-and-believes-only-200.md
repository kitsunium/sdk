# ADR 0131 — a readiness probe asks the loopback, and believes only 200

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Related**: [ADR 0060](0060-sdk-health-domain.md) (the health domain), [ADR 0072](0072-health-bounds-every-wait-it-owns.md) (a budget on the injected clock), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md)

## Context

The health domain answers the three probes; nothing in the SDK ASKS one. A
container image built from a Go binary has no shell and no curl, so its
HEALTHCHECK runs the binary itself — `app healthcheck` — which must ask the
running process whether it is ready. A downstream framework wrote that client
by hand: split the listen address, replace an unspecified host by `127.0.0.1`,
GET a path with a three-second timeout through `http.DefaultClient`, exit 0 on
200. Read against what a probe must not do:

- `http.DefaultClient` follows redirects, so a 302 to another host is followed
  and its 200 believed.
- `http.DefaultTransport` reads `HTTP_PROXY`: a probe of this machine can leave
  it (only a loopback address or `localhost` is exempt).
- `::` was mapped to `127.0.0.1`, which an IPv6-only listener does not accept.
- The body was closed unread, so every probe of a server still writing met a
  reset, and a body that never ends was not bounded.
- Every failure was the same exit status, with nothing saying why.

## Decision

`health.Ask(ctx, AskConfig) (status int, err error)` in `internal/service/health`,
published by `pkg/v1/health`.

- **The address is the one the process LISTENS on.** An unspecified host — empty,
  `0.0.0.0`, its IPv4-mapped spelling, `::`, a zoned `::` — is not an address
  a connection can be made to (Windows refuses it outright), so it becomes this
  machine's loopback of the same family: `127.0.0.1` for an empty host and for
  IPv4, `::1` for IPv6. Any other host is dialled as written. The port must be a
  number from 1 to 65535.
- **Ready means 200.** `status` is what the process answered, zero when it
  answered nothing, and `err` is nil exactly when `status` is 200.
- **No redirect is followed** — a redirect is an answer, reported with its
  status. **No proxy is consulted**, whatever the environment says. **No
  connection is kept.** The response header is bounded to 64 KiB, the body read
  to at most `MaxAskDrainBytes` (64 KiB) and closed, and no byte of it ever
  reaches an error.
- **Bounded time, on the injected clock.** The budget is `AskConfig.Timeout`,
  else `DefaultAskTimeout` (3 s: far above a readiness endpoint answering from
  memory, an order of magnitude under Docker's own 30 s HEALTHCHECK timeout, so
  the probe reports why before its supervisor gives up). It is armed on
  `AskConfig.Clock` like every budget in this package — the audit that forbids a
  wall-clock wait in `service/health` covers `Ask` — and it ends the exchange by
  cancelling its context with `AskTimeout` as the cause. The caller's own
  context bounds it too. A negative timeout is refused; zero is the default,
  never "no time at all" (ADR 0031).
- **Four codes in the package's own `0.3.59.*`**: `ASK_MISCONFIGURED`
  (`0.3.59.7`, exit 78: nothing is dialled, an `argument` field names what),
  `ASK_UNREACHABLE` (`0.3.59.8`: the transport's error is the cause),
  `ASK_TIMEOUT` (`0.3.59.9`: the budget, with a `budget` field; or the caller's
  context, whose error stays in the chain — the precedent `CHECK_TIMEOUT` set
  for a departed caller), `ASK_NOT_READY` (`0.3.59.10`: a `status` field). Each
  carries the dialled `target`.

## Consequences

- The framework's `healthcheck` becomes one call, and can print why a probe
  failed; an image built on it can probe an IPv6-only listener.
- A HEALTHCHECK cannot be redirected, proxied, or held by a body.

## Breaking changes

None. `Ask` and its configuration, constants and sentinels are new.

## Alternatives considered

- **The SDK's guarded `client`.** It reads the whole body into a value and
  refuses one past its bound, which turns a 200 with a large body into a
  failure; a probe wants the status and a polite close.
- **`Probe` as the name.** `health.Probe` already names the three questions a
  registry answers.
- **`localhost` for an unspecified host.** It needs a resolver and an
  `/etc/hosts` a minimal image may not have; a literal loopback needs neither.
- **An error alone, the status only as a field.** A command printing why it
  failed would have to walk the fields for the one number it needs.

## Deferred

- TLS: a probe of a listener that serves only TLS, with the trust decision it
  needs.
- A HEAD probe, for an endpoint that answers GET with a body worth skipping.

## Verification

- `internal/service/health/ask_external_test.go` — against loopback servers:
  200 and only 200, the status field and no byte of the body in any rendering, a
  redirect reported and its target never asked, a refused connection, the budget
  reached on a manual clock, the caller's cancellation and an expired deadline
  (nothing asked), a body that never ends, the IPv4 and IPv6 unspecified hosts,
  each configuration refused before dialling, a query kept.
- `ask_internal_test.go` — the listen-to-dial mapping over every spelling, and
  the client's posture: no proxy, no keep-alive, the header bound, no redirect.
- `pkg/v1/health/ask_external_test.go` — `Ask` asking the readiness handler the
  same package serves, before and after `Drain`.

## References

- [Docker HEALTHCHECK](https://docs.docker.com/reference/dockerfile/#healthcheck)
- [`net/http.Client.CheckRedirect`](https://pkg.go.dev/net/http#Client), [`http.ProxyFromEnvironment`](https://pkg.go.dev/net/http#ProxyFromEnvironment)
