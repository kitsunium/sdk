// Package failover: errors.go declares the sentinels returned by the
// failover Sink. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package failover

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// Exhausted wraps an errors.Join of every per-sink failure when the
	// failover chain has tried every downstream Sink without success.
	Exhausted = errs.Define(CodeFailoverExhausted, "FAILOVER_EXHAUSTED",
		"All failover sinks returned an error",
		"service/logger/middleware/failover.Write tried every downstream sink and aggregated their errors")

	// Empty is returned when New is invoked without any downstream sinks.
	Empty = errs.Define(CodeFailoverEmpty, "FAILOVER_EMPTY",
		"Failover requires at least one downstream sink",
		"service/logger/middleware/failover.New called with zero sinks")
)
