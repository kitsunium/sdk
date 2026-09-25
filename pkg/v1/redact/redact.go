//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/redact .

// Package redact renders values, JSON documents, text and log attributes for
// DISPLAY with their secrets replaced, within a byte bound, never touching
// what it is given.
//
//	r := redact.New(redact.Config{})
//	shown, err := r.Value(request, 8<<10)   // a Document: shown.JSON, shown.Truncated
//	line := r.Text(message, 2<<10)          // URL credentials replaced, cut
//	for key, text := range r.Attrs(record.Attrs, 2<<10) { … }
//
// # What counts as a secret
//
// Three rules, each stated so nobody has to guess:
//
//   - A NAME. A member, header or attribute whose name contains one of the
//     Redactor's words, case-insensitively — DefaultWords is password, passwd,
//     secret, token, authorization, cookie, session, apikey, api_key, api-key —
//     has its value replaced by Placeholder, whatever the value is: an object
//     under "session" is replaced whole. Log attributes are judged by their
//     DOTTED key, so "db.password" is a secret.
//   - A DECLARATION. A struct field tagged `redact:"secret"` — or
//     `yourtag:"secret"` with Config.Tag, and `yourtag:"other,secret"` too —
//     or one Config.Field says is secret. Found by walking the value's type
//     the way encoding/json lays it out, through pointers, slices, maps and
//     embedded structs, once per type. A type that writes its own JSON is
//     opaque to the walk; only the names in its output are judged.
//   - A URL's CREDENTIALS. In every string: scheme://user:password@host keeps
//     its scheme and host and loses the rest of the userinfo, up to the last
//     "@" before the path, so an "@" inside the password does not leak what
//     follows it.
//
// What is NOT recognised is shown: a password in a member called "p", a key
// pasted into free text, a secret in a map keyed by something innocent. This
// is a display filter, not an access control.
//
// # The bound is exact
//
// Every call takes a byte bound (raised to MinBytes). A JSON result is never
// longer than it and is always one well-formed value: a container that does
// not fit is closed early, a string that does not fit is cut at a rune and
// ends in Ellipsis, and a number that does not fit becomes the Ellipsis as a
// string. Document.Truncated says whether anything was left out. Text is cut
// AFTER its URL credentials are replaced, so a cut can never land between a
// password and the "@" that marks it.
//
// # Refusals
//
// JSON refuses a document that is not exactly one JSON value
// (CodeDocumentInvalid) and returns nothing for it: a partial copy of a
// document that does not parse cannot be trusted to have had its secrets
// recognised. Value refuses what encoding/json will not encode
// (CodeValueUnencodable). Neither refusal repeats a byte of what it refused.
package redact

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcredact "github.com/kitsunium/sdk/internal/service/redact"
)

// Placeholder replaces every secret.
const Placeholder string = svcredact.Placeholder

// Ellipsis ends whatever was cut to fit a bound, and is counted in it.
const Ellipsis string = svcredact.Ellipsis

// MinBytes is the smallest bound honoured; a smaller one is raised to it.
const MinBytes int = svcredact.MinBytes

// Unencodable is the text a log attribute encoding/json refuses is shown as.
const Unencodable string = svcredact.Unencodable

// CodeDocumentInvalid identifies a document that is not one JSON value.
const CodeDocumentInvalid errs.Code = svcredact.CodeDocumentInvalid

// CodeValueUnencodable identifies a value encoding/json refuses to encode.
const CodeValueUnencodable errs.Code = svcredact.CodeValueUnencodable

var (
	// DocumentInvalid refuses a document that is not one JSON value.
	DocumentInvalid = svcredact.DocumentInvalid
	// ValueUnencodable refuses a value encoding/json will not encode.
	ValueUnencodable = svcredact.ValueUnencodable
)

// Config says what a Redactor treats as secret: the name Words, the struct
// Tag, an extra Field rule, and how an Error in a log attribute is shown. Its
// zero value is usable.
type Config = svcredact.Config

// Redactor applies one Config: Name, Text, JSON, Value and Attrs. It caches
// what it learns about each Go type, so build one per configuration and keep
// it. It is safe for concurrent use.
type Redactor = svcredact.Redactor

// Document is a JSON document with its secrets replaced — never longer than
// its bound, always well-formed — and whether it had to be cut.
type Document = svcredact.DocumentValue

// New returns a Redactor applying cfg.
func New(cfg Config) *Redactor {
	//: delegate verbatim to the service constructor.
	return svcredact.NewRedactor(cfg)
}

// DefaultWords returns the name fragments a Redactor treats as secret when its
// Config gives none, as a fresh slice a caller may extend.
func DefaultWords() []string {
	//: delegate verbatim to the service implementation.
	return svcredact.DefaultWords()
}
