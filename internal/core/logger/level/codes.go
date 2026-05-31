// Package level — range 0.2.17.* (ADR 0006 core/logger/level block).
package level

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.17.0 - 0.2.17.255

// CodeLevelUnknown identifies a ParseLevel call whose input does not match any
// of the four canonical lowercase level names (debug / info / warn / error).
const CodeLevelUnknown errs.Code = 0x00_02_11_01 // 0.2.17.1
