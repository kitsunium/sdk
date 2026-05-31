// Package tee — range 0.3.29.* (ADR 0014 service slot 0x1d).
package tee

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.29.0 - 0.3.29.255

// CodeTeeAllBranchesFailed identifies a record that every primary sink
// rejected — the trigger for routing it to the dead-letter spill sink.
const CodeTeeAllBranchesFailed errs.Code = 0x00_03_1D_01 // 0.3.29.1

// CodeSpillFailed identifies a failure to deliver an all-primaries-failed
// record to the dead-letter spill sink.
const CodeSpillFailed errs.Code = 0x00_03_1D_02 // 0.3.29.2
