# internal/service/view/

## Purpose

The one engine the SDK ships for the ADR 0058 rendering port: a
`coreview.Factory` over the stdlib's `html/template`, registered under
`coreview.HTML`, that parses a whole template tree eagerly and renders each
request into a bounded buffer it owns.

Code range: `0.3.57.*` (ADR 0058).

## Why this shape

**`html/template` is used, not reimplemented, and that is the security
decision.** It is the only engine in the Go ecosystem that escapes ACCORDING TO
CONTEXT, and contextual escaping is not an escaping function — it is an HTML
parser that tracks the cursor through `{{if}}`/`{{range}}` branches. A
hand-written replacement would be new, unaudited, and would become the single
most likely source of the XSS this domain exists to prevent. `html/template` is
the standard library, so "no dependency, total control" is already satisfied.
ADR 0058 §D1 argues it at length.

**Everything permanent is refused at construction.** `newRenderer` walks the
FS, parses every selected file, and then FORCES each template's escaping plan
by executing it once against `nil` into `io.Discard`. `html/template` resolves
escaping lazily, at first `Execute`, and three failure classes exist only there:

| `template.ErrorCode` | shape | what it looks like in production |
|---|---|---|
| `ErrEndContext` (4) | `<a href="{{.}}` | a page that ends inside a tag |
| `ErrBranchEnd` (3) | `<script>{{if .}}var x = "{{end}}</script>` | branches leaving different contexts |
| `ErrNoSuchTemplate` (5) | `{{template "renamed.html"}}` | **the everyday one** — a partial gets renamed, every other page's test passes, and it ships |

Only an escaping failure is fatal: executing against `nil` can legitimately
fail (a field chain on nothing), so the probe matches `*template.Error` and
ignores everything else.

**Templates are named by their full slash path.** `set.New(entry)` with `entry`
relative to the FS root. `html/template`'s own `ParseFS` names by
`filepath.Base`, so a tree with `admin/page.html` and `user/page.html` ends up
with ONE `page.html` — the second parse wins silently and every request for the
admin page renders the user page.

**The renderer is immutable after construction.** No `Add`, no `Reload`, no
lazy parse — so no lock on the read path, and no request can observe a
half-built set.

## Contents

| File | Holds |
|---|---|
| `html.go` | the `Factory`, its registration, `NewHTML`, `ContentType` |
| `parse.go` | the FS walk, full-path naming, the escaping probe, the `MaxBytes` clamp |
| `render.go` | the bounded, all-or-nothing render and the pooled scratch |
| `limit.go` | the writer that stops execution AT the ceiling |
| `scan.go` | the trust-type scan that runs BEFORE the template does |
| `cycles.go` | the pointer bookkeeping that stops a self-referential model |
| `codes.go` / `errors.go` | `0.3.57.*` and the `raise` helper |

## Conventions

- **The typed-nil trap is closed explicitly.** `newHTMLRenderer` exists solely
  so a failed construction hands back a genuinely nil `coreview.Renderer`.
  Returning `newRenderer`'s `(*renderer, error)` pair through the interface
  would produce a NON-NIL `Renderer` holding a nil pointer on every failure —
  in a domain whose whole contract is that a broken tree stops the deploy.
- **`RenderTooLarge` is recognised by identity, never by message.**
  `text/template` strips its own `writeError` wrapper and returns the writer's
  error verbatim, so the sentinel `limitWriter` returned arrives at
  `renderFailure` unchanged.
- **The engine's diagnostic is a FIELD and never a Public.** `html/template`'s
  execution error carries an absolute path, a line, a column, a fragment of
  template source and a Go type name. `raise` attaches it as `engine_error`;
  ADR 0005 keeps Fields out of `err.Error()`. A framework that serialises
  `errs.FieldsOf` into a response has re-opened the split.
- **`UnsafeValue` names the path and the type, never the value** (ADR 0046's
  rule). The path uses GO field names — `data.Items[1].Body` — not json tags,
  because a template author reads `{{.Items}}` and a developer greps the struct
  field. That is the opposite call from `validation`, whose input arrived as a
  decoded document.
- **The trust-type scan is gated on the model's static TYPE.** Whether a type
  can transitively hold one of the six is computed once and cached in a
  `sync.Map`; a struct of strings and ints is pruned without touching a value.
  Measured (`BENCH.md`): **445.9 ns** for a typed model, **13 657 ns** for the
  equivalent `map[string]any`.
- **Cycles follow `encoding/json`'s policy.** Pointers are recorded only past
  `startDetectingCyclesAfter` (1000), so a normal model allocates no
  bookkeeping. Two named tests drive an immediate self-loop and a 1200-link
  ring that closes past the threshold.

## The performance rule

**Parse once.** Build the `Renderer` at start-up and keep it. Constructing one
per request re-reads the tree, re-parses every file and re-runs the escaping
analysis: on a forty-two-template tree that is **33.9× the time, 34.4× the
bytes and 8.1× the allocations** (`BENCH.md`). The ratio scales with tree size
— it is only 1.7× on a two-file tree, which is why a measurement taken on a toy
tree understates the defect.

The contract's own cost over raw `html/template` is **two allocations and one
copy of the document**. The copy is deliberate and deliberately not optimised
away: the scratch goes back to `recycler.CappedPool` and returning it would let
the next render overwrite the caller's document.

## Do NOT

- **Import `text/template`.** `TestTheDomainNeverReachesTextTemplate` parses
  every `.go` file in all three view packages — production and test — and fails
  the build. The two packages are API-compatible, so the substitution compiles
  and ships stored XSS.
- **Return the pooled buffer instead of copying out.** The next render
  overwrites it. `TestTheReturnedDocumentIsNotAliasedByTheNextRender` guards it.
- **Register custom template functions.** Helpers are out of scope for the
  domain (ADR 0058), and a registered function would also run during the
  escaping probe, which currently executes nothing user-written.
- **Make the escaping probe lenient**, or move it to first render. It is the
  whole reason a broken tree is a deploy that does not come up.
- **Skip the trust-type scan for "trusted" callers.** There is no such
  distinction available at run time — see ADR 0058 §D4.
- **Add a reload, a lazy parse or an internal template cache.** Each puts a
  mutex on the read path to solve a problem a package-level variable solves.

## Verification

```
bazel test --config=race //internal/service/view:view_test
# or: cd internal/service && GOWORK=off go test -race ./view/...

# benchmarks + the profiles quoted in BENCH.md
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./view/
```
