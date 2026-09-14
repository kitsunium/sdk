// Package level — range 0.2.17.* (ADR 0006 core/logger/level block).
package level

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.17.0 - 0.2.17.255

// CodeLevelUnknown identifies a ParseLevel call whose input does not match any
// of the four canonical lowercase level names (debug / info / warn / error).
const CodeLevelUnknown errs.Code = 0x00_02_11_01 // 0.2.17.1

// LevelUnknown is returned by ParseLevel when the input string does not name
// one of the four canonical levels (debug / info / warn / error). The offending
// input is intentionally not echoed into the error so a hostile config value
// cannot leak through the message; callers parsing untrusted env/config strings
// get a stable, redaction-safe sentinel matchable via errs.HasCode or errors.Is.
var LevelUnknown = errs.Define(CodeLevelUnknown, "LEVEL_UNKNOWN",
	"log level name is not recognised",
	"internal/core/logger/level.ParseLevel called with an unrecognised name")
