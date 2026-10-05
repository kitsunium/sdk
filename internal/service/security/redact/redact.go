package redact

import (
	"slices"
	"strings"

	coreredact "github.com/kitsunium/sdk/internal/core/security/redact"
)

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
	return max(maxBytes, coreredact.MinBytes)
}
