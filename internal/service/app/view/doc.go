// Package view — the cycle detection the trust-type scan needs, kept in its own
// file so scan.go holds the walk and nothing else.
//
// Package view — the one helper through which every core/app/view sentinel is
// raised. The domain's codes and sentinels — the port's, and this engine's
// construction failures — are declared in internal/core/app/view (ADR 0160).
//
// No Public string the engine raises names a template, a path, a line or a
// fragment of template source. html/template's diagnostics are unusually rich
// — a parse failure carries the file path and the offending function name, an
// escaping failure carries the escaper's internal state machine, and an
// execution failure carries the path, the line, the column, a fragment of the
// template's own source and a Go type name. Every one of those is useful to an
// operator and every one of them is reconnaissance to a stranger, so all of it
// travels as Fields and Private and none of it reaches a Public.
//
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
//
// Package view — the writer that stops a render AT its ceiling rather than
// after it.
//
// Package view — construction: the FS walk, full-path template naming, the
// MaxBytes clamp, and the escaping probe that moves a lazily-detected failure
// to boot time.
//
// Package view — the bounded, all-or-nothing render.
//
// Package view — the trust-type scan that runs BEFORE the template does.
package view
