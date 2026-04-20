// Package file: codes.go — range 3500-3599 reserved for the file Sink.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package file

// range: 3500-3599

// CodePathEmpty identifies a New call made with an empty file path.
const CodePathEmpty int = 3501

// CodeOpenFailed identifies a New call whose os.OpenFile invocation failed.
const CodeOpenFailed int = 3502

// CodeCtxCancelled identifies a Write call whose context was already done.
const CodeCtxCancelled int = 3510

// CodeWriteFailed identifies a Write call whose underlying *os.File returned
// a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed int = 3520

// CodeSyncFailed identifies a Flush call whose *os.File.Sync returned an
// error; surfaces fsync(2) failures to upstream sinks (e.g. async).
const CodeSyncFailed int = 3530

// CodeCloseFailed identifies a Close call whose *os.File.Close returned an
// error; surfaces fclose(2) failures so callers can react.
const CodeCloseFailed int = 3540
