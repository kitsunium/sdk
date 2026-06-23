# resilience (service)

Concrete retry / circuit-breaker / rate-limit / bulkhead / timeout policies
implementing `internal/core/resilience.Runner`. Stdlib + kernel clock only,
cross-OS portable. Public facade: `pkg/v1/resilience`. ADR 0026. See `CLAUDE.md`.
