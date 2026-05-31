// Package level — ParseLevel, the inverse of a lowercased Level.String.
package level

import "strings"

// ParseLevel maps a canonical lowercase level name to its Level constant. It is
// the inverse of Level.String lowercased: "debug", "info", "warn", and "error"
// round-trip to Debug, Info, Warn, and Error respectively. Input is trimmed of
// surrounding whitespace and lowercased before matching, so "INFO" and " Warn "
// resolve too. Any other input yields the zero Level and the LevelUnknown
// sentinel, so callers parse an env/config string then feed the result to Var.Set.
func ParseLevel(name string) (lvl Level, err error) {
	//: normalise casing and surrounding whitespace so env/flag values match.
	switch strings.ToLower(strings.TrimSpace(name)) {
	//: canonical debug name.
	case "debug":
		//: detailed tracing window.
		return Debug, nil
	//: canonical info name.
	case "info":
		//: routine operational window.
		return Info, nil
	//: canonical warn name.
	case "warn":
		//: recoverable-anomaly window.
		return Warn, nil
	//: canonical error name.
	case "error":
		//: failure window.
		return Error, nil
	//: no canonical match — surface the redaction-safe sentinel.
	default:
		//: zero Level + typed sentinel; the offending input is never echoed.
		return Level(0), LevelUnknown
	}
}
