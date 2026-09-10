<!-- updated: 2026-09-05T00:00:00Z -->
# internal/service/net/client/

## Purpose

The outbound half of the network domain (ADR 0029): the policy-enforcing HTTP
transport and the concrete policies it evaluates. The guarded `RoundTripper`, the per-phase
timeouts, the response size cap, the concrete policies and the `Client` have all
landed.

Public façade: `pkg/v1/client`.

## Contents

| File | Surface |
|---|---|
| `method_policy.go` | `methodPolicy` — closed set of HTTP methods |
| `path_policy.go` | `pathPolicy` — anchored allow patterns |
| `deny_policy.go` | `denyPolicy` — anchored deny patterns, final |
| `conjunction.go` | `conjunction` — every member must allow |
| `client.go` | `Client`, `New`, `Get`, `Do`, `HTTP` — per-phase timeouts, redirect cap, `readBody`'s bounded use of the peer's `Content-Length`, `canonicalHeaders` |
| `guard.go` | the policy-enforcing `http.RoundTripper` |
| `capped_body.go` | the response ceiling; fails rather than truncating |
| `policies.go` | `AllowMethods` / `AllowPaths` / `DenyPaths` / `Policies` |
| `defaults.go` | the safe defaults filled into `corenet.ClientConfig`, whose type core declares beside the inbound half's `LimitsValue` / `TimeoutsValue` |
| `path.go` | `checkPath` / `hasDotSegment` / `hasEncodedSeparator` — safety checks run *before* patterns; `foldsASCII` / `lowerASCII` fold in place so they allocate nothing |
| — | `Client.resolve` (in `client.go`) refuses a reference carrying its own origin, which resolution would otherwise substitute for the base |

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
- **Encoded separators are refused, and that is a consequence of judging the
  escaped path.** Matching the wire form means `/v1/supi/a%2fb` satisfies
  `^/v1/supi/[^/]+$` as a *single* segment, while an upstream that decodes
  before routing sees `/v1/supi/a/b` — two segments and a different resource
  than the policy believed it authorised. The two ends disagree, so no pattern
  can authorise it honestly and `hasEncodedSeparator` refuses it (`%2f`, `%5c`,
  either case). This was found by mutation-testing the dot-segment guard: the
  mutation did *not* fail, which showed the escaped-path decision was unpinned
  and pointed straight at the real gap.
- **A path that carries its own ORIGIN is refused, not resolved.** RFC 3986
  reads `//other/path` as an authority rather than as a path, so
  `url.URL.ResolveReference` replaces the configured host with it — and the
  built-in policies judge only the method and the path, so nothing downstream
  sees the substitution. `Get("//peer/v1/x")` therefore reached `peer` past an
  `AllowPaths("/v1/x")` policy that authorised it. `resolve` refuses any
  reference with a scheme or a host once a base is configured: the argument is
  documented as a path relative to that base, and anything else is a different
  request than the call site reads as. With no base the caller supplies absolute
  URLs by design, so the check does not apply there.
- **A refusal never echoes the path in its public message.** The path is the
  shape of the private API surface; a denial must not leak it into a log line or
  a response. The reason goes in a structured field instead.
- **`RequestValue` is passed by value** so a policy cannot mutate the request it
  is authorising. The consumer's hand-rolled version took a `*url.URL` and they
  flagged it themselves as a mistake.
- **`EscapedPath` is read ONCE in `RoundTrip` and passed down.** It is not a
  field read: where `u.RawPath` is set — exactly the percent-encoded paths the
  safety checks exist to catch — `url.URL` re-validates and unescapes, and that
  allocates. Reading it twice made the adversarial input cost twice. The
  consequence is that `requestOf` now takes a `string`, so handing it
  `req.URL.Path` compiles and reads fine while silently showing every policy the
  DECODED path; `Test_guard_handsThePolicyTheEscapedPath` is there for that one
  mistake and nothing else.
- **The safety checks fold ASCII in place, never `strings.ToLower`.** The
  lowercase-and-compare spelling allocated a copy of the whole path and of every
  segment for any input carrying an uppercase byte — which `EscapedPath`
  guarantees, since it emits `%2F` and never `%2f`, and which a canonical UUID
  carries on a path that is not adversarial at all. Equivalence with the old
  spelling is not argued in a comment: `path_equivalence_internal_test.go`
  sweeps the whole Unicode code space for the lemma it rests on, then runs both
  implementations side by side over the adversarial corpus and 200 000 seeded
  random paths.
- **The observation record and its closure are built only when a hook exists.**
  A closure assigning into the record forces the record onto the heap, so
  installing both unconditionally charged the DEFAULT configuration — a nil hook
  — two heap allocations per request for an observer that does not exist, while
  a comment two lines away claimed a nil hook cost one comparison. The ceiling
  (`cappedBody`) is still installed unconditionally and must be.
