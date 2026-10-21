package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// RateLimitOption configures a [RateLimit].
type RateLimitOption = iapp.RateLimitConfigurer

// BulkheadOption configures a [Bulkhead].
type BulkheadOption = iapp.BulkheadConfigurer

// WaitOption configures a [RateLimit] and a [Bulkhead] alike.
type WaitOption = iapp.WaitConfigurer
