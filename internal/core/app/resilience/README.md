# resilience

Package `resilience` declares the SDK reliability port: the ctx-aware
`Operation` and the composable `Runner` interface, plus typed outcome sentinels
(RetryExhausted / CircuitOpen / RateLimited / BulkheadFull / TimeoutExceeded /
FallbackFailed / PolicyMisconfigured).
Concrete policies live in `internal/service/app/resilience`; facade: `pkg/v1/app/resilience`.
ADR 0026. See `CLAUDE.md`.

The `Runner` port is generated from `design/app/resilience.yaml` into
`design_gen.go` (ADR 0163): it changes in the design, then `kit gen`.
