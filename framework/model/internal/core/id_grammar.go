// The grammar's words, matched by hand.

package core

import "strings"

// The patterns' bounds, which the matchers below apply.
const (
	// maxWord is a segment's or a name's length at most: one first byte and
	// {0,62} more.
	maxWord int = 63
	// maxVersionDigits is a contract version's length at most: [1-9][0-9]{0,3}.
	maxVersionDigits int = 4
	// routeWhitespace is what \s matches in RE2, which a route's path excludes.
	routeWhitespace = "\t\n\f\r "
)

// The matchers apply SegmentPattern, NamePattern, RoutePattern and
// ContractPattern byte by byte, and TestTheMatchersAgreeWithThePatterns
// holds them to the patterns. They are not the patterns compiled: RE2
// expands a bounded repetition such as {0,62} into as many states, and the
// four took 0.6 ms and 2 500 allocations at every start of every product,
// which declares its names before main.

// wordOf reports whether s is one byte first accepts, then up to maxWord-1
// bytes rest accepts: a segment or a name.
func wordOf(s string, first, rest func(byte) bool) bool {
	if s == "" || len(s) > maxWord || !first(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !rest(s[i]) {
			return false
		}
	}
	return true
}

// validRoute reports whether s is a route an endpoint is named by:
// RoutePattern, "[A-Z]+ /[^\s]*".
func validRoute(s string) bool {
	method, path, found := strings.Cut(s, " /")
	return found && method != "" && strings.IndexFunc(method, notUpper) < 0 &&
		!strings.ContainsAny(path, routeWhitespace)
}

// isLower is [a-z].
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

// isLetter is [A-Za-z].
func isLetter(c byte) bool { return isLower(c) || (c >= 'A' && c <= 'Z') }

// isDigit is [0-9].
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isSegmentByte is a segment's [a-z0-9-].
func isSegmentByte(c byte) bool { return isLower(c) || isDigit(c) || c == '-' }

// isNameByte is a name's [A-Za-z0-9_.-].
func isNameByte(c byte) bool { return isLetter(c) || isDigit(c) || c == '_' || c == '.' || c == '-' }

// notDigit is any rune but [0-9].
func notDigit(r rune) bool { return r < '0' || r > '9' }

// notUpper is any rune but [A-Z].
func notUpper(r rune) bool { return r < 'A' || r > 'Z' }
