// Package encwrite — range 0.3.28.* (ADR 0014 service slot 0x1c).
package encwrite

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.28.0 - 0.3.28.255

// CodeEncWriteSealFailed identifies a record whose per-sink subkey could not be
// derived or whose bytes could not be sealed before delivery to the downstream
// sink.
const CodeEncWriteSealFailed errs.Code = 0x00_03_1C_01 // 0.3.28.1

// CodeFramingFailed identifies a sealed box too large for the 4-byte big-endian
// length prefix the byte framing prepends.
const CodeFramingFailed errs.Code = 0x00_03_1C_02 // 0.3.28.2
