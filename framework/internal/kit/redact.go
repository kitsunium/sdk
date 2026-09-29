// Package kit — redaction: what a value shows once its secrets are hidden.
package kit

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/kitsunium/sdk/pkg/v1/redact"
)

// Redaction: what the Studio shows of a payload or a log record, and what
// the terminal's log lines write, never carries a secret or a person's data.
// The SDK's redact does the work (ADR 0001); kit says what to hide — a member
// named like a secret (password, token, secret, authorization, cookie,
// session, API key), a Go field whose kit tag classifies it personal,
// special or secret, or names it the subject (ADR 0006), a field read from a
// cookie, or from a header named like a secret — and renders an error by
// what kit would say of it on the wire.

var (
	// names judges a name by the SDK's words alone. It is the redactor's rule
	// too, kept apart so the field rule below can use it while redactor is built.
	names = redact.New(redact.Config{})

	// redactor is the one redactor the Studio's views go through. It caches
	// what it learns about each Go type for the life of the process.
	redactor = redact.New(redact.Config{
		Tag:   "kit",
		Field: boundSecret,
		Error: func(err error) string {
			_, body := describe(err)
			return body.Message
		},
	})
)

// boundSecret reports whether a field is hidden by declaration beyond the
// SDK's own reading of kit:"secret": its kit tag classifies it personal or
// special, or names it the subject — by the comma rule, whatever else the
// tag says — or it is a request field bound to a cookie, or to a header
// named like a secret, which holds a credential whatever its JSON name.
func boundSecret(f reflect.StructField) bool {
	if redactedByTag(f.Tag.Get("kit")) {
		return true
	}
	if _, ok := f.Tag.Lookup("cookie"); ok {
		return true
	}
	if h, ok := f.Tag.Lookup("header"); ok {
		name, _, _ := strings.Cut(h, ",")
		return names.Name(name)
	}
	return false
}

// redactValue encodes v as JSON — as the wire would — with its secrets
// replaced, within limit bytes. It reports whether anything was cut.
func redactValue[V any](v V, limit int) (json.RawMessage, bool) {
	doc, err := redactor.Value(v, limit)
	if err != nil {
		return json.RawMessage(`"` + redact.Unencodable + `"`), false
	}
	return doc.JSON, doc.Truncated
}

// redactJSON copies one JSON document with its secrets replaced, within
// limit bytes. It reports whether anything was cut.
func redactJSON(raw []byte, limit int) (json.RawMessage, bool) {
	doc, err := redactor.JSON(raw, limit)
	if err != nil {
		return json.RawMessage(`"[not valid JSON]"`), false
	}
	return doc.JSON, doc.Truncated
}
