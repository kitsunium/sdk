// Package recover: errors.go declares the sentinels returned by the
// recover Sink. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package recover

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// Panicked is returned when the wrapped downstream sink panics during
	// Write / Flush / Close. The recovered panic value is captured in the
	// error's Fields metadata for downstream introspection.
	Panicked = errs.Define(CodeRecoverPanicked, "RECOVER_PANICKED",
		"Wrapped sink panicked",
		"service/logger/sink/recover caught a panic from the downstream sink")

	// DownstreamNil is returned when New receives a nil downstream sink.
	DownstreamNil = errs.Define(CodeRecoverDownstreamNil, "RECOVER_DOWNSTREAM_NIL",
		"Recover sink requires a non-nil downstream",
		"service/logger/sink/recover.New called with nil downstream")
)
