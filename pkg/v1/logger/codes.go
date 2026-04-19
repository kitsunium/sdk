// Package logger: codes.go — range 4100-4199 for pkg/v1/logger emissions.
package logger

// range: 4100-4199

// CodeWriterRequired identifies a NewText call with Config.Writer == nil;
// the v1 façade refuses to default silently to stderr.
const CodeWriterRequired int = 4101
