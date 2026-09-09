# resilience (service)

Concrete retry / circuit-breaker / rate-limit / bulkhead / timeout / fallback /
hedging policies implementing `internal/core/resilience.Runner`. Hedging runs
the operation concurrently with itself, so it is correct only on an idempotent
one — `HedgeConfig.Idempotent` makes the caller assert that in code.
Stdlib + kernel clock only, cross-OS portable. Public facade:
`pkg/v1/resilience`. ADR 0026. See `CLAUDE.md`.
