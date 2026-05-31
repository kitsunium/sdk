// Package batcher — range 0.1.5.* (ADR 0014 kernel/batcher block).
package batcher

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.1.5.0 - 0.1.5.255

// CodeBatcherClosed identifies an Add or Flush call made after Close.
const CodeBatcherClosed errs.Code = 0x00_01_05_01 // 0.1.5.1

// CodeBatcherDeliverFailed identifies a deliver closure that returned an error.
const CodeBatcherDeliverFailed errs.Code = 0x00_01_05_02 // 0.1.5.2
