// Package redact — text: the credentials of a URL, and a cut that respects
// runes.
package redact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// credentials matches the userinfo of a URL: the scheme, then everything up
// to the LAST "@" before the path or the end of the word. Greedy on purpose:
// "https://user:p@ss@host" is one userinfo with an unescaped "@" in the
// password, and stopping at the first "@" would show "ss". A "?" or "#" before
// the "@" is read as part of the userinfo, which over-redacts a host — the
// safe direction.
var credentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s]+@`)

// Text returns s with the credentials of every URL in it replaced by
// Placeholder, then cut to at most maxBytes bytes (raised to MinBytes) at a
// rune boundary, ending in Ellipsis when cut.
//
// The credentials are replaced BEFORE the cut, so a cut can never land
// between a password and the "@" that marks it as one.
func (r *Redactor) Text(s string, maxBytes int) string {
	//: scrub first, then cut: the order is the guarantee.
	return clip(r.scrub(s), bound(maxBytes))
}

// scrub replaces the credentials of every URL in s.
func (r *Redactor) scrub(s string) string {
	//: most text holds no URL with credentials, and the regexp is not free.
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		//: unchanged.
		return s
	}
	//: the scheme is kept, so the reader still sees what kind of URL it was.
	return credentials.ReplaceAllString(s, "${1}"+Placeholder+"@")
}

// clip cuts s to at most limit bytes at a rune boundary, the Ellipsis
// included.
func clip(s string, limit int) string {
	//: the ordinary case.
	if len(s) <= limit {
		//: unchanged.
		return s
	}
	cut := limit - len(Ellipsis)
	//: back to the start of the rune the cut fell in.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	//: the prefix and the marker, within the limit.
	return s[:cut] + Ellipsis
}
