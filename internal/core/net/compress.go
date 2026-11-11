package net

import (
	"strconv"
	"strings"
)

const (
	// acceptEncodingMaxBytes is the longest Accept-Encoding this reads: a
	// browser's is a few dozen bytes, and the field is the peer's.
	acceptEncodingMaxBytes int = 1 << 10
	// acceptEncodingMaxElements is the most elements of one it walks.
	acceptEncodingMaxElements int = 32
)

// CompressValue configures the response compression of Compress: the types it
// may compress, which are an allowlist, the smallest body worth it, and gzip's
// level. Every field's zero value is the default, never no bound (ADR 0031).
type CompressValue struct {
	// Types lists the Content-Types, without parameters, that may be compressed.
	// Empty selects text/plain, text/css, text/javascript, text/csv, text/xml,
	// application/json, application/javascript, application/xml, image/svg+xml
	// and application/wasm. text/html is not among them: a page that reflects a
	// request's input beside a secret is what BREACH reads, and a caller who
	// compresses HTML names it here, knowing it does not do that.
	Types []string `json:"types"`
	// Level is gzip's level, 1 to 9 or -1 for its default; zero selects -1.
	Level int `json:"level"`
	// MinBytes is the smallest body compressed: a smaller one is sent as it is,
	// as its gzip container would cost more than it saves. Zero selects 1024; a
	// negative value is refused.
	MinBytes int `json:"min_bytes"`
}

// AcceptsEncoding reports whether the Accept-Encoding field value admits coding
// (RFC 9110 §12.5.3): named with a non-zero quality, or covered by "*" with one.
// A value past 1 KiB or 32 elements is not parsed, and admits nothing — the
// field is the peer's, and identity is a safe answer.
func AcceptsEncoding(value, coding string) bool {
	//: past the bound: identity is always safe.
	if len(value) > acceptEncodingMaxBytes {
		//: refused without reading it.
		return false
	}
	named, star := false, false
	elements := 0
	//: one element per comma.
	for element := range strings.SplitSeq(value, ",") {
		elements++
		//: past the bound: identity is always safe.
		if elements > acceptEncodingMaxElements {
			//: refused without reading the rest.
			return false
		}
		token, quality := acceptElement(element)
		//: an element without a name says nothing.
		if token == "" {
			continue
		}
		//: the exact coding decides for itself, "*" only for what is not named.
		if strings.EqualFold(token, coding) {
			named = true
			star = quality
			continue
		}
		//: the wildcard, unless the coding is named elsewhere in the list.
		if token == "*" && !named {
			star = quality
		}
	}
	//: admitted by its own name, or by a "*" of a positive quality.
	return star
}

// acceptElement splits one Accept-Encoding element into its coding and whether
// its quality is positive.
func acceptElement(element string) (token string, quality bool) {
	head, rest, hasParams := strings.Cut(element, ";")
	token = strings.TrimSpace(head)
	//: no quality: one.
	if !hasParams {
		//: admitted.
		return token, true
	}
	//: the parameters; the quality is the only one this reads.
	for param := range strings.SplitSeq(rest, ";") {
		name, val, ok := strings.Cut(param, "=")
		//: a parameter that is not the quality.
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		//: an unreadable quality admits nothing: identity is always safe.
		if err != nil || q <= 0 {
			//: refused.
			return token, false
		}
		//: positive.
		return token, true
	}
	//: no q= among the parameters.
	return token, true
}
