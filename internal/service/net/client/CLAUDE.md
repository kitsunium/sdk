<!-- updated: 2026-09-28T19:19:15Z -->
# internal/service/net/client/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/client/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why-this-shape.

## Purpose

The outbound half of the network domain (ADR 0029): the policy-enforcing HTTP
transport and the concrete policies it evaluates. The guarded `RoundTripper`, the per-phase
timeouts, the response size cap, the concrete policies and the `Client` have all
landed.

Public façade: `pkg/v1/net/client`.

## Error range

None of its own. Refusals are `corenet.RequestDenied` (`0.2.11.17`) and
`corenet.UnsafePath` (`0.2.11.22`), wrapped with a `why` field. Per ADR 0029 the
service layer declares **no** codes.

## Imports allowed

stdlib (`bytes`, `net/http`, `net/url`, `regexp`, `strings`, `io`, `time`) +
`internal/kernel/*` + `internal/core/net`. Never `pkg/*`.

**Never the codec.** The client must not decode response bodies: depending on
the codec registry would link every codec the SDK ships into every
consumer that only wanted a guarded GET. Decoding belongs above
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
- Follow a redirect to another scheme, host or port unless `RedirectOrigins`
  names it, or send `DefaultHeaders` to any origin but the base's — not even
  to an origin the caller opened.
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
own numbers and not the network's. `pkg/v1/net/client/BENCH.md` prices the
end-to-end call and is the one to read for "what does a request cost"; this is
what is inside the 1.17 % of allocated objects it attributes to
`guard.RoundTrip`.

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

`pkg/v1/net/client/BENCH.md` recommends denying by default and enumerating what you
allow. At fifty patterns that recommendation costs 1.8 % of its own loopback
`Get`, so it **survives** — with those two qualifications, and with the note
that ~500 patterns is where the linear scan stops being noise.

The allocation claims above are gated by `client_alloc_internal_test.go`, which
carries `//go:build !race` — `AllocsPerRun` under the race detector measures the
detector — and therefore runs only with the detector off: in the race-off alloc
lane, and again in CI's `test-386` job, which has no `-race`.
`//internal/service/net/client:client_test` is listed in
`tools/alloc-lane-targets.txt` for that reason (SDK-wide rule 12). Each gate is
mutation-checked in its own doc comment.

## Verification

```
bazel test --config=race //internal/service/net/client:client_test
# The allocation gates run ONLY race-off (here, and in CI's test-386 job):
bazel test --config=alloc //internal/service/net/client:client_test
# Fallback:
cd internal/service && GOWORK=off go test -race -cover ./net/client/...
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- Contract layer — `internal/core/net/CLAUDE.md`
