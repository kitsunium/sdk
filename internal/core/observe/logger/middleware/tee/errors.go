// Package tee — declares the sentinels the dead-letter tee Sink returns.
// Each var's Reason equals its own name, or its code's, in SCREAMING_SNAKE
// form (ADR 0020).
package tee

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// AllBranchesFailed wraps an errors.Join of every primary failure when
	// every primary sink rejected the record.
	AllBranchesFailed = errs.Define(CodeTeeAllBranchesFailed, "ALL_BRANCHES_FAILED",
		"every primary sink rejected the record",
		"service/observe/logger/middleware/tee: all primary sinks failed to write the record")

	// SpillFailed wraps the spill cause when the dead-letter sink rejected a
	// record that every primary had already failed.
	SpillFailed = errs.Define(CodeSpillFailed, "SPILL_FAILED",
		"failed to spill record to the dead-letter sink",
		"service/observe/logger/middleware/tee: the dead-letter spill sink rejected the record")
)
