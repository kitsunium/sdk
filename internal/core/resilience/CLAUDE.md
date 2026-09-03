# internal/core/resilience/

## Purpose

Declares the **reliability port**: the ctx-aware `Operation` and the composable
`Runner` interface every policy (retry, circuit-breaker, rate-limit, bulkhead,
timeout) satisfies, plus the typed outcome sentinels. A core sibling admitted by
**ADR 0026** (Phase-B wave). Policies are concrete and live in
`internal/service/resilience`; this package owns only the contract + sentinels,
so policies compose by nesting: `Retry(Breaker(Timeout(op)))`.

Code range: `0.2.8.*` (ADR 0026).

## Contents

| File | Surface |
|---|---|
| `resilience.go` | `Operation func(ctx) error` + `Runner interface { Run(ctx, op) error }` |
| `codes.go` | `Code*` constants — range 0.2.8.* |
| `errors.go` | `RetryExhausted` / `CircuitOpen` / `RateLimited` / `BulkheadFull` / `TimeoutExceeded` / `PolicyMisconfigured` (`errs.Define`) |

## Conventions

- **No registry** — policies are concrete algorithms, not pluggable schemes
  (like `proc`, this core sibling has no registry).
- **`Runner` composes** — a Runner's `Operation` may invoke an inner Runner.
- **`Operation` MUST honour ctx** — timeout/cancellation propagate through it.
- Sentinels carry `EX_TEMPFAIL` (75) — rejections are transient unavailability.
  **The one exception is `PolicyMisconfigured` (`0.2.8.6`), which carries
  `EX_CONFIG` (78)**: a policy built with a configuration it cannot honour is
  permanently broken, so retrying it is pointless (ADR 0031). Code that treats
  resilience errors as a retryable class must exclude it — that is what the
  distinct exit code is for.

## Do NOT

- Put policy bodies here — they live in `service/resilience`.
- Add a retryable-error classifier to the port — that is a policy/caller concern.

## Verification

```
bazel test --config=race //internal/core/resilience:resilience_test
```
