package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// PortPolicyOption guards the calls a port makes: [Retry], [Budget],
// [Breaker], [Hedge], [Idempotent], [Throttle] and [Timeout], given to
// [Port].Guard.
type PortPolicyOption = iapp.PortPolicyConfigurer

// TimeoutOption configures an endpoint, a command, a query and a port
// alike: [Timeout].
type TimeoutOption = iapp.TimeoutConfigurer

// RetryOption configures a [Retry].
type RetryOption = iapp.RetryConfigurer

// BreakerOption configures a [Breaker].
type BreakerOption = iapp.BreakerConfigurer
