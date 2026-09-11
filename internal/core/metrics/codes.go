// Package metrics — range 0.2.9.* (ADR 0027 core/metrics block).
package metrics

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.9.0 - 0.2.9.255

// CodeUnknownExporter identifies an Export/Lookup naming an exporter that no
// imported package has registered.
const CodeUnknownExporter errs.Code = 0x00_02_09_01 // 0.2.9.1

// CodeExportFailed identifies an exporter that returned an error while shipping
// a snapshot (the exporter's cause rides the wrap trail).
const CodeExportFailed errs.Code = 0x00_02_09_02 // 0.2.9.2

// CodeInstrumentKindConflict identifies a Meter call reusing a name already
// bound to a DIFFERENT instrument kind (e.g. a Counter name fetched as a Gauge).
const CodeInstrumentKindConflict errs.Code = 0x00_02_09_03 // 0.2.9.3

// CodeInvalidLabel identifies an instrument fetched with a label set that
// cannot name a series: a label with an empty Key, or the same Key twice.
const CodeInvalidLabel errs.Code = 0x00_02_09_04 // 0.2.9.4

// CodeDuplicateRegistration identifies a boot-time Exporter registry collision:
// a nil exporter, or a distinct exporter claiming an already-registered Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_09_05 // 0.2.9.5
