// Package view implements the ADR 0058 rendering port over the stdlib's
// html/template: one [coreview.Factory], registered under [coreview.HTML], that
// parses a whole template tree eagerly and renders each request into a bounded
// buffer it owns.
//
// # Why html/template and nothing else
//
// html/template is not "the stdlib option". It is the only template engine in
// the Go ecosystem that escapes ACCORDING TO CONTEXT — a value between two
// tags, the same value inside an attribute, inside a URL and inside a <script>
// block are four different escapings, and it tracks which one applies by
// parsing the surrounding HTML. text/template, pongo2, quicktemplate and jet
// all escape uniformly or not at all, which is why they are extension-point
// candidates rather than SDK defaults.
//
// The package therefore never imports text/template, and does not merely
// promise not to: TestDomainNeverReachesTextTemplate parses the source of all
// three view packages and fails the build on the import. The two packages are
// API-compatible, so the substitution compiles, passes every test and ships
// stored XSS — a documented rule would not have survived the first afternoon
// somebody needed to render an email body.
//
// # What lands where
//
//   - html.go   — the Factory, its registration, and the two constructors.
//   - parse.go  — the FS walk, full-path naming, and the escaping probe that
//     turns a lazily-detected escaping failure into a refused construction.
//   - render.go — the renderer type and the bounded, all-or-nothing render.
//   - limit.go  — the writer that stops execution AT the ceiling.
//   - scan.go   — the trust-type scan that runs before the template does.
//   - cycles.go — the pointer bookkeeping that stops a self-referential model.
package view

import (
	coreview "github.com/kitsunium/sdk/internal/core/view"
)

// HTML is the registered html/template [coreview.Factory].
//
// It registers through a package-level var initialiser rather than an init(),
// mirroring core/codec and core/writer: importing this package is what wires
// the engine, and the binding is a value a caller can hold.
//
// Registration arms nothing. Unlike ADR 0048's OTLP/HTTP emitter — which is
// deliberately NOT registered, because an import that arms a network client is
// worse than the stdout hazard ADR 0030 refuses — a Factory holds no file, no
// socket and no stream. It cannot act until someone calls New.
var HTML = coreview.Register(htmlFactory{})

// htmlFactory builds html/template renderers. Empty struct: every parameter
// lives in the [coreview.Config] handed to New, so the factory itself is a
// singleton with no state to race over.
type htmlFactory struct{}

// Engine returns [coreview.HTML], the registry key this factory claims.
func (htmlFactory) Engine() coreview.Engine {
	//: the name is a constant of the port, not of this package — an engine does
	//: not get to invent the key callers resolve it by.
	return coreview.HTML
}

// New parses cfg.FS in full and returns a ready [coreview.Renderer].
//
// Everything permanent is decided here. A nil FS, a file that does not parse
// and a template html/template cannot build an escaping plan for are all
// refused now, so a broken template tree is a deploy that does not come up
// rather than a 500 on the one page nobody smoke-tested.
//
// An EMPTY tree is not an error. It produces a Renderer that refuses every name
// with [coreview.TemplateNotFound] — a loud, typed behaviour rather than an
// inert one, which is the half of ADR 0031 that applies to a configuration
// nobody filled in.
func (htmlFactory) New(cfg coreview.Config) (renderer coreview.Renderer, err error) {
	//: the constructor is the whole engine-specific surface; see parse.go.
	return newHTMLRenderer(cfg)
}

// NewHTML builds an html/template [coreview.Renderer] directly, without going
// through the registry. It is what pkg/v1/view.New calls.
//
// Both paths exist on purpose. A program that knows it renders HTML says so in
// code and gets a compile-time answer; a program that resolves the engine from
// configuration goes through [coreview.Open] and gets [coreview.EngineUnknown]
// for a name nobody claims. The registry is the extension point, not a
// mandatory indirection.
func NewHTML(cfg coreview.Config) (renderer coreview.Renderer, err error) {
	//: identical to the factory path — one constructor, two spellings.
	return newHTMLRenderer(cfg)
}

// newHTMLRenderer adapts the concrete constructor to the port's interface
// return, and it is not a rename: returning newRenderer's (*renderer, error)
// pair directly would hand back a NON-NIL coreview.Renderer holding a nil
// *renderer on every construction failure. A caller checking `if r != nil`
// would then call Render on nothing.
//
// This is Go's typed-nil trap, and a domain whose whole contract is "a broken
// template tree is a deploy that does not come up" is the last place it may
// happen: the deploy would come up, holding a renderer that panics on its
// first request.
func newHTMLRenderer(cfg coreview.Config) (renderer coreview.Renderer, err error) {
	built, buildErr := newRenderer(cfg)
	//: on failure hand back an explicitly nil interface, never a typed nil.
	if buildErr != nil {
		//: nil, so `renderer != nil` means what a caller expects it to mean.
		return nil, buildErr
	}
	//: only a successfully parsed tree is boxed into the port.
	return built, nil
}

// ContentType reports [coreview.ContentTypeHTML].
//
// The charset is part of the security property, not formatting: a response
// whose Content-Type carries no charset is sniffed, and a document sniffed as
// UTF-7 can smuggle markup past an escaper that judged the bytes as UTF-8.
func (*renderer) ContentType() string {
	//: constant for this engine — it escapes for exactly one media type.
	return coreview.ContentTypeHTML
}
