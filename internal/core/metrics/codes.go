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
// bound to a DIFFERENT instrument kind (e.g. a Counter name fetched as a Gauge,
// or as an UpDownCounter, which is a different monotonicity on one metric).
const CodeInstrumentKindConflict errs.Code = 0x00_02_09_03 // 0.2.9.3

// CodeInvalidAttribute identifies an instrument fetched with an attribute set
// that cannot name a series: an attribute with an empty Key, the same Key
// twice, or a value no constructor ever set (AttrKindInvalid).
const CodeInvalidAttribute errs.Code = 0x00_02_09_04 // 0.2.9.4

// CodeDuplicateRegistration identifies a boot-time Exporter registry collision:
// a nil exporter, or a distinct exporter claiming an already-registered Name.
const CodeDuplicateRegistration errs.Code = 0x00_02_09_05 // 0.2.9.5

// CodeInvalidTemporality identifies a MeterConfig carrying a Temporality that
// is none of the three declared constants — reachable only by a deliberate cast.
const CodeInvalidTemporality errs.Code = 0x00_02_09_06 // 0.2.9.6

// CodeInvalidDescription identifies a Describe call carrying an empty
// description — a call that would document nothing (ADR 0067).
const CodeInvalidDescription errs.Code = 0x00_02_09_07 // 0.2.9.7

// CodeDescriptionConflict identifies a second, DIFFERENT description bound to
// an instrument name that already has one. A description belongs to the name,
// so two of them means one of the two wiring sites is wrong (ADR 0067).
const CodeDescriptionConflict errs.Code = 0x00_02_09_08 // 0.2.9.8
