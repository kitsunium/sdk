// Package view is the public facade for the SDK's server-side rendering: a
// named template plus a data value become a complete HTML document, escaped
// according to the context each value lands in.
//
//	//go:embed web
//	var web embed.FS
//
//	renderer, err := view.New(view.Config{FS: web, Ext: []string{".html"}})
//	if err != nil { return err }
//
//	document, err := renderer.Render(ctx, "web/admin/page.html", model)
//	if err != nil { return err }            // nothing has been written yet
//	w.Header().Set("Content-Type", renderer.ContentType())
//	w.Write(document)
//
// # The engine is html/template, and that is the decision, not a default
//
// This package does not implement a template engine. It builds a CONTRACT on
// top of the standard library's html/template, which is the only engine in the
// Go ecosystem that escapes according to CONTEXT: the same value is escaped
// one way between two tags, another inside an attribute, another inside a URL
// and another inside a <script> block, and it decides which by parsing the
// surrounding HTML.
//
// Re-implementing that would be a security regression rather than a gain.
// html/template is one of the most heavily audited packages in the standard
// library, and a hand-written escaper would become the source of exactly the
// cross-site scripting this domain exists to prevent. It is also already
// "total control with no dependency" — it is the stdlib.
//
// text/template has NO representation here. Not a name, not a Factory, not an
// import: the two packages are API-compatible, so swapping one for the other
// compiles, passes every test and ships stored XSS, and a named AST audit
// fails the build if the identifier appears anywhere in the domain's three
// packages.
//
// # What the SDK adds that the stdlib does not have
//
//   - A [Renderer] port a framework can be wired to, with one registered
//     engine and room for others ([Register], [Open]).
//   - Templates named by their full SLASH PATH. html/template's own ParseFS
//     names them by filepath.Base, so a tree holding admin/page.html and
//     user/page.html ends up with ONE template called "page.html" — the second
//     parse silently wins, and every request for the admin page renders the
//     user page.
//   - Typed SDK errors instead of text/template's strings, with the engine's
//     leak-prone diagnostic kept out of the wire-safe half.
//   - Eager parsing AND an escaping probe at construction, so a broken tree is
//     a deploy that does not come up rather than a 500 on the one page nobody
//     smoke-tested.
//   - A byte ceiling, because text/template's Execute takes no context and
//     cannot be cancelled.
//
// # Read this before calling TrustHTML
//
// [TrustHTML] is the one place escaping can be bypassed, and it is the only
// spelling the SDK offers for it. Six of html/template's seven trust types —
// CSS, HTMLAttr, JS, JSStr, URL and Srcset — are REFUSED when they appear in
// render data, with the path that carries them named in the error.
//
// What that buys is precise, and the limit is worth reading twice:
// [TrustedHTML] is a type ALIAS of html/template.HTML, because html/template
// recognises its trust types by an exact type switch and a distinct type would
// simply be escaped like any other string. The engine therefore CANNOT tell a
// fragment marked through [TrustHTML] from one a caller converted directly.
//
// So the SDK does not prevent a bad trust decision — it cannot see one. What
// it prevents is an ACCIDENTAL one, and what it provides is a single greppable
// word for the deliberate one. Every call is an assertion that the string was
// produced by the server and sanitised. It is never correct to hand it a
// string that arrived from a request, from a user-controlled database field,
// or from a third-party API.
//
// # Rendering is all-or-nothing
//
// [Renderer].Render returns a []byte. It does not take an io.Writer, and that
// is what the port is shaped by: template execution writes incrementally and
// can fail halfway, so handed an http.ResponseWriter it would have flushed the
// status line, the headers and a plausible prefix of the page before reporting
// the failure — at which point 500 is no longer sendable and the browser
// renders a truncated document. The engine renders into a bounded buffer it
// owns and hands back the complete bytes or nothing at all.
//
// # Parse once. This is the one performance rule the domain has.
//
// Build the [Renderer] at start-up and keep it. It is immutable after
// construction, safe for concurrent use, and holds no writer, no file and no
// stream. Constructing one per request re-parses the whole tree and re-runs
// the escaping analysis on every request; measured on a forty-two-template
// tree, that is 34 times the time and 34 times the bytes of reusing one, and
// the ratio grows with the tree — see internal/service/app/view/BENCH.md.
// There is deliberately no reload, no lazy parse and no cache, because each of
// them would put a mutex on the read path to solve a problem that a package
// variable already solves.
//
// # Scope
//
// The SDK ships the extension point and one implementation of it. It does not
// ship a template LANGUAGE: layouts, block inheritance, helper libraries,
// in-template i18n and asset pipelines are framework opinions. Template
// composition is whatever the engine's own syntax already provides — for
// html/template that is {{define}} and {{template}}, which this package
// neither extends nor conventionalises.
package view
