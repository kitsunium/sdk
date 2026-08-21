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

## Conventions

- **No `errs.Define` here** — service emits the `core/resilience` sentinels via
  `wrapAs` (origin-wins keeps the policy code even when the cause is an *errs.Error).
- **Injectable clock** (breaker/ratelimit) for deterministic tests.
- **Reject mode** for bulkhead/ratelimit in v1 (no queuing/waiting — deferred).
- Cross-OS: 100 % portable (context/time/sync/atomic).

## Do NOT

- Relabel an `*errs.Error` cause via plain `errs.Wrap(cause, …)` — origin-wins
  would let the cause hijack the policy code; use `wrapAs`.
- Add jitter/wait-mode without an ADR note (deferred items).

## Verification

```
bazel test --config=race //internal/service/resilience:resilience_test
```
