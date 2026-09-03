# internal/service/resilience/

## Purpose

Concrete reliability policies implementing `core/resilience.Runner`: retry,
circuit-breaker, rate-limit, bulkhead, timeout. Each constructor returns a
Runner; policies compose by nesting. Stdlib + kernel `clock` only — **no vendor
deps**, cross-OS portable. Wraps outcomes in the `core/resilience` sentinels.
ADR 0026.

## Contents

| File | Policy | Notes |
|---|---|---|
| `retry.go` / `retry_config.go` | retry | capped exponential backoff, ctx-aware sleep, `RetryExhausted` |
| `breaker.go` / `breaker_config.go` / `breaker_state.go` | circuit-breaker | Closed→Open→HalfOpen (injectable clock), `CircuitOpen` |
| `ratelimit.go` / `ratelimit_config.go` | rate-limit | token bucket (reject mode), `RateLimited` |
| `bulkhead.go` | bulkhead | buffered-channel semaphore (reject mode), `BulkheadFull` |
| `timeout.go` | timeout | `context.WithTimeout`, `TimeoutExceeded` |
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

## Do NOT

- Relabel an `*errs.Error` cause via plain `errs.Wrap(cause, …)` — origin-wins
  would let the cause hijack the policy code; use `wrapAs`.
- Add jitter/wait-mode without an ADR note (still-deferred items of ADR 0026;
  the retryable-error classifier is the one that has landed).
- Call `cfg.Retryable` with a nil error — both call sites gate on `err != nil`
  first, so the predicate only ever classifies real failures.

## Verification

```
bazel test --config=race //internal/service/resilience:resilience_test
```
