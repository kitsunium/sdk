// Package logger: codes.go — range 1.1.0.* (ADR 0005 pkg/v1/logger block).
package logger

// range: 1.1.0.0 - 1.1.0.255

// CodeWriterRequired identifies a NewText call with Config.Writer == nil;
// the v1 façade refuses to default silently to stderr.
const CodeWriterRequired = 0x01_01_00_01 // 1.1.0.1
