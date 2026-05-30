// Package batcher — declares the sentinels returned by the Batcher operations.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package batcher

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// BatcherClosed is returned by Add (and Flush) once Close has run; the
	// batcher no longer accepts items and its ticker, if any, has been joined.
	BatcherClosed = errs.Define(CodeBatcherClosed, "BATCHER_CLOSED",
		"Batcher is closed",
		"internal/kernel/batcher: Add/Flush called after Close")

	// BatcherDeliverFailed wraps a deliver closure error so callers can route on
	// the typed sentinel; the original cause stays reachable via errors.Is.
	BatcherDeliverFailed = errs.Define(CodeBatcherDeliverFailed, "BATCHER_DELIVER_FAILED",
		"Batcher delivery failed",
		"internal/kernel/batcher: the deliver closure returned an error")
)
