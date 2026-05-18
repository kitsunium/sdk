// Package console — range 0.3.13.* (ADR 0005 service/logger/sink/console block).
package console

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.13.0 - 0.3.13.255

// CodeWriterNil identifies a New call made with a nil io.Writer.
const CodeWriterNil errs.Code = 0x00_03_0D_01 // 0.3.13.1

// CodeCtxCancelled identifies a Write call whose context was already done.
const CodeCtxCancelled errs.Code = 0x00_03_0D_0A // 0.3.13.10 (serial 10)

// CodeWriteFailed identifies a Write call whose underlying io.Writer
// returned a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed errs.Code = 0x00_03_0D_14 // 0.3.13.20 (serial 20)
