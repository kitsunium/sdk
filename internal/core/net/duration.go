package net

import (
	"strconv"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// decimalBase is the radix used to read a bare nanosecond count.
	decimalBase int = 10
	// bitSize64 is the width a nanosecond count must fit, matching time.Duration.
	bitSize64 int = 64
	// minQuotedLen is the shortest possible quoted JSON token: the two quotes.
	minQuotedLen int = 2
)

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
func (d DurationValue) MarshalJSON() (encoded []byte, err error) {
	//: quote the canonical form; the rendering never contains a character
	//: needing JSON escaping, so a manual quote is safe and allocation-light.
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// UnmarshalJSON accepts a quoted Go duration string ("30s") or a bare number of
// nanoseconds, so both a hand-written config file and a machine-generated one
// decode.
func (d *DurationValue) UnmarshalJSON(data []byte) error {
	token := string(data)
	//: a quoted token is a duration string; anything else must be numeric.
	if len(data) >= minQuotedLen && data[0] == '"' && data[len(data)-1] == '"' {
		//: parse the unquoted body with the stdlib duration grammar.
		return d.parseString(token[1 : len(token)-1])
	}
	nanos, perr := strconv.ParseInt(token, decimalBase, bitSize64)
	//: neither a duration string nor a nanosecond count.
	if perr != nil {
		//: reject rather than silently decode to zero.
		return wrapAs(InvalidDuration, perr, errs.String("value", token))
	}
	*d = DurationValue(nanos)
	//: a bare integer is taken as nanoseconds, matching encoding/json's own
	//: rendering of a time.Duration.
	return nil
}

// parseString decodes the unquoted body of a JSON duration token.
func (d *DurationValue) parseString(token string) error {
	//: an empty string means "unset" and decodes to the zero duration.
	if token == "" {
		*d = 0
		//: an explicitly empty value is "unset", not an error.
		return nil
	}
	parsed, perr := time.ParseDuration(token)
	//: not a valid Go duration literal.
	if perr != nil {
		//: reject rather than silently decode to zero.
		return wrapAs(InvalidDuration, perr, errs.String("value", token))
	}
	*d = DurationValue(parsed)
	//: the token parsed cleanly.
	return nil
}
