# internal/service/net/client — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Contents

| File | Surface |
|---|---|
| `method_policy.go` | `methodPolicy` — closed set of HTTP methods |
| `path_policy.go` | `pathPolicy` — anchored allow patterns |
| `deny_policy.go` | `denyPolicy` — anchored deny patterns, final |
| `conjunction.go` | `conjunction` — every member must allow |
| `client.go` | `Client`, `New`, `Get`, `Do`, `HTTP` — per-phase timeouts, redirect cap and origin check (`redirectLimiter`, `parseOrigins`, `sameOrigin`, `stripDefaults`), `readBody`'s bounded use of the peer's `Content-Length`, `canonicalHeaders` |
| `status.go` | `statusError` — the `CallFailed` of a non-2xx response: its `status` field, and on a 429 or a 503 whose `Retry-After` holds positive delta-seconds the `retry_after_seconds` field `app/resilience.RetryAfter` reads (#476); an HTTP-date is not read (it would compare the server's clock to ours). `retry_after_external_test.go` pins the key against `RetryAfter` |
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
- **A redirect stays on its origin, and the defaults never leave it (#448).**
  Every hop re-enters the guard, but the built-in policies judge the method and
  the path, so a 302 that kept the path and swapped the origin — another host,
  `169.254.169.254`, https turned into http — passed all of them, and net/http
  copies the first request's headers onto each hop, stripping only
  `Authorization`, `WWW-Authenticate` and `Cookie`: an API key configured in
  `DefaultHeaders` reached whatever peer the upstream named. `redirectLimiter`
  now refuses (`RequestDenied`, `why` "the redirect leaves the origin") any hop
  whose scheme, host or port differs from the chain's first request, unless
  `ClientConfig.RedirectOrigins` names that origin — an opt-in, empty by default
  (ADR 0031), each entry a bare `scheme://host[:port]` checked in `New`
  (`parseOrigins`, `InvalidAddress` otherwise; a path is refused rather than
  ignored, since it would read as narrower than the origin it opens). An
  admitted hop is stripped of every header still carrying a default's value
  (`stripDefaults`), and `applyHeaders` adds the defaults only to a request for
  the base's origin. Each hop's headers are a fresh copy of the first request's,
  so a chain that comes home carries them again on the hop that returns.
  `sameOrigin` compares substrings — scheme and host without case, an omitted
  port read as the scheme's default — and allocates nothing, which is what lets
  `applyHeaders` run it per request.
- **The probe byte saturates (#453).** `cappedBody.Read` reads one byte past
  the ceiling to tell "ended on the boundary" from "crossed it"; at
  `MaxResponseSize = math.MaxInt64` the sum `limit - read + 1` wrapped negative
  and `p[:remaining]` panicked on the caller's goroutine. The increment is
  skipped at `math.MaxInt64`, a budget no buffer reaches.
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
- **A call is timed on the guard's clock.** `CallValue.Duration` is read through
  `kernel/clock` — `clock.System` for every guard `New` builds, an interface
  call on a zero-size value that allocates nothing — and a white-box test sets a
  `ManualClock`, so `Test_guard_timesTheCallOnItsClock` asserts the exact
  duration a transport spent rather than a wall-clock tolerance.
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
