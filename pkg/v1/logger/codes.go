// Package logger — range 1.1.0.* (ADR 0005 pkg/v1/logger block).
package logger

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 1.1.0.0 - 1.1.0.255

// CodeWriterRequired identifies a NewText call with Config.Writer == nil;
// the v1 façade refuses to default silently to stderr.
const CodeWriterRequired errs.Code = 0x01_01_00_01 // 1.1.0.1

// CodeSinkConfigRequired identifies a NewWithSink call with
// SinkConfig.Sink == nil; the v1 façade refuses to default silently to
// a console sink so callers see the misconfiguration immediately. The
// identifier is qualified with "Config" so the AST audit can distinguish
// it from the service-layer sink/handler validation (0.3.1.4).
const CodeSinkConfigRequired errs.Code = 0x01_01_00_02 // 1.1.0.2

// CodeWriterSpecInvalid identifies a NewMulti call with no WriterSpec entries;
// the façade refuses to build a logger that fans out to nothing (ADR 0012).
const CodeWriterSpecInvalid errs.Code = 0x01_01_00_03 // 1.1.0.3

// CodeTopologyInvalid identifies a FromConfig call whose config blob is
// malformed, names an unregistered writer, or whose Factory/DecodeConfig
// rejected its options. The error path is redacted: it names only the writer
// and the failure kind, never any decoded credential or option value (ADR 0014).
const CodeTopologyInvalid errs.Code = 0x01_01_00_04 // 1.1.0.4
