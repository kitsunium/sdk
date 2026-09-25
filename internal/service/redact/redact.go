// Package redact renders values, JSON documents, text and log attributes for
// DISPLAY with their secrets replaced — a member whose name says it is one, a
// struct field declared secret, the credentials of a URL — and within a byte
// bound. It never mutates what it is given.
//
// It is for showing data to a person: a developer console, a trace viewer, a
// log panel, an error page. It is not an access control and it is not a
// sanitiser for data that goes back into a system: a redacted document is a
// different document, and a secret spelled in a way no rule recognises — a
// password in a member called "p", a key pasted into free text — is shown.
// What IS recognised is stated in each rule's own comment, so nobody has to
// guess.
package redact

import (
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Placeholder replaces every secret this package recognises.
const Placeholder string = "[redacted]"

// Ellipsis ends a string, a text or a document cut to fit its bound. It is
// one character, three bytes of UTF-8, and it is counted inside the bound.
const Ellipsis string = "…"

// MinBytes is the smallest bound a call honours; a smaller or non-positive
// one is raised to it. Below it a cut document could not hold its own
// marker and the closers of the containers it cut — and a bound is the
// display's size, so raising a nonsensical one is safer than refusing to
// show anything (ADR 0031's clamp: the floor is obvious).
const MinBytes int = 16

// defaultTag is the struct tag key read when Config.Tag is empty.
const defaultTag string = "redact"

// secretOption is the tag option that declares a field secret.
const secretOption string = "secret"

// defaultWords are the fragments of a member name that make it a secret,
// matched case-insensitively anywhere in the name — "accessToken",
// "password_confirm", "X-Api-Key", "Set-Cookie" all match.
var defaultWords = []string{
	"password", "passwd", "secret", "token", "authorization",
	"cookie", "session", "apikey", "api_key", "api-key",
}

// DefaultWords returns the name fragments a Redactor treats as secret when
// its Config gives none. It returns a fresh slice, so a caller extending it —
// append(redact.DefaultWords(), "pin") — changes nothing shared.
func DefaultWords() []string {
	//: a copy: the package's own list is never handed out.
	return slices.Clone(defaultWords)
}

// Config says what a Redactor treats as secret. Its zero value is usable:
// the default words, the "redact" tag, no extra field rule, errors rendered
// by their own text.
type Config struct {
	// Field is an extra rule: it reports whether a struct field is secret
	// by declaration, beyond the tag — a field bound to a cookie, a field
	// bound to a header whose name is a secret's. Nil adds nothing. It is
	// called once per field per type, when the type is first seen.
	Field func(field reflect.StructField) bool
	// Error renders an error found in a log attribute. Nil uses the error's
	// own text, scrubbed and cut like any other text. A framework that
	// shows only an error's wire-safe half passes its own.
	Error func(err error) string
	// Tag is the struct tag key whose comma-separated options, when one of
	// them is "secret", declare a field secret: Tag "kit" reads
	// `kit:"secret"` and `kit:"other,secret"`. Empty means "redact".
	Tag string
	// Words are the fragments of a member or attribute name that make it a
	// secret, matched case-insensitively anywhere in the name. Nil or empty
	// means DefaultWords — there is no way to recognise NO name, because a
	// redactor that recognises nothing by name is a formatter.
	Words []string
}

// Redactor applies one Config. It is safe for concurrent use, and it caches
// what it learns about each Go type — which of its fields are secret — for
// its own lifetime, so build one per configuration and keep it.
type Redactor struct {
	field func(field reflect.StructField) bool
	error func(err error) string
	plans sync.Map // reflect.Type → *plan
	tag   string
	words []string
}

// NewRedactor returns a Redactor applying cfg.
func NewRedactor(cfg Config) *Redactor {
	words := cfg.Words
	//: no words is the default words, never none.
	if len(words) == 0 {
		words = defaultWords
	}
	lowered := make([]string, 0, len(words))
	//: matched case-insensitively, so lowered once here rather than per call.
	for _, word := range words {
		//: an empty fragment would match every name.
		if word = strings.ToLower(strings.TrimSpace(word)); word != "" {
			lowered = append(lowered, word)
		}
	}
	tag := cfg.Tag
	//: the SDK's own spelling.
	if tag == "" {
		tag = defaultTag
	}
	//: ready; the per-type cache fills as types are met.
	return &Redactor{field: cfg.Field, error: cfg.Error, tag: tag, words: lowered}
}

// Name reports whether a member, header or attribute name names a secret: it
// contains one of the Redactor's words, whatever its case.
func (r *Redactor) Name(name string) bool {
	lowered := strings.ToLower(name)
	//: a fragment anywhere in the name is enough.
	for _, word := range r.words {
		//: "accessToken" contains "token".
		if strings.Contains(lowered, word) {
			//: a secret's name.
			return true
		}
	}
	//: nothing in it says secret.
	return false
}

// bound resolves a caller's byte bound to one the output can honour.
func bound(maxBytes int) int {
	//: the floor under which a cut output cannot hold its own markers.
	return max(maxBytes, MinBytes)
}
