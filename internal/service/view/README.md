# view (service)

The one engine the SDK ships for the ADR 0058 rendering port: a
`core/view.Factory` over the stdlib's `html/template`, registered under
`core/view.HTML`.

```go
renderer, err := view.NewHTML(coreview.Config{FS: templates, Ext: []string{".html"}})
document, err := renderer.Render(ctx, "admin/page.html", model)
```

`html/template` is **used, not reimplemented**: it is the only engine in the Go
ecosystem that escapes according to context, and a hand-written replacement
would become the source of the XSS this domain exists to prevent. It is also
already "no dependency, total control" — it is the standard library. ADR 0058
§D1 argues it; `TestTheDomainNeverReachesTextTemplate` makes the substitution
to `text/template` fail the build.

What the engine adds over calling `html/template` directly:

- templates named by their full **slash path**, so `admin/page.html` and
  `user/page.html` do not collapse into one `page.html`;
- eager parsing **and** an escaping probe at construction, so the three lazily
  detected escaping failures become a deploy that does not come up;
- an all-or-nothing render into a pooled, byte-bounded buffer;
- a scan that refuses the six `html/template` trust types in render data,
  before the template runs, naming the path that carries them;
- typed errors whose wire-safe half names no template, path, line or value.

Error range `0.3.57.*`. Numbers in `BENCH.md`; maintainer rationale — including
the parse-once rule and the two profiled optimisations — in `CLAUDE.md`.
