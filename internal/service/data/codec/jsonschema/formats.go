package jsonschema

import (
	"net/mail"
	"time"

	corejsonschema "github.com/kitsunium/sdk/internal/core/data/codec/jsonschema"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// uuidLayout is a UUID's text (RFC 9562 §4): hexadecimal digits where it
// has an x, a hyphen where it has one.
const uuidLayout string = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"

// formatCheckers are the formats this package can assert; any other is an
// annotation, and naming it in Config.AssertFormats is refused.
func formatChecker(name string) (func(string) bool, bool) {
	//: by format name.
	switch name {
	case "date-time":
		return isDateTime, true
	case "uuid":
		return isUUID, true
	case "email":
		return isEmail, true
	default:
		return nil, false
	}
}

// checkFormats refuses a format named in Config.AssertFormats that this
// package cannot assert.
func checkFormats(names []string) error {
	//: each name must have a checker.
	for _, name := range names {
		if _, ok := formatChecker(name); !ok {
			return errs.Wrap(corejsonschema.FormatUnknown, errs.WrapParams{}, errs.String("format", name))
		}
	}
	return nil
}

// buildFormat compiles "format": an annotation, unless Config.AssertFormats
// names it.
func buildFormat(k keyword) (check, error) {
	//: a string.
	if k.kw.kind != kindString {
		return nil, keywordInvalid("format")
	}
	is, known := formatChecker(k.kw.s)
	if !known || !k.c.formats[k.kw.s] {
		return nil, nil
	}
	return func(e *eval, v *value, path string) bool {
		if v.kind != kindString || is(v.s) {
			return true
		}
		return e.fail(path, "format", "must be a valid "+k.kw.s, violationCode)
	}, nil
}

// isDateTime reports whether s is an RFC 3339 date-time.
func isDateTime(s string) bool {
	//: time's RFC 3339 reader, fractional seconds allowed.
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// isUUID reports whether s is a UUID in its 8-4-4-4-12 hexadecimal text.
func isUUID(s string) bool {
	//: the length, the hyphens, then the digits.
	if len(s) != len(uuidLayout) {
		return false
	}
	for i := range len(s) {
		if !uuidByte(i, s[i]) {
			return false
		}
	}
	return true
}

// uuidByte reports whether c may sit at position i of a UUID's text.
func uuidByte(i int, c byte) bool {
	//: a hyphen where the layout has one.
	if uuidLayout[i] == '-' {
		return c == '-'
	}
	//: a hexadecimal digit elsewhere.
	return isHex(c)
}

// isHex reports whether c is a hexadecimal digit, in either case.
func isHex(c byte) bool {
	//: the three ranges.
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isEmail reports whether s is an addr-spec as net/mail reads one, and
// nothing more: no display name, no angle brackets.
func isEmail(s string) bool {
	//: parsed, and the address is the whole input.
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Name == "" && addr.Address == s
}
