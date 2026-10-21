package resilience

import coreres "github.com/kitsunium/sdk/internal/core/app/resilience"

// breakerState is the circuit-breaker state machine value: the phase the core
// publishes — coreres.BreakerClosed, BreakerOpen, BreakerHalfOpen —, so State
// reports what the machine holds without a translation.
type breakerState = coreres.BreakerPhase
