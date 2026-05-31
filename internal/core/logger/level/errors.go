// Package level — declares the typed sentinel returned by ParseLevel. The var
// name equals its errs.Define Reason in SCREAMING_SNAKE form; the code lives in
// codes.go (range 0.2.17.*).
package level

import "github.com/kitsunium/sdk/internal/kernel/errs"

// LevelUnknown is returned by ParseLevel when the input string does not name
// one of the four canonical levels (debug / info / warn / error). The offending
// input is intentionally not echoed into the error so a hostile config value
// cannot leak through the message; callers parsing untrusted env/config strings
// get a stable, redaction-safe sentinel matchable via errs.HasCode or errors.Is.
var LevelUnknown = errs.Define(CodeLevelUnknown, "LEVEL_UNKNOWN",
	"log level name is not recognised",
	"internal/core/logger/level.ParseLevel called with an unrecognised name")
