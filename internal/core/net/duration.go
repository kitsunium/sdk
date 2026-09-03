// Package net — the configuration-friendly duration value.
package net

import (
	"strconv"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DurationValue is a time.Duration that round-trips through JSON as either a Go
// duration string ("30s", "1m30s") or a raw nanosecond count. The SDK's config
// loader (ADR 0028) decodes through a JSON round-trip, and encoding/json renders
// a bare time.Duration as nanoseconds — which would force an operator to write
// 30000000000 in a YAML file to mean thirty seconds. This type accepts both and
// always emits the readable form.
//
// PROMOTION CANDIDATE: this type is domain-neutral and stdlib-only, so it
// belongs in internal/kernel or internal/core/config the moment a second domain
// needs it. It is declared here because the repo's bar for a shared primitive is
// two real consumers arising from an actual duplication, and today there is one.
type DurationValue time.Duration

// Duration returns the value as a plain time.Duration.
func (d DurationValue) Duration() time.Duration {
	//: DurationValue is a defined type over time.Duration — the conversion is free.
	return time.Duration(d)
}

// String renders the canonical Go duration form, e.g. "1m30s".
func (d DurationValue) String() string {
	//: delegate to time.Duration so the rendering matches the stdlib exactly.
	return time.Duration(d).String()
}

// MarshalJSON emits the readable duration string so a written-back configuration
// file stays legible.
func (d DurationValue) MarshalJSON() ([]byte, error) {
	//: quote the canonical form; the rendering never contains a character
	//: needing JSON escaping, so a manual quote is safe and allocation-light.
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// UnmarshalJSON accepts a quoted Go duration string ("30s") or a bare number of
// nanoseconds, so both a hand-written config file and a machine-generated one
// decode.
func (d *DurationValue) UnmarshalJSON(b []byte) error {
	//: a quoted token is a duration string; anything else must be numeric.
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		//: parse the unquoted body with the stdlib duration grammar.
		return d.parseString(string(b[1 : len(b)-1]))
	}
	//: bare token — accept an integer nanosecond count.
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		//: neither a duration string nor a nanosecond count.
		return wrapAs(InvalidDuration, err, errs.String("value", string(b)))
	}
	*d = DurationValue(n)
	return nil
}

// parseString decodes the unquoted body of a JSON duration token.
func (d *DurationValue) parseString(s string) error {
	//: an empty string means "unset" and decodes to the zero duration.
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		//: not a valid Go duration literal.
		return wrapAs(InvalidDuration, err, errs.String("value", s))
	}
	*d = DurationValue(parsed)
	return nil
}
