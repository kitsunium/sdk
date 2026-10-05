// Package i18n — the RFC 9110 Accept-Language header parser.
//
// Package i18n — a catalogue entry, in the two shapes a catalogue file has.
//
// Package i18n — the one error this package builds rather than declares. The
// domain's codes and sentinels — the port's verdicts and the outcomes only a
// concrete catalogue can produce — are declared in internal/core/app/i18n
// (ADR 0160); this file holds failLoad, which raises CATALOG_LOAD_FAILED over
// the filesystem's or the codec's own error.
//
// Package i18n — loading a catalogue directory through the codec domain.
//
// Package i18n — a message compiled from the text a translator wrote.
//
// Package i18n — Accept-Language negotiation: RFC 9110 parsing over RFC 4647
// matching.
//
// Package i18n — the language negotiator a server builds once and uses on
// every request.
//
// Package i18n — the placeholder syntax, compiled once at catalogue load.
//
// Package i18n — the placeholder parser, run once per catalogue entry.
//
// Package i18n — the hand-written CLDR plural rules, and the languages this
// SDK will speak.
//
// Package i18n — one language's CLDR plural specification.
//
// Package i18n — the renderer: one language, one resolution chain.
//
// Package i18n — the resolution chain a [Printer] walks.
//
// Package i18n — the concrete catalogue, and everything it refuses at load.
//
// Package i18n — the compile-time proof that [Store] satisfies the port and
// both of its ADR 0039 siblings.
//
// The assertions live in their own file rather than beside the type because a
// port change must fail the build HERE, at a declaration whose only purpose is
// to say what Store claims to be, rather than at whichever call site happened
// to pass one first.
//
// Package i18n — the parser of a written language tag: the BCP 47 subset this
// domain admits, and everything outside it refused BY NAME.
package i18n
