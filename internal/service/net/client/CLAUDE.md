<!-- updated: 2026-09-03T00:00:00Z -->
# internal/service/net/client/

## Purpose

The outbound half of the network domain (ADR 0029): the policy-enforcing HTTP
transport and the concrete policies it evaluates. **Work in progress** — the
policy layer and the path guard have landed; the guarded `RoundTripper`, the
per-phase timeouts, the response size cap and the `Client` itself are next.

Public façade: `pkg/v1/client` (not yet published).

## Contents

| File | Surface |
|---|---|
| `method_policy.go` | `methodPolicy` — closed set of HTTP methods |
| `path_policy.go` | `pathPolicy` — anchored allow patterns |
| `deny_policy.go` | `denyPolicy` — anchored deny patterns, final |
| `conjunction.go` | `conjunction` — every member must allow |
| `path.go` | `checkPath` / `hasDotSegment` — safety checks run *before* patterns |

## Why-this-shape

- **The policy is enforced in the `RoundTripper`, never at the call site.** That
  is the entire point of the design: a caller cannot build a request that skips
  the check, so "this client is read-only" is a property of the code rather
  than a convention the next contributor has to remember. It is also why the
  façade can hand out a raw `*http.Client` without forfeiting the guarantee.
- **Every default is closed.** A `conjunction` with no members refuses. A
  `pathPolicy` with no patterns refuses. A `methodPolicy` with an empty set
  refuses. An allowlist that grew empty by accident — a config key renamed, a
  slice not populated — must close, never open. This is the classic way an
  allowlist silently becomes a passthrough.
- **Composition is by conjunction, never disjunction.** Adding a policy can only
  narrow what is permitted, so a reviewer never has to check whether a new rule
  accidentally widened the surface.
- **`checkPath` runs before any pattern, and that ordering is load-bearing.** An
  anchored pattern is not sufficient on its own: `^/v1/supi/[^/]+$` matches
  `/v1/supi/..` perfectly well, because `[^/]+` matches `..`. The upstream then
  normalises that to a different resource. Go reduces dot segments neither in
  `url.URL` nor in the transport, so nothing else in the stack catches it. This
  came from a real consumer integration, not from theory.
- **Patterns are anchored by the constructor, not by the caller.** Leaving
  anchoring to the caller is exactly the omission that turns an allowlist into a
  sieve, and it is invisible in review.
- **Policies judge the ESCAPED path.** `url.URL.Path` is already
  percent-decoded, so a policy that reads it accepts `%2e%2e`, which the
  upstream reinterprets as `..`. `corenet.RequestValue` therefore carries
  `EscapedPath` and deliberately does not carry the decoded form.
- **A refusal never echoes the path in its public message.** The path is the
  shape of the private API surface; a denial must not leak it into a log line or
  a response. The reason goes in a structured field instead.
- **`RequestValue` is passed by value** so a policy cannot mutate the request it
  is authorising. The consumer's hand-rolled version took a `*url.URL` and they
  flagged it themselves as a mistake.

## Error range

None of its own. Refusals are `corenet.RequestDenied` (`0.2.11.17`) and
`corenet.UnsafePath` (`0.2.11.22`), wrapped with a `why` field. Per ADR 0029 the
service layer declares **no** codes.

## Imports allowed

stdlib (`net/http`, `net/url`, `regexp`, `strings`, `io`, `time`) +
`internal/kernel/*` + `internal/core/net`. Never `pkg/*`.

**Never the codec.** The client must not decode response bodies: depending on
the codec registry would drag mongo-driver, msgpack and cbor into the module
graph of every consumer that only wanted a guarded GET. Decoding belongs above
this layer.

## Do NOT

- Move the policy check out of the transport and into a helper the caller must
  remember to invoke. That converts a guarantee into a convention.
- Let a policy default to allow when it is unconfigured.
- Trust a caller-supplied regexp to be anchored.
- Compare a decoded path.
- Put the refused path in a `Public` message.
- Truncate an over-sized response body — fail. A silent truncation surfaces
  three layers away as an incomprehensible decode error.

## Verification

```
bazel test --config=race //internal/service/net/client:client_test
# Fallback:
cd internal/service && GOWORK=off go test -race -cover ./net/client/...
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- Contract layer — `internal/core/net/CLAUDE.md`
