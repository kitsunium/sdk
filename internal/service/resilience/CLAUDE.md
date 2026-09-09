# internal/service/resilience/

## Purpose

Concrete reliability policies implementing `core/resilience.Runner`: retry,
circuit-breaker, rate-limit, bulkhead, timeout, fallback, hedging. Each
constructor returns a Runner; policies compose by nesting. Stdlib + kernel `clock` only — **no vendor
deps**, cross-OS portable. Wraps outcomes in the `core/resilience` sentinels.
ADR 0026.

## Contents

| File | Policy | Notes |
|---|---|---|
| `retry.go` / `retry_config.go` | retry | capped exponential backoff, ctx-aware sleep, `RetryExhausted` |
| `breaker.go` / `breaker_config.go` / `breaker_state.go` | circuit-breaker | Closed→Open→HalfOpen (injectable clock), `CircuitOpen` |
| `ratelimit.go` / `ratelimit_config.go` | rate-limit | token bucket (reject mode), `RateLimited`; non-positive `Rate` refused (ADR 0031) |
| `bulkhead.go` | bulkhead | buffered-channel semaphore (reject mode), `BulkheadFull` |
| `timeout.go` | timeout | `context.WithTimeout`, `TimeoutExceeded`; non-positive `d` refused (ADR 0031) |
| `fallback.go` / `fallback_config.go` | fallback | secondary `Operation` on primary failure; `FallbackFailed` carries BOTH errors; nil `Fallback` refused (ADR 0031) |
| `hedge.go` / `hedge_config.go` / `hedge_race.go` | hedging | duplicate copies raced after `Delay`, first success wins; `Idempotent`/`Delay`/`MaxInFlight` refused, `MaxHedges`→1 (ADR 0031) |
| `wrap.go` | — | `wrapAs(sentinel, cause)` — sentinel origin-wins + cause as a field |
| `retryable.go` | — | `isRetryable(pred, err)` — nil-predicate default shared by retry + breaker |
| `misconfigured.go` | — | `newMisconfigured(policy, knob)` — refuses every call with `PolicyMisconfigured` (ADR 0031) |

## Conventions

- **No `errs.Define` here** — service emits the `core/resilience` sentinels via
  `wrapAs` (origin-wins keeps the policy code even when the cause is an *errs.Error).
- **Injectable clock** (breaker/ratelimit) for deterministic tests.
- **No constructor returns an inert policy** (ADR 0031). A non-positive knob is
  either clamped to a working floor — `MaxAttempts`→1, `Multiplier`→2,
  `FailureThreshold`→5, `OpenDuration`→30s, `Burst`→1, bulkhead limit→1 — or,
  where any SDK-chosen value would be arbitrary, refused. A breaker with a zero
  cooldown admits the next call and so rejects nothing, which is the one failure
  mode that hands back a false sense of protection.
- **Retryable classifier** (`RetryConfig.Retryable` / `BreakerConfig.Retryable`,
  ADR 0026 §Deferred, delivered): one `func(error) bool` shape for both policies
  so a nested `Retry(Breaker(op))` shares a single predicate. `nil` keeps the
  pre-classifier contract (replay everything / count everything as a failure).
  A rejected error is returned **verbatim**: retry spends no budget and skips
  the `RetryExhausted` relabel; the breaker folds it into neither the failure
  count nor the success path, so it cannot trip an Open nor close a HalfOpen.
- **Reject mode** for bulkhead/ratelimit in v1 (no queuing/waiting — deferred).
- Cross-OS: 100 % portable (context/time/sync/atomic).

## Fallback — which error survives a double failure

A fallback that also fails poses the only question the policy really has: which
error does the caller get? **Both.** `Run` returns the package's own
`FallbackFailed` sentinel with the two messages attached as the `primary` and
`fallback` fields.

The three candidates and why the other two lose:

- *Only the fallback's error* — never says what plan B was covering for. The
  originating failure, the one an operator needs, is gone.
- *Only the primary's* — never says that plan B was tried and also broke, so
  the operator debugs a dependency that was only half the story.
- *Either one promoted to the wrap origin* — origin-wins (rule 6) would let an
  `*errs.Error` half hijack the policy's code, which is precisely the hazard
  `wrapAs` exists to prevent. The sentinel must be the origin.

