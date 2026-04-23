// Package file: codes.go — range 0.3.14.* (ADR 0005 service/logger/sink/file block).
package file

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.14.0 - 0.3.14.255

// CodePathEmpty identifies a New call made with an empty file path.
const CodePathEmpty errs.Code = 0x00_03_0E_01 // 0.3.14.1

// CodeOpenFailed identifies a New call whose os.OpenFile invocation failed.
const CodeOpenFailed errs.Code = 0x00_03_0E_02 // 0.3.14.2

// CodeCtxCancelled identifies a Write call whose context was already done.
const CodeCtxCancelled errs.Code = 0x00_03_0E_0A // 0.3.14.10 (serial 10)

// CodeWriteFailed identifies a Write call whose underlying *os.File returned
// a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed errs.Code = 0x00_03_0E_14 // 0.3.14.20 (serial 20)

// CodeSyncFailed identifies a Flush call whose *os.File.Sync returned an
// error; surfaces fsync(2) failures to upstream sinks (e.g. async).
const CodeSyncFailed errs.Code = 0x00_03_0E_1E // 0.3.14.30 (serial 30)

// CodeCloseFailed identifies a Close call whose *os.File.Close returned an
// error; surfaces fclose(2) failures so callers can react.
const CodeCloseFailed errs.Code = 0x00_03_0E_28 // 0.3.14.40 (serial 40)
