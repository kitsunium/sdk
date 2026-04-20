// Package logger: codes.go — range 3100-3199 reserved for service/logger
// sentinels. Codes are declared at source as typed constants; the errs
// registry audit verifies uniqueness and range membership.
package logger

// range: 3100-3199

// CodeWriterNil identifies a NewTextHandler call made with a nil io.Writer.
const CodeWriterNil int = 3101

// CodeHandlerNil identifies a New call made with a nil core.Handler.
const CodeHandlerNil int = 3102

// CodeCtxCancelled identifies a Handle call whose context was already done.
const CodeCtxCancelled int = 3110

// CodeWriteFailed identifies a Handle call whose underlying writer returned
// a non-nil error; ExitCode defaults to 74 (EX_IOERR) for this case.
const CodeWriteFailed int = 3120

// CodeEncoderNil identifies a NewHandler call made with a nil Encoder.
const CodeEncoderNil int = 3103

// CodeSinkRequired identifies a NewHandler call made with a nil Sink.
const CodeSinkRequired int = 3104
