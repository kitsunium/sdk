// Package logger: codes.go — range 0.3.1.* (ADR 0005 service/logger block).
// Untyped constants so Go auto-converts to errs.Code at Define call sites.
package logger

// range: 0.3.1.0 - 0.3.1.255

// CodeWriterNil identifies a NewTextHandler call made with a nil io.Writer.
const CodeWriterNil = 0x00_03_01_01 // 0.3.1.1

// CodeHandlerNil identifies a New call made with a nil core.Handler.
const CodeHandlerNil = 0x00_03_01_02 // 0.3.1.2

// CodeCtxCancelled identifies a Handle call whose context was already done.
const CodeCtxCancelled = 0x00_03_01_0A // 0.3.1.10 (serial 10)

// CodeWriteFailed identifies a Handle call whose underlying writer returned
// a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed = 0x00_03_01_14 // 0.3.1.20 (serial 20)
