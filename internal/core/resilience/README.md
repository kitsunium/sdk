# resilience

Package `resilience` declares the SDK reliability port: the ctx-aware
`Operation` and the composable `Runner` interface, plus typed outcome sentinels
(RetryExhausted / CircuitOpen / RateLimited / BulkheadFull / TimeoutExceeded /
FallbackFailed / PolicyMisconfigured).
Concrete policies live in `internal/service/resilience`; facade: `pkg/v1/resilience`.
ADR 0026. See `CLAUDE.md`.
