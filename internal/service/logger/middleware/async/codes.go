// Package async: codes.go — range 0.3.17.* (ADR 0005 service/logger/middleware/async block).
package async

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.17.0 - 0.3.17.255

// CodeAsyncStopped identifies a Write call after Close has terminated the
// drainer goroutine — the buffered queue can no longer accept work.
const CodeAsyncStopped errs.Code = 0x00_03_11_01 // 0.3.17.1

// CodeAsyncBufferFull identifies a Write call where the policy is DropNewest
// and the ring is saturated; the entry is dropped and the OnDrop callback
// fires for the caller's metric pipeline.
const CodeAsyncBufferFull errs.Code = 0x00_03_11_02 // 0.3.17.2

// CodeAsyncCtxCancelled identifies a Write or Flush call whose context was
// already done. Wraps the stdlib context error so the typed-errors-only SDK
// rule is satisfied and consumers can HasCode / errors.Is against it.
const CodeAsyncCtxCancelled errs.Code = 0x00_03_11_03 // 0.3.17.3
