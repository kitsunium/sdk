// Package async: errors.go declares the sentinels returned by the async
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package async

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// Stopped is returned by Write after Close has stopped the drainer
	// goroutine. Subsequent Write calls drop the payload immediately.
	Stopped = errs.Define(CodeAsyncStopped, "ASYNC_STOPPED",
		"Async sink is stopped",
		"service/logger/middleware/async.Write called after Close")

	// BufferFull is returned by Write under the DropNewest policy when the
	// ring is saturated; the entry is dropped and the OnDrop callback fires.
	BufferFull = errs.Define(CodeAsyncBufferFull, "ASYNC_BUFFER_FULL",
		"Async sink ring buffer is full",
		"service/logger/middleware/async.Write saw a saturated ring under DropNewest policy")

	// CtxCancelled wraps a context that was already done when Write or
	// Flush was entered. Provided as a typed sentinel so consumers match
	// via HasCode / errors.Is without reaching for stdlib context errors.
	CtxCancelled = errs.Define(CodeAsyncCtxCancelled, "ASYNC_CTX_CANCELLED",
		"Async sink aborted due to cancellation",
		"service/logger/middleware/async.Write or Flush saw a cancelled context")
)
