// Package redact renders values, JSON documents, text and log attributes for
// DISPLAY with their secrets replaced, within a byte bound, never touching
// what it is given.
//
//	r := redact.New(redact.Config{})
//	shown, err := r.Value(request, 8<<10)   // a Document: shown.JSON, shown.Truncated
//	line := r.Text(message, 2<<10)          // URL credentials replaced, cut
//	for key, text := range r.Attrs(record.Attrs, 2<<10) { … }
//
// [Redactor] is a port frozen at five methods — Name, Text, JSON, Value and
// Attrs: [New] returns the SDK's implementation, and code that accepts a
// Redactor can be handed a test double in its place.
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
