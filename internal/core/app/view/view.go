// Package view declares the SDK's server-side rendering DOMAIN: the [Renderer]
// port that turns a named template plus a data value into bytes, the [Engine]
// registry that resolves one implementation by name, and the single trust type
// through which a caller can deliberately bypass escaping. A core sibling
// admitted by ADR 0058.
//
// # The domain is HTML, and that is a security boundary rather than a scope note
//
// html/template's entire value is that it escapes ACCORDING TO CONTEXT: the
// same value is escaped one way between two tags, another way inside an
// attribute, another inside a URL, and another inside a <script> block.
// text/template does none of that — it is string concatenation with a syntax —
// and the two packages are API-compatible, so swapping one import for the
// other compiles, passes every test, and ships stored XSS.
//
// This domain therefore has no text/template representation at all. Not an
// [Engine] name, not a Factory, not a constructor, not an import: a named AST
// audit in internal/service/app/view fails the build if the identifier appears
// anywhere in the domain's three packages. The distinction is structural
// because a documented one is a distinction that survives exactly until the
// afternoon someone needs to render an email body.
//
// Plain-text templating is out of scope, by name. If it is ever wanted it
// arrives as its own domain with its own port — never as a second [Engine] in
// this registry, because a registry exists to make its members interchangeable
// and an escaping engine is not interchangeable with a non-escaping one.
//
// # Rendering is all-or-nothing, and the signature is what enforces it
//
// [Renderer.Render] returns a []byte. It does NOT take an io.Writer, and that
// is the decision this port is shaped by.
//
// Template execution writes incrementally and can fail halfway: a nil pointer
// in a field chain, a method returning an error, a range over the wrong type.
// Handed an http.ResponseWriter, html/template will have flushed the status
// line, the headers and part of the body before it reports the failure —
// measured at seventeen bytes of a valid-looking page for a template failing on
// its second action — and at that point 500 is no longer sendable and the
// browser renders a truncated document. A caller cannot avoid this by being
// careful; it can only avoid it by not being given the opportunity.
//
// So the engine renders into a bounded buffer it owns and hands back the
// complete bytes or nothing at all. The caller's destination is never touched
// on a failed render, because the caller's destination is never passed in.
//
// # Nothing here writes anywhere, which is how ADR 0030 is satisfied
//
// ADR 0030 forbids an SDK default that writes to os.Stdout, because stdout may
// be the process's protocol channel. This domain does not satisfy that rule by
// choosing stderr; it satisfies it by having no destination in the port at all.
// A Renderer holds no writer, opens no file and knows no stream.
//
// # Where this domain stops
//
// The SDK ships the extension point and one implementation of it. It does not
// ship a template LANGUAGE: layouts, block inheritance, helper libraries,
// in-template i18n, asset pipelines and distributed compilation caches are all
// framework opinions, and a domain that held them would be a rendering
// framework wearing a port's name. Template composition is whatever the chosen
// engine's own syntax already provides — for html/template that is {{define}}
// and {{template}}, which this package neither extends nor conventionalises.
package view

// ContentTypeHTML is the value an HTML [Renderer] reports from ContentType.
//
// The charset is not decoration. A response whose Content-Type carries no
// charset is sniffed by the browser, and a document sniffed as UTF-7 or as a
// legacy multi-byte encoding can smuggle markup past an escaper that judged
// the bytes as UTF-8.
const ContentTypeHTML string = "text/html; charset=utf-8"
