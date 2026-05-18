// Package logger — range 0.3.1.* (ADR 0005 service/logger block).
package logger

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.1.0 - 0.3.1.255

// CodeWriterNil identifies a NewTextHandler call made with a nil io.Writer.
const CodeWriterNil errs.Code = 0x00_03_01_01 // 0.3.1.1

// CodeHandlerNil identifies a New call made with a nil core.Handler.
const CodeHandlerNil errs.Code = 0x00_03_01_02 // 0.3.1.2

// CodeEncoderNil identifies a NewHandler call made with a nil Encoder.
const CodeEncoderNil errs.Code = 0x00_03_01_03 // 0.3.1.3

// CodeSinkRequired identifies a NewHandler call made with a nil Sink.
const CodeSinkRequired errs.Code = 0x00_03_01_04 // 0.3.1.4

// CodeCtxCancelled identifies a Handle call whose context was already done.
const CodeCtxCancelled errs.Code = 0x00_03_01_0A // 0.3.1.10 (serial 10)

// CodeWriteFailed identifies a Handle call whose underlying writer returned
// a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed errs.Code = 0x00_03_01_14 // 0.3.1.20 (serial 20)
