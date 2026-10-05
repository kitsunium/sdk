// Package redact — log attributes rendered as display text.
//
// Package redact — the members encoding/json writes for a struct, selected by
// encoding/json's own rules rather than an approximation of them.
//
// Package redact — copying a JSON document with its secrets replaced, within
// an exact byte bound.
//
// Package redact — which members of a Go type's JSON form are secret by
// declaration, worked out once per type.
//
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
//
// It implements the internal/core/security/redact port: the Redactor
// interface, the values a caller compares results against — Placeholder,
// Ellipsis, MinBytes, Unencodable, DocumentValue — and the domain's two codes
// are declared there (ADR 0160). What is here is the engine and its
// construction parameters (ADR 0074).
//
// Package redact — hosts the compile-time interface assertion, keeping it out
// of the production source so the runtime binary carries no diagnostic-only
// declaration.
//
// Package redact — text: the credentials of a URL, and a cut that respects
// runes.
package redact
