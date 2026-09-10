//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/view .

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
// [Renderer.Render] returns a []byte. It does not take an io.Writer, and that
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
// the ratio grows with the tree — see internal/service/view/BENCH.md.
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

import (
	coreview "github.com/kitsunium/sdk/internal/core/view"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

const (
	// HTML is the [Engine] name of the stdlib html/template implementation. It
	// is the only engine the SDK ships.
	HTML Engine = coreview.HTML

	// ContentTypeHTML is the media type an HTML [Renderer] reports, charset
	// included — a response with no charset is sniffed, and a document sniffed
	// as UTF-7 can smuggle markup past an escaper that judged the bytes as
	// UTF-8.
	ContentTypeHTML string = coreview.ContentTypeHTML

	// DefaultMaxBytes is the ceiling [Config.MaxBytes] clamps to when it is not
	// positive: 8 MiB. There is deliberately no spelling for "unlimited".
	DefaultMaxBytes int = coreview.DefaultMaxBytes
)

// Renderer turns a named template and a data value into a complete document:
// Render and ContentType.
//
// The method set is FROZEN at two. New capabilities arrive as SIBLING
// interfaces reached by type assertion, because this alias publishes the
// interface, Go interfaces are structural, and a third method would break
// every downstream implementer at compile time with no deprecation window
// (ADR 0039).
type Renderer = coreview.Renderer

// Config parameterises every engine. FS is required; everything else has a
// defensible default.
type Config = coreview.Config

// Factory is the plug-in contract an engine implements to enter the registry.
// Registering is a promise the security model rests on — see [Register].
type Factory = coreview.Factory

// Engine names a registered template engine. The zero value is the reserved
// invalid name.
type Engine = coreview.Engine

// TrustedHTML is an HTML fragment written WITHOUT escaping.
//
// It is an ALIAS of html/template.HTML, and that has a consequence stated
// rather than hidden: the engine cannot tell a fragment marked through
// [TrustHTML] from one a caller converted directly. See the package
// documentation.
type TrustedHTML = coreview.TrustedHTML

// The sentinels a caller matches with errors.Is or errs.HasCode.
var (
	// ViewMisconfigured is returned by a constructor for a Config it cannot
	// honour: a nil FS, a tree that does not parse, or a template
	// html/template cannot contextually escape.
	ViewMisconfigured = coreview.ViewMisconfigured

	// TemplateNotFound is returned by Render for a name the engine does not
	// hold, including the empty name. A Renderer over an empty tree is
	// legitimate and refuses everything by name.
	TemplateNotFound = coreview.TemplateNotFound

	// RenderFailed is returned when execution started and could not finish.
	// Nothing is returned alongside it: the partial bytes are discarded
	// inside the engine.
	RenderFailed = coreview.RenderFailed

	// RenderTooLarge is returned when a render reached Config.MaxBytes. It is
	// a denial-of-service guard, not a formatting complaint.
	RenderTooLarge = coreview.RenderTooLarge

	// UnsafeValue is returned when render data carries one of the six refused
	// html/template trust types. The fields name the path and the type, never
	// the value.
	UnsafeValue = coreview.UnsafeValue

	// EngineUnknown is returned by [Open] for a name no factory claims —
	// usually an engine package that was never imported.
	EngineUnknown = coreview.EngineUnknown

	// EngineInvalid is the boot-time panic for a nil Factory or one claiming
	// the empty Engine name.
	EngineInvalid = coreview.EngineInvalid

	// DuplicateEngine is the boot-time panic for two distinct factories under
	// one Engine name. It is refused rather than resolved, because one of the
	// two would silently decide how every value in the program is escaped.
	DuplicateEngine = coreview.DuplicateEngine

	// TemplateSourceFailed is returned when the template FS could not be READ
	// — usually an embed pattern that matched nothing.
	TemplateSourceFailed = svcview.TemplateSourceFailed

	// TemplateParseFailed is returned for a file html/template refused to
	// parse or could not build an escaping plan for.
	TemplateParseFailed = svcview.TemplateParseFailed
)

// TrustHTML marks s as HTML that is already safe to write unescaped.
//
// It is the one bypass the SDK offers and the only spelling it has, so "where
// does this codebase decide to trust markup?" is one grep with one answer.
// Read the package documentation before using it: the SDK cannot verify the
// assertion this call makes.
func TrustHTML(s string) TrustedHTML {
	//: delegate to the port, which owns the one conversion.
	return coreview.TrustHTML(s)
}

// New builds the stdlib html/template [Renderer] over cfg.FS, parsing the
// whole tree eagerly.
//
// Call it once, at start-up, and keep the result: see the package
// documentation's parse-once rule. A nil FS, a file that does not parse and a
// template whose escaping context cannot be resolved are all refused here, so
// a broken tree is a deploy that does not come up.
//
// An EMPTY tree is not an error. It produces a Renderer that refuses every
// name with [TemplateNotFound] — loud rather than inert (ADR 0031).
func New(cfg Config) (renderer Renderer, err error) {
	//: delegate to the engine constructor, which validates and parses.
	return svcview.NewHTML(cfg)
}

// Open resolves an [Engine] by name and builds a [Renderer] from cfg. It is
// the configuration-driven counterpart to [New].
//
// It fails loudly on a name nobody claims — [EngineUnknown] — rather than
// falling back to a default, because a fallback here would let a typo in a
// configuration file silently choose how every value in the program is
// escaped.
func Open(name Engine, cfg Config) (renderer Renderer, err error) {
	//: delegate to the registry; the factory owns every Config check.
	return coreview.Open(name, cfg)
}

// Register inserts f under f.Engine() and returns it, so a third-party engine
// can bind the singleton to a package-level variable.
//
// Registration is not merely a name binding. An engine in this registry MUST
// contextually escape every value it renders for the media type its
// ContentType reports, render into its own buffer and return complete bytes or
// none, honour Config.MaxBytes, refuse at construction a Config it cannot
// honour, and name every template by its slash path within Config.FS.
//
// It panics on a nil factory, on the empty [Engine] name, and when a DISTINCT
// factory already claims the name — at import, where the offender is visible.
func Register(f Factory) Factory {
	//: delegate to the port's registry; it owns the boot-time refusals.
	return coreview.Register(f)
}

// Available returns every registered [Engine], sorted, so a program can print
// what its imports actually wired up.
func Available() []Engine {
	//: delegate to the port's registry.
	return coreview.Available()
}