Two failure paths deliberately do **not** reach the fallback: a cancelled
context (the fallback would fail the same way, and the outcome would be
reported as a fallback fault rather than as the cancellation it is — the retry
policy stops on the same condition), and an error the `Retryable` classifier
rejects (a deterministic failure is the caller's own, and serving a substitute
answer for it hides a bug behind a stale success). A **successful** fallback
returns `nil` and the primary error is not surfaced at all — that masking is
the policy; a caller who needs to count activations instruments the fallback
`Operation`, which is their own closure.

## Hedging — the two hazards, and how each is answered

Hedging is the only policy here that runs an `Operation` **concurrently with
itself**, which makes it the only one that can corrupt rather than merely
delay. Two hazards follow, and neither is answerable by the SDK alone:

**1. Duplicated effects.** A non-idempotent operation hedged is a double
charge, a double insert, a duplicate outbound message — and the policy reports
one clean success, so nothing in the code or the errors will ever mention it.
The SDK cannot detect idempotence: it sees a `func(ctx) error`. So the claim is
mandatory and made **in code**, as `HedgeConfig.Idempotent`, whose zero value
refuses the policy. A comment would be the wrong instrument for a hazard whose
symptom is silent success; a required field is visible at the construction site
during review, cannot be reached by copying a config and deleting a line, and
makes `grep -r 'Idempotent:'` enumerate every hedged call path in a codebase.
The port itself (`core/resilience.Operation`) stays silent on idempotence
because six of the seven policies do not need it.

**2. Load amplification.** A dependency that has gone slow makes every
in-flight call want a duplicate at the same instant, so an unbounded hedge adds
load exactly when the dependency has least to spare and deepens the outage it
was meant to hide. Three mechanics bound it:

- `Delay` is **refused** at zero rather than clamped. Zero is what forgetting
  the field yields, and it duplicates every call the instant it starts — the
  policy inverted. The useful value is the caller's measured p95, which the SDK
  cannot guess (the `RateLimiterConfig.Rate` argument, in latency).
- `MaxInFlight` caps duplicates in flight across **all** calls of a Runner, and
  is **refused** when unset. It is the rare knob where both directions of a
  guess are harmful: a conservative SDK default silently stops hedging under
  exactly the load hedging was bought for (inert — ADR 0031's own failure
  mode), and a generous one hands back the amplifier. Reaching the cap degrades
  the policy to **no-hedging**, never to a rejection: the first attempt of every
  call always runs, and a caller who wants rejection under saturation composes
  `NewBulkhead`, whose job that is.
- **Hedging fires on latency, never on failure.** An attempt that fails before
  `Delay` elapses ends the call with that error, verbatim; no duplicate is
  issued. Replaying a failure is retrying, and `NewRetry` already does it —
  `Retry(Hedge(op))` composes the two. Keeping them separate is what keeps the
  added load proportional to *slowness* rather than to *breakage*.

`MaxHedges` is the contrast that locates ADR 0031's clamp/refuse line: "issue
at least one duplicate" is an obvious floor, so a non-positive value clamps to
1 exactly as `Burst` and the bulkhead limit do.

Mechanics: one goroutine per attempt (including the first, so `Run` stays free
to watch the delay elapse) and one ticker per call — **this is the only policy
in the package that is not allocation-trivial**, which is the price of racing.
Losers are cancelled through a shared derived context and their results land in
a channel buffered to the attempt count, so a straggler can neither block nor
leak. When every launched attempt has failed, the **first failure to arrive** is
returned verbatim — no sentinel is minted, by the same first-to-finish rule that
decides a success.

## Do NOT

- Relabel an `*errs.Error` cause via plain `errs.Wrap(cause, …)` — origin-wins
  would let the cause hijack the policy code; use `wrapAs`.
- Add jitter/wait-mode without an ADR note (still-deferred items of ADR 0026;
  the retryable-error classifier is the one that has landed).
- Call `cfg.Retryable` with a nil error — the call sites gate on `err != nil`
  first, so the predicate only ever classifies real failures.
- Default `HedgeConfig.Idempotent` to true, or demote it to a doc comment. It is
  the one precondition here that is a property of the *caller's code* rather
  than of a number, and its breach is a silent double effect — the refusal is
  what makes the claim visible and greppable.
- Let `hedge` react to a failure by issuing another copy. That is retrying, it
  is `NewRetry`'s job, and it would make the duplicate load scale with breakage
  instead of with slowness.
- Add adaptive concurrency (AIMD) or deadline propagation here without an ADR:
  both change what a policy may do on the caller's behalf, and both need
  measurement rather than a plausible implementation.

## Verification

```
bazel test --config=race //internal/service/resilience:resilience_test
```
