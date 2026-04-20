// Package logger: codes.go — range 4100-4199 for pkg/v1/logger emissions.
package logger

// range: 4100-4199

// CodeWriterRequired identifies a NewText call with Config.Writer == nil;
// the v1 façade refuses to default silently to stderr.
const CodeWriterRequired int = 4101

// CodeSinkConfigRequired identifies a NewWithSink call with
// SinkConfig.Sink == nil; the v1 façade refuses to default silently to
// a console sink so callers see the misconfiguration immediately. The
// identifier is qualified with "Config" so the AST audit can distinguish
// it from the service-layer sink/handler validation (3104).
const CodeSinkConfigRequired int = 4102
