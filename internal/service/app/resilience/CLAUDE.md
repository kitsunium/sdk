<!-- updated: 2026-10-21T12:00:00Z -->
# internal/service/app/resilience/

## Purpose

Concrete reliability policies implementing `core/app/resilience.Runner`: retry,
circuit-breaker, rate-limit, bulkhead, timeout, fallback, hedging, adaptive
throttle. Each
constructor returns a Runner; policies compose by nesting. Stdlib + kernel
`backoff`, `clock` and `errs` + the `core/app/resilience` port only — **no vendor deps**,
cross-OS portable. Wraps outcomes in the `core/app/resilience` sentinels.
ADR 0026; the bounded waits, the jitters, `HalfOpenTrials` and the adaptive
throttle are ADR 0184.

## Contents

| File | Policy | Notes |
|---|---|---|
| `retry.go` / `retry_config.go` | retry | capped exponential backoff (the `BackoffValue` curve), ctx-aware sleep on the injected `Clock`, `RetryExhausted`; `JitterMode` (`curveWait`) — additive (the zero), `kbackoff.Full`, or `kbackoff.Decorrelate` from `previous`, the call's last CURVE wait, kept on `Run`'s stack (an imposed `Delay` is not recorded), ADR 0184; `Delay(attempt, err)` lets the failure impose the wait (no jitter, held into `[0, MaxDelay]` by `capDelay`), nil = the curve (#476); `Budget` records each success and retryable failure and is asked before each retry — a refusal is `RetryBudgetExceeded`; `OnRetry(attempt, err, delay)` is called synchronously before each wait (#475) |
| `retry_budget.go` / `retry_budget_config.go` | retry budget | `NewRetryBudget` — gRFC A6's token bucket implementing `core/app/resilience.RetryBudget`: thousandths of a token in one `atomic.Int64`, full at `MaxTokens`, failure −1, success +`TokenRatio` (capped), `AllowRetry` while strictly more than half remain; CAS loops, no lock, 0 alloc. `refusedBudget` for a non-positive `MaxTokens` or a `TokenRatio` rounding below 0.001 / non-finite — allows nothing, and `NewRetry`/`NewHedge` turn it into `PolicyMisconfigured` naming the knob (ADR 0031, #475) |
| `retry_after.go` | — | `RetryAfter(err)` — reads the `retry_after_seconds` field (`retryAfterField`) of the outermost `*errs.Error`, whole or fractional seconds, rounded to the nanosecond, saturated at the longest `Duration`; zero, negative and non-finite are no hint. Written by the OTLP sender, `net/client` on 429/503 and `rateLimited` — each spelling pinned by its producer's suite (#476) |
| `backoff.go` | — | `BackoffValue` — an alias of `kernel/backoff.Value`, the layer that owns the curve (ADR 0074, ADR 0103); `retryRunner.backoff`/`jittered` delegate to its two halves, `kernel/backoff.Grow` (pure, bounded, never negative) and `Widen` (jitter) |
| `keyed_ratelimit.go` / `keyed_ratelimit_config.go` | keyed rate-limit | one `tokenBucket` per `Key(ctx)`, at most `MaxKeys` (LRU), forgotten after `IdleTimeout` (sliding), no sweeper; `Rate`/`Key`/`MaxKeys`/`IdleTimeout` refused at zero (ADR 0031, ADR 0103) |
| `breaker.go` / `breaker_run.go` / `breaker_config.go` / `breaker_state.go` | circuit-breaker | Closed→Open→HalfOpen (injectable clock), `CircuitOpen`; ONE HalfOpen trial at a time (a deterministic error, a caller's cancellation or a panic frees the slot); each trip, `Isolate` and `Reset` start a generation a call captures on admission, and a straggler's outcome (an older generation) is ignored — ADR 0026 §Deferred, closed by #467; `classify` makes a `context.Canceled` under a closed caller context neutral, before the classifier is asked (#477). `Run` splits three paths — the trial (`runTrial`, the deferred release), the window's (`runTimed`, two clock reads) and the consecutive closed path, which reads no clock and defers nothing. `BreakerConfig.OpenBackoff` sizes the n-th opening since the last close with `kbackoff.Value.Delay(n)` (`cooldownFor`); `BaseDelay`/`MaxDelay` ≤ 0 refused (#480). `HalfOpenTrials` (`trials`, clamped to ≥ 1): `settleTrial` counts `passed` and stays HalfOpen until it reaches `trials`; `moveTo` zeroes `passed` on every change of phase (ADR 0184). `breaker_state.go` aliases the core's `BreakerPhase` so `State` reports what the machine holds |
| `breaker_window.go` | circuit-breaker window | `BreakerWindowValue` resolved (`resolveWindow`: zero → the Polly/resilience4j default, out of range → the field's name for `PolicyMisconfigured`) into a `slidingWindow`: `Buckets` slots of `Size/Buckets` on the breaker's clock, allocated once, running totals kept as slots fill and empty, `headEnd` so a record in the current slot pays no division, a clock set back counted in the latest slot; the ratios read only when one could have risen — a failure, a slow call, a slot dropped, or the call reaching `MinCalls` (#480) |
| `breaker_control.go` | circuit-breaker state | `BreakerState` (core sibling port, ADR 0039): `State` (advances the window to now, changes no phase), `Isolate` (Open with no cooldown until `Reset`), `Reset`; `OnStateChange` delivery — `moveTo` queues each change in the `changeQueue` under `mu`, the first goroutine to `claimDelivery` hands the queue out OUTSIDE `mu`, in order, re-entrant (a listener may call `Reset`), and a listener's panic gives the claim back (#480) |
| `ratelimit.go` / `ratelimit_config.go` | rate-limit | token bucket, `RateLimited` carrying `retry_after_seconds = (1 − tokens) / rate` through `rateLimited` (#476); non-positive `Rate` refused (ADR 0031); `usableRate`/`bucketSize`/`newTokenBucket`/`rateLimited`/`take`/`refill` shared with the keyed limiter. `MaxWait` (ADR 0184): `take` RESERVES a token due within `maxWait` (tokens go negative — later calls queue behind the debt), `await` waits it on `waiter` (`clock.Timed`), a cancelled wait `giveBack`s it; a positive `MaxWait` on a clock that is not `clock.Timed` is refused naming `Clock`. The keyed limiter's `maxWait` stays 0 |
| `bulkhead.go` | bulkhead | buffered-channel semaphore (reject mode), `BulkheadFull` |
| `bulkhead_wait.go` / `bulkhead_wait_config.go` | bulkhead that queues | `NewBulkheadWait` — `slots` and `queue`, two buffered-channel semaphores; a free slot is the reject-mode path (`runHeld`); otherwise a `queue` place or `BulkheadFull`, then `await` races the slot against a `MaxWait` timer on `Clock` and `ctx.Done()` (→ `ctx.Err()`), giving its place back either way. Zero `Queue` or `MaxWait` returns `NewBulkhead(Max)` (ADR 0184) |
| `throttle.go` / `throttle_config.go` / `throttle_window.go` | adaptive throttle | `NewAdaptiveThrottle` — the SRE book's client-side throttling: `refuse` advances the `throttleWindow` (time slots, as the breaker's `slidingWindow`), reads `refusal(k)` = `max(0, (requests − k·accepts)/(requests + 1))` BEFORE counting the request, counts it, and draws `random()` under `mu` only when the probability is positive; `accepted` — nil, `!Rejected(err)`, or a caller who left — counts an accept. `resolveThrottle` applies the defaults (K 2, 2 min, 10 slots) and names the refused field (`K` < 1 or non-finite, `Buckets` outside [0, 1000], `Window` negative or unsplittable) (ADR 0184) |
| `timeout.go` / `timeout_config.go` / `timeout_clock.go` | timeout | `context.WithTimeout`, `TimeoutExceeded` — only for its OWN deadline: a caller's context already finished returns its own error (#477); `NewTimeoutWith(TimeoutConfig{Duration, Clock})` measures the deadline on an injected clock through `clockDeadline`, a context with its own `Done` so a derived context reports `DeadlineExceeded` too (ADR 0090); non-positive duration refused (ADR 0031) |
| `fallback.go` / `fallback_config.go` | fallback | secondary `Operation` on primary failure; `FallbackFailed` carries BOTH errors; nil `Fallback` refused (ADR 0031) |
| `hedge.go` / `hedge_config.go` / `hedge_race.go` / `hedge_outcome.go` | hedging | duplicate copies raced after `Delay`, first success wins; a copy's panic re-raised on the caller's goroutine with its original value; `Idempotent`/`Delay`/`MaxInFlight` refused, `MaxHedges`→1 (ADR 0031); `Budget` asked before each duplicate (`claimSlot`, before the in-flight slot) — a refusal degrades to no-hedging — and each decided call recorded once (`recordOutcome`: success, or failure on a live context) (#475) |
| `wrap.go` | — | `wrapAs(sentinel, cause)` (sentinel origin-wins + cause as a field) over `wrapAsFields(sentinel, fields...)`, the one builder every outcome goes through, `FallbackFailed`'s two halves included |
| `retryable.go` | — | `isRetryable(pred, err)` — nil-predicate default shared by retry + breaker |
| `misconfigured.go` | — | `newMisconfigured(policy, knob)` — refuses every call with `PolicyMisconfigured` (ADR 0031) |

## Conventions

- **No `errs.Define` here** — service emits the `core/app/resilience` sentinels via
  `wrapAs` (origin-wins keeps the policy code even when the cause is an *errs.Error).
- **Injectable clock** (breaker/ratelimit/keyed ratelimit read it; retry,
  hedge and timeout WAIT on it — `RetryConfig.Clock`, `HedgeConfig.Clock` and
  `TimeoutConfig.Clock` are `clock.Timed`, so a test drives every backoff,
  every hedge delay and every deadline with a `ManualClock` instead of
  sleeping; `TestRetryWaitsOnItsClock`, `TestHedgeWaitsOnItsClock` and
  `TestTimeoutWithWaitsOnItsClock` pin each to the nanosecond). `NewTimeout`
  stays on the wall clock, through `context.WithTimeout`, and so does
  `NewTimeoutWith` under a nil clock or `clock.System`.
- **A caller who leaves is not a failing dependency** (#477). The breaker
  folds a `context.Canceled` returned once the caller's context is closed in
  as neutral — it frees a HalfOpen trial and counts neither way — and the
  timeout relabels a deadline as `TimeoutExceeded` only while the caller's
  context is live. `DeadlineExceeded` from the dependency stays a failure.
- **No constructor returns an inert policy** (ADR 0031). A non-positive knob is
  either clamped to a working floor — `MaxAttempts`→1, `Multiplier`→2,
  `FailureThreshold`→5, `OpenDuration`→30s, `Burst`→1, bulkhead limit→1 — or,
  where any SDK-chosen value would be arbitrary, refused — a `BreakerConfig.Window`
  field out of range and an `OpenBackoff` without a base or a ceiling included
  (#480), each named in the refusal. A breaker with a zero
  cooldown admits the next call and so rejects nothing, which is the one failure
  mode that hands back a false sense of protection.
- **Retryable classifier** (`RetryConfig.Retryable` / `BreakerConfig.Retryable`,
  ADR 0026 §Deferred, delivered): one `func(error) bool` shape for both policies
  so a nested `Retry(Breaker(op))` shares a single predicate. `nil` keeps the
  pre-classifier contract (replay everything / count everything as a failure).
  A rejected error is returned **verbatim**: retry spends no budget and skips
  the `RetryExhausted` relabel; the breaker folds it into neither the failure
  count nor the success path, so it cannot trip an Open nor close a HalfOpen.
- **Reject mode by default** for bulkhead/ratelimit: a wait exists only when
  it is bounded twice — `NewBulkheadWait` needs a `Queue` AND a `MaxWait`,
  the limiter a `MaxWait` — and runs on the policy's clock (ADR 0184). A
  caller who leaves while waiting gets `ctx.Err()`, never the rejection
  sentinel: the policy refused nothing.
- Cross-OS: 100 % portable (context/time/sync, no syscall).

## Do NOT

- Call `OnStateChange` while holding `mu`, or from each goroutine after it
  unlocks: the first deadlocks a listener that reads `State`, the second
  delivers two back-to-back changes out of order.
- Read a clock, or defer, on the consecutive breaker's closed path: it is
  what keeps a nil Window within 2 ns of its cost before #480.

- Let the budget refuse a call's FIRST attempt, or make `AllowRetry` spend a
  token. The question is a query; only outcomes move the bucket, and a budget
  that throttles first attempts is a rate limiter that never recovers.

- Relabel an `*errs.Error` cause via plain `errs.Wrap(cause, …)` — origin-wins
  would let the cause hijack the policy code; use `wrapAs`.
- Add an unbounded wait: a bulkhead queue or a limiter wait without its
  bound, or on a clock other than the policy's (ADR 0184 D2).
- Make `RetryConfig.Jitter` apply by default, or move it inside `retryRunner.backoff`. Its
  zero value being "deterministic" is what made it safe to land on a shipped
  policy — every existing caller's timing is unchanged — and keeping `backoff`
  pure is what lets the suite assert the growth curve separately from the
  randomisation.
- Call `cfg.Retryable` with a nil error — the call sites gate on `err != nil`
  first, so the predicate only ever classifies real failures.
- Default `HedgeConfig.Idempotent` to true, or demote it to a doc comment. It is
  the one precondition here that is a property of the *caller's code* rather
  than of a number, and its breach is a silent double effect — the refusal is
  what makes the claim visible and greppable.
- Let `hedge` react to a failure by issuing another copy. That is retrying, it
  is `NewRetry`'s job, and it would make the duplicate load scale with breakage
  instead of with slowness.
- Re-raise a hedged copy's panic as a wrapper to carry its stack. It breaks
  value equality in the caller's `recover` and turns `http.ErrAbortHandler`
  into a logged crash; the lost stack is the documented price.
- Size anything per call from `MaxHedges` — the budget is a count of
  duplicates, and `math.MaxInt` is a value a caller may legitimately write.
- Add adaptive concurrency (Vegas, Gradient2, AIMD) or deadline propagation
  here without an ADR: both change what a policy may do on the caller's
  behalf, and both need measurement rather than a plausible implementation.
  ADR 0184 admitted the SRE book's adaptive THROTTLE only, and defers
  adaptive concurrency explicitly.
- Count a throttled call out of `requests`, or draw `Random` when the
  probability is zero: the first keeps a refusing dependency flooded, the
  second puts a call to `Random` on every healthy call.

## Verification

```
bazel test --config=race //internal/service/app/resilience:resilience_test
```

## Reference in NOTES.md

Moved unchanged to `internal/service/app/resilience/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Backoff — one curve, published, owned by the kernel`; `Keyed rate limit — a bucket per caller, bounded`; `Fallback — which error survives a double failure`; `Hedging — the two hazards, and how each is answered`; `Retry budget — amplification bounded across calls (#475)`; `Breaker — a rate over a window, and a state an operator reads (#480)`; `Bounded waits, three jitters, several trials (ADR 0184)`; `Adaptive throttle — the SRE book's, client side (ADR 0184)`.
