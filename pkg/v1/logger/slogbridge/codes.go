// Package slogbridge — range 1.1.1.* (ADR 0005 pkg/v1/logger/slogbridge block).
package slogbridge

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 1.1.1.0 - 1.1.1.255

// CodeLoggerRequired identifies a NewHandler / New call with a nil Logger.
// The bridge refuses to build a handler that discards silently: its whole
// purpose is to guarantee a single pipeline, and a bridge to nowhere would
// break that guarantee precisely where the caller believed it held.
const CodeLoggerRequired errs.Code = 0x01_01_01_01 // 1.1.1.1
