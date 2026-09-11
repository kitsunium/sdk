# ADR 0031 — A policy's zero value is a safe default or an explicit refusal, never an inert policy

- **Status**: Accepted
- **Date**: 2026-09-03
- **Deciders**: SDK maintainers
- **Related**: [ADR 0026](0026-sdk-resilience-domain.md) (resilience domain), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (stdout is a protocol channel — the same class of defect on a different surface), [ADR 0007](0007-sdk-release-and-versioning.md) (bump semantics), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (error codes)
- **Amends**: [ADR 0026](0026-sdk-resilience-domain.md) §Decision 2 (the constructors' handling of non-positive configuration)

## Context

Three `resilience` policies could be built with a configuration that left them
unable to do their job, and none of them said so:

| Built as | Measured behaviour |
|---|---|
| `NewCircuitBreaker(BreakerConfig{FailureThreshold: 2})` | trips, then admits the very next call and every call after it — rejects nothing |
| `NewRateLimiter(RateLimiterConfig{})` | admits call 1, then rejects every call forever with `RATE_LIMITED` |
| `NewTimeout(0)` | every operation fails `TIMEOUT_EXCEEDED`, including an instantaneous one |

The three are one defect. A reliability library's whole value is that the caller
can stop thinking about the failure mode once the policy is in place, so a
policy that silently does not work is worse than no policy at all: the caller
has *stopped watching* the thing that is now unguarded. The breaker case is the
worst of the three because it fails **open** — it hands back a false sense of
protection. The other two fail closed, which is survivable, but they still
present a misconfiguration as if it were normal operation: `RATE_LIMITED` from a
limiter that can never admit anything is indistinguishable, at the call site,
from a limiter doing its job.

What makes this a policy question rather than three bug fixes is that the
package was already inconsistent with itself. Every other non-positive knob is
clamped to a working minimum — `MaxAttempts` to 1, `Multiplier` to 2,
`FailureThreshold` to 5, `Burst` to 1, `NewBulkhead`'s limit to 1, a nil `Clock`
to `clock.System`. `OpenDuration`, `Rate` and the `NewTimeout` duration were the
three that were not, and nothing recorded why.

## Decision

**A policy constructor never returns an inert policy.** Given a configuration it
cannot honour, it does exactly one of two things:

1. **Clamp**, when a working default exists that a reader would accept without
   being told the number — the value is a *floor*, not the caller's intent.
2. **Refuse**, when any value chosen on the caller's behalf would be arbitrary —
   the knob *is* the caller's intent, and guessing it silently substitutes the
   SDK's judgement for a decision only the caller can make.

The line between them is whether the SDK can supply the value without inventing
the caller's requirement.

### Applied

- **`BreakerConfig.OpenDuration` → clamp to 30s.** The cooldown is a floor, not
  a requirement: too short slows nothing but recovery, too long delays it, and
  neither breaks the contract. Refusing here would also be incoherent inside a
  single struct, since `FailureThreshold` in the very same constructor already
  defaults to an equally conventional 5. Consistency with the sibling knobs
  outweighs the appeal of strictness.
- **`RateLimiterConfig.Rate` and `NewTimeout`'s duration → refuse.** A rate in
  tokens-per-second and a deadline are the entire content of those two
  policies. There is no defensible SDK-side value: a caller who forgot `Rate`
  and silently received "100/s" would be worse off than one who is told, because
  they would believe they are limited at their intended rate. `Burst` clamps to
  1 precisely because it *does* have an obvious floor ("admit at least one"),
  which is the contrast that locates the line. The test is **finiteness, not
  sign**: NaN and ±Inf pass a `<= 0` comparison and rebuild the very policy
  this refusal removes — NaN poisons the token arithmetic and rejects every
  call, and +Inf flips between rejecting everything and admitting everything
  according to how much time elapsed between two calls. A rate reaches those
  values by ordinary arithmetic (`budget/window` with a zero window), not by a
  caller typing them.

### Later applications

The two policies added after this ADR were designed under it rather than
retrofitted, and one of them extends the rule past numbers:

- **`FallbackConfig.Fallback` (nil) → refuse.** The secondary operation is the
  whole content of the policy. Running the primary and passing its error
  through is the inert case exactly — a policy that does nothing while the
  caller believes plan B is wired up — and a no-op fallback returning nil would
  be worse still, reporting success for work that never ran.
- **`HedgeConfig.Delay` and `MaxInFlight` (non-positive) → refuse.** A zero
  delay duplicates every call the instant it starts, which is not an inert
  policy but an inverted one: a load amplifier under a resilience policy's name.
  `MaxInFlight` is the rarer knob where *both* directions of a guess are
  harmful — a conservative SDK default silently stops hedging under exactly the
  load hedging was bought for, and a generous one restores the amplifier.
  `MaxHedges` clamps to 1 for the same reason `Burst` does.
- **`HedgeConfig.Idempotent` (false) → refuse.** This one extends the ADR from
  *values the SDK cannot pick* to *preconditions the SDK cannot check*. Hedging
  is correct only on an idempotent operation, its breach is a silent double
  effect rather than an error, and no amount of documentation makes a call site
  reviewable. Making the assertion a required field puts it in the diff, in the
  review, and in `grep`. The same instrument, applied to a semantic
  precondition: the zero value is an explicit refusal, never an inert policy.

### How a refusal is delivered

The constructors return a bare `Runner`, not `(Runner, error)`. Changing that
would break every call site to fix a misconfiguration none of them has, so the
refusal is surfaced at first use instead of at construction: a misconfigured
constructor returns a Runner whose every `Run` returns the new
`PolicyMisconfigured` sentinel (`0.2.8.6`, `POLICY_MISCONFIGURED`) **without
executing the operation**, carrying a field naming the policy and the offending
knob. The value is never echoed.

This is fail-closed, matchable with `errs.HasCode`, and — the point —
*distinguishable* from the policy operating normally. `EX_CONFIG` (78) is its
exit code, not the `EX_TEMPFAIL` (75) the other five resilience sentinels carry:
a misconfiguration is permanent, and retrying it is pointless.

## Consequences / Semantics

- **A constructor's return value is now trustworthy.** A `Runner` handed back by
  `NewCircuitBreaker`, `NewRateLimiter` or `NewTimeout` either enforces the
  policy it names or refuses every call saying so. There is no third state in
  which it runs the work while claiming a protection it does not provide.
- **`POLICY_MISCONFIGURED` is the first resilience sentinel that is not
  transient.** Code handling resilience errors as a retryable class must
  exclude it; that is what the distinct exit code — `EX_CONFIG` (78) rather
  than the `EX_TEMPFAIL` (75) its five siblings carry — is for.
- **A refusal names the knob, never the value.** The error carries `policy` and
  `knob` fields; whatever the caller passed is not echoed, so a refusal is safe
  to log wherever the other sentinels are.
- The guard against regression is a test per policy asserting the **observable**
  outcome — a call is actually rejected, the operation did not run — never the
  clamped field value, so it survives a change of mechanism.

## Breaking changes

Three behavioural changes land under this decision: `BreakerConfig.OpenDuration`
clamps to 30s, `RateLimiterConfig.Rate` is refused when it is not a usable rate,
and `NewTimeout`'s duration is refused when non-positive. All three change
behaviour observable through `pkg/v1` for caller code that did not change, so
each is a **minor bump** under ADR 0007 §Bump semantics and each commit carries
the `Release-bump: minor` trailer.

- A caller currently passing `Rate: 0` or `NewTimeout(0)` moves from a silent
  permanent rejection to a named one. Nobody loses a working configuration —
  none of the three was working.
- A caller whose breaker was built without an `OpenDuration` moves from a
  breaker that admitted the call immediately after tripping to one that stays
  open for 30s. That is the first time the policy actually rejects anything, so
  a caller who had adapted to the broken behaviour will now see `CIRCUIT_OPEN`.
- No signature changes. The constructors still return a bare `Runner`, so
  nothing stops compiling; the change is in what that `Runner` does.

## Alternatives considered

- **Change the constructors to `(Runner, error)`.** The textbook answer, and
  rejected: `pkg/v1` is pre-1.0 so it is *permitted*, but it breaks every
  existing call site to report a fault none of them has, and it makes the
  common, correct path pay for the rare, broken one.
- **Panic at construction.** There is repo precedent (`RegisterExporter` panics
  on a duplicate, `assertKind` on a cross-kind reuse). Rejected here: those
  panic on a *process-global registry* corruption at boot, where no caller can
  proceed. A single misconfigured policy value is local, and crashing the
  process is a wildly disproportionate response for a library whose subject is
  keeping services up.
- **Clamp `Rate` and the timeout to some minimum too.** Rejected: it is the
  failure mode this ADR exists to prevent, one step removed. A limiter silently
  running at an SDK-chosen rate is inert in the sense that matters — it is not
  doing the job the caller asked for, and it never says so.
- **Treat `NewTimeout(0)` as "no timeout"**, the `http.Client.Timeout` /
  `net.Dialer.Timeout` convention. Genuinely tempting, and rejected on the
  difference between a *field* and a *constructor*: a zero field on a struct
  that does other work reasonably means "this feature off", but calling
  `NewTimeout` is an unambiguous request for a deadline. Returning something
  that never times out answers a question the caller did not ask.

## Why not extend this to the other clamps

`MaxAttempts: 0 → 1` and `NewBulkhead(0) → 1` were examined and deliberately
left alone. Both still execute the caller's work under a meaningful, if
minimal, policy — one attempt, one concurrent slot. Neither claims a protection
it does not provide, which is the specific harm this ADR names.

## Deferred

- **Refusal at construction, at the `pkg/v2` cut.** `(Runner, error)` is the
  textbook shape and is rejected above only because breaking every call site
  pre-1.0 to report a fault none of them has is a bad trade. At the v2 boundary
  a signature change costs nothing extra, and reporting a misconfiguration
  where it is made beats reporting it at first use. Recorded so the v2 design
  meets the question rather than rediscovering it.
- **Enforcing "a misconfiguration is never retryable" in code.** This ADR states
  that code treating resilience errors as a retryable class must exclude
  `POLICY_MISCONFIGURED`, and the distinct exit code is the marker — but nothing
  in the SDK enforces it. The `Retryable` classifier consults only the
  caller-supplied predicate (and treats a nil predicate as "everything is
  transient"), so a permissive predicate lets an outer retry replay a policy
  that can never work, and a breaker count the refusal towards its threshold.
  Short-circuiting the classifier on the sentinel ahead of both the nil default
  and the predicate would close it. That changes the classifier's published
  contract — a caller predicate would stop being the last word — so it is
  recorded as a decision to take, not folded into a fix batch.

## References

- Impl: `internal/service/resilience/{breaker.go,ratelimit.go,timeout.go,misconfigured.go}`, `internal/core/resilience/{codes.go,errors.go}`; later applications in `internal/service/resilience/{fallback.go,hedge.go}` (see §Later applications).
- ADR 0026 §Decision 2 — the five policies and their constructors.
- ADR 0030 — the sibling decision on defaults that are unsafe rather than inert.
