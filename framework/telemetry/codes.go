// The codes of framework/telemetry: range 0.4.3.* (ADR 0149).

package telemetry

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.4.3.0 - 0.4.3.255

// CodeMisconfigured identifies an exporter configuration that cannot be
// honoured: no socket path, a buffer outside its bounds, an ID outside the
// grammar in the node table.
const CodeMisconfigured errs.Code = 0x00_04_03_01 // 0.4.3.1

// CodeRunning identifies a Start on an exporter already started.
const CodeRunning errs.Code = 0x00_04_03_02 // 0.4.3.2
