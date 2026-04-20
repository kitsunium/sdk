// Package async: codes.go — range 3700-3799 reserved for the async Sink.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package async

// range: 3700-3799

// CodeAsyncStopped identifies a Write call after Close has terminated the
// drainer goroutine — the buffered queue can no longer accept work.
const CodeAsyncStopped int = 3701

// CodeAsyncBufferFull identifies a Write call where the policy is DropNewest
// and the ring is saturated; the entry is dropped and the OnDrop callback
// fires for the caller's metric pipeline.
const CodeAsyncBufferFull int = 3702
