// Package trace — range 0.2.20.* (ADR 0051 core/trace block).
package trace

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.20.0 - 0.2.20.255

// CodeInvalidTraceParent identifies a traceparent header that cannot be read as
// a span context: a malformed field, the invalid version "ff", or an all-zero
// trace-id or parent-id — the two values W3C Trace Context declares invalid.
const CodeInvalidTraceParent errs.Code = 0x00_02_14_01 // 0.2.20.1

// CodeInvalidTraceState identifies a tracestate header that does not match the
// W3C list grammar: a key or value outside its character set, more than 32 list
// members, or the same key twice.
const CodeInvalidTraceState errs.Code = 0x00_02_14_02 // 0.2.20.2

// CodeUnknownExporter identifies an Export/Lookup naming a span exporter that no
// imported package has registered.
const CodeUnknownExporter errs.Code = 0x00_02_14_03 // 0.2.20.3

// CodeExportFailed identifies a span exporter that returned an error while
// shipping a batch (the exporter's cause rides the wrap trail).
const CodeExportFailed errs.Code = 0x00_02_14_04 // 0.2.20.4

// CodeDuplicateRegistration identifies a boot-time SpanExporter registry
// collision: a nil exporter, or a distinct exporter claiming a taken Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_14_05 // 0.2.20.5

// CodeInvalidSpanName identifies a Start call with an empty span name. A span's
// name is the low-cardinality operation label every backend groups on, so an
// empty one produces a trace nobody can search for.
const CodeInvalidSpanName errs.Code = 0x00_02_14_06 // 0.2.20.6