- **The peer's `Content-Length` is a hint, bounded at `maxPresizedRead`.**
  `io.ReadAll` starts at 512 bytes and grows by append, so it allocates roughly
  twice a large body. The obvious repair is worse than the defect: sizing from
  the header up to `MaxResponseSize` lets a ten-byte reply claiming
  `Content-Length: 8388608` reserve 8 MiB per request, for free, times every
  call in flight. So the hint is clamped, ignored when negative, and — the part
  that is not obvious — **ignored entirely above the bound** rather than clamped
  to it, because reserving 64 KiB on the word of a peer claiming 8 MiB still
  hands a liar 64 KiB. The ceiling is untouched: `cappedBody` refuses an
  over-sized body whatever buffer it is handed.
- **Default header names are canonicalised once, in `New`.** `http.Header.Get`
  and `Set` both canonicalise their argument and both allocate for a name that
  is neither already canonical nor one of `net/http`'s interned common names —
  so a `DefaultHeaders` map written `"x-request-source"` cost two allocations per
  request, forever, for a configuration that was never wrong. Nothing documented
  the requirement, which is why the fix is in the constructor rather than in a
  sentence.

## Error range

None of its own. Refusals are `corenet.RequestDenied` (`0.2.11.17`) and
`corenet.UnsafePath` (`0.2.11.22`), wrapped with a `why` field. Per ADR 0029 the
service layer declares **no** codes.

## Imports allowed

stdlib (`bytes`, `net/http`, `net/url`, `regexp`, `strings`, `io`, `time`) +
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
- Let a caller-supplied path carry its own scheme or authority into `resolve`.
  It replaces the base's origin, and no path-and-method policy can see it.
- Truncate an over-sized response body — fail. A silent truncation surfaces
  three layers away as an incomprehensible decode error.
- Size a read buffer from `Content-Length` without a bound, or bound it by
  `MaxResponseSize`. Both let a peer reserve memory it never has to send.
- Memoise `checkPath` across policies by caching on `RequestValue`. It is passed
  by value precisely so a policy cannot mutate what it is authorising; a cache
  field trades a documented security property for an allocation that no longer
  exists.
- Hoist `checkPath` out of the policies into `guard.RoundTrip`. It halves the
  work and looks safer, and it is a behaviour change: a caller composing only
  `AllowMethods(...)` gets no path check today, and hoisting starts refusing
  dot segments for policy sets that previously passed. That is an ADR.
- Replace the `strings.ToLower` spelling in `path.go` without re-running
  `path_equivalence_internal_test.go` against the implementation you removed.
  The reference implementations live in that file for exactly this reason.

## Cost

Measured in `BENCH.md` against a **stub transport**, so these are this package's
own numbers and not the network's. `pkg/v1/client/BENCH.md` prices the
end-to-end call and is the one to read for "what does a request cost"; this is
what is inside the 5.53 % it attributes to `guard.RoundTrip`.

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `guard.RoundTrip` + `Close`, no hook | 195.8 | 64 | **1** |
| `guard.RoundTrip` + `Close`, with a hook | 350.1 | 184 | 3 |
| `guard.RoundTrip` on a `%2F` path | 532.7 | 96 | 2 |
| `checkPath`, plain path | 88.5 | 0 | **0** |
| `checkPath`, uppercase UUID path | 125.7 | 0 | **0** |
| `Policies(AllowMethods, DenyPaths, AllowPaths)`, UUID path | 1 361 | 0 | **0** |

The single allocation left in `RoundTrip` is the `cappedBody`. It is the
response ceiling and it is not optional.

**A pattern list is a linear scan, at two very different slopes.** Per pattern
tried and rejected: **64.2 ns** when it shares a prefix with the request path,
**about 5 ns** when it does not. So it is not the pattern COUNT that decides the
bill, it is prefix similarity × count. Fifty patterns cost 3 436 ns to admit the
last-listed endpoint and 557 ns to refuse a path none of them match.

Two consequences a consumer should know, both in `BENCH.md` §4:

- **Order an allow list hot-first.** It short-circuits on a match, so an
  admitted request costs its POSITION, not the list's length.
- **A deny list is paid in full on every admitted request.** `denyPolicy` can
  only return nil after trying every pattern, so it has no best case. Prefer a
  precise allow list to a broad one plus a long deny list.

`pkg/v1/client/BENCH.md` recommends denying by default and enumerating what you
allow. At fifty patterns that recommendation costs 1.8 % of its own loopback
`Get`, so it **survives** — with those two qualifications, and with the note
that ~500 patterns is where the linear scan stops being noise.

The allocation claims above are gated by `client_alloc_internal_test.go`, which
carries `//go:build !race` — `AllocsPerRun` under the race detector measures the
detector — and therefore runs in exactly one place: the race-off alloc lane.
`//internal/service/net/client:client_test` is listed in
`tools/alloc-lane-targets.txt` for that reason (SDK-wide rule 12). Each gate is
mutation-checked in its own doc comment.

## Verification

```
bazel test --config=race //internal/service/net/client:client_test
# The allocation gates run ONLY in the race-off lane:
bazel test --config=alloc //internal/service/net/client:client_test
# Fallback:
cd internal/service && GOWORK=off go test -race -cover ./net/client/...
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- Contract layer — `internal/core/net/CLAUDE.md`
