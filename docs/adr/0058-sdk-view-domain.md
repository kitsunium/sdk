# ADR 0058 — server-side rendering domain (`view`): a contract ON TOP of `html/template`, one trust type, and the XSS the SDK cannot see

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal — both halves are used here), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0033](0033-consumer-rule-enforcement.md) (`tools/sdkguard`, where the consumer-side ban on the raw conversion belongs), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (no SDK default writes to a stream), [ADR 0043](0043-drain-is-a-signal-not-a-cancellation.md) (a response that must be allowed to finish), [ADR 0046](0046-sdk-validation-domain.md) (a message names the rule and never the value), [ADR 0010](0010-kernel-recycler-primitive.md) (`recycler.CappedPool`), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (Public/Private/Fields)

## Context

The SDK can now serve HTTP, negotiate TLS, stream Server-Sent Events and speak
WebSocket (ADR 0029/0043/0047), and it has no way to produce an HTML document.
Every consumer therefore writes the same fifteen lines: `template.ParseFS`, a
package-level `*template.Template`, and `tmpl.ExecuteTemplate(w, name, data)`.

Three defects are in those fifteen lines, and all three are silent.

**`ParseFS` names templates by `filepath.Base`.** A tree holding
`admin/page.html` and `user/page.html` ends up with **one** template called
`page.html`. Measured: the second parse wins, there is no error and no warning,
and every request for the admin page renders the user page — with the admin
page's model. Nothing about the calling code looks wrong.

**Escaping is resolved lazily, at a template's first `Execute`.** Three failure
classes exist only at that moment: a template ending inside an unterminated tag
(`ErrEndContext`), an `{{if}}` whose branches end in different escaping
contexts (`ErrBranchEnd`), and a `{{template "x"}}` naming something absent
(`ErrNoSuchTemplate`). The third is the everyday one — a partial gets renamed,
every test that exercises another page passes, and the failure ships as a 500
on whichever request first reaches that page.

**`ExecuteTemplate(w, …)` writes as it goes.** Template execution can fail
halfway: a nil pointer in a field chain, a method returning an error, a range
over the wrong type. Handed an `http.ResponseWriter`, `html/template` has
already flushed the status line, the headers and a plausible-looking prefix of
the page before it reports the failure. At that point 500 is not sendable and
the browser renders a truncated document. A caller cannot avoid this by being
careful — only by not being given the opportunity.

To which the ticket adds a fourth requirement, "contextual escaping", and the
first job of this ADR is to refuse to build it.

## Decision

Add `view` as a core sibling: `internal/core/view` (`0.2.27.*`),
`internal/service/view` (`0.3.57.*`), `pkg/v1/view`. **One engine ships**, the
standard library's `html/template`, behind a `Renderer` port and a `Factory`
registry.

### D1 — `html/template` is USED, not reimplemented, and that is the security decision

The SDK's doctrine is low-level, total control, minimal dependencies. Applied
without thinking, it says "write the escaper". Applied to *this* problem, it
says the opposite, for three reasons that all point the same way.

**`html/template` is the standard library.** It is already zero dependencies
and already total control in the only sense the doctrine cares about: no
third-party code, no supply chain, no version treadmill. There is no
dependency here to minimise.

**Contextual escaping is not an escaping function, it is an HTML parser.**
Deciding how to escape a value requires knowing whether the cursor is between
two tags, inside an unquoted attribute, inside a quoted one, inside a `<script>`
block, inside a CSS rule, or inside a URL — and tracking that through
`{{if}}`/`{{range}}` branches that must agree on where they leave the cursor.
`html/template` is roughly six thousand lines of exactly that, with a
per-context escaper, a state machine over HTML5's tokenisation quirks, and
`ZgotmplZ` for the case where a URL scheme cannot be proven safe.

**A reimplementation would be a security REGRESSION, not a gain.** The domain
exists to prevent cross-site scripting. A hand-written escaper would become the
single most likely source of it in the SDK — it would be new, unaudited, and
would have to be right about `<textarea>` vs `<script>` vs `srcset` vs
`javascript:` on the first try, against a body of published bypasses that
`html/template` has been hardened against for a decade. Writing it would trade
a heavily audited implementation for an unaudited one and call the trade
"control".

So the domain builds the **contract** on top of the engine. What the SDK adds
is everything in §Context that `html/template` does not do, plus the port a
framework can be wired to.

### D2 — `text/template` has NO representation in this domain, and an AST audit enforces it

`text/template` and `html/template` are API-compatible. Swapping one import for
the other **compiles**, passes every test that does not assert on escaped
output, and ships stored XSS. One is an HTML-aware escaper; the other is string
concatenation with a syntax.

A documented rule would survive until the afternoon somebody needs to render an
email body. So the rule is structural:
`TestTheDomainNeverReachesTextTemplate` parses every `.go` file in all three
view packages — production **and** test — and fails the build on the import.
Test files are included deliberately: a test importing `text/template` is a
test that can demonstrate the unescaped behaviour looks fine, which is the
argument that would precede the production import.

Plain-text templating is out of scope **by name**. If it is ever wanted it
arrives as its own domain with its own port — never as a second engine in this
registry, because a registry exists to make its members interchangeable and an
escaping engine is not interchangeable with a non-escaping one.

### D3 — The port, frozen at two, returning bytes and never taking a writer

```go
type Renderer interface {
	Render(ctx context.Context, name string, data any) ([]byte, error)
	ContentType() string
}

type Factory interface {
	Engine() Engine
	New(cfg Config) (Renderer, error)
}
```

`Renderer` is **frozen at two methods**. `pkg/v1/view` aliases it and Go
interfaces are structural, so a third method breaks every downstream
implementer at compile time with no deprecation window (ADR 0039). New
capabilities arrive as sibling interfaces reached by type assertion, as
`codec`'s `Appender` does. `TestATwoMethodDoubleStillSatisfiesRenderer` is the
guard.

`Render` returns `([]byte, error)` and does **not** take an `io.Writer`. This
is the third defect in §Context answered by the signature rather than by
advice: the engine renders into a bounded buffer it owns and hands back the
complete bytes or nothing at all. The caller's destination is never touched on
a failed render because the caller's destination is never passed in.

`ContentType` is a method rather than a package constant because it is the
promise an engine makes about the escaping it performs. An engine that does not
contextually escape may not report an HTML content type — so a framework
writing this header verbatim never labels unescaped output as a document the
browser will parse as HTML. The value carries `charset=utf-8`, which is not
decoration: a response with no charset is sniffed, and a document sniffed as
UTF-7 can smuggle markup past an escaper that judged the bytes as UTF-8.

Nothing in the domain writes anywhere, which is how ADR 0030 is satisfied —
not by choosing stderr, but by having no destination in the port at all.

### D4 — `Trusted`: one type, one spelling, and an alias that costs something

`TrustedHTML` is a **type alias** of `html/template.HTML`:

```go
type TrustedHTML = template.HTML
func TrustHTML(s string) TrustedHTML { return TrustedHTML(s) }
```

It is an alias and not a defined type because `html/template` recognises its
trust types by an **exact type switch**. A `type TrustedHTML string` would be
escaped like any other string — measured, it renders `<b>hi</b>` as
`&lt;b&gt;hi&lt;/b&gt;` — and a trust type that does not confer trust is worse
than none, because callers stop looking for the reason their markup
disappeared.

**What the alias costs is the subject of this section, and it is stated rather
than buried.** Because `TrustedHTML` and `template.HTML` are the same type, the
engine **cannot tell** a fragment marked through `TrustHTML` from one a caller
converted directly with `template.HTML(s)`. There is no run-time difference to
detect, because there is no difference.

So what the SDK buys is precise, and worth naming exactly:

- **Six of the seven trust types are REFUSED.** `CSS`, `HTMLAttr`, `JS`,
  `JSStr`, `URL` and `Srcset` are rejected when they appear anywhere in render
  data, before the template runs, with the path that carries them named in the
  error. None of the six has an SDK spelling at all —
  `TestTrustHTMLIsTheOnlyTrustConstructorTheDomainOffers` parses the package's
  own source and fails if a `TrustCSS`, `TrustJS`, `TrustURL` … ever appears.
  Each of them disables contextual escaping for a context where a plain string
  could not have done harm: measured,
  `template.URL("javascript:alert(1)")` reaches the browser as
  `href="javascript:alert%281%29"` and **executes**, while the identical string
  left as a `string` is rewritten to the inert `#ZgotmplZ`.
- **The seventh has one canonical, greppable spelling.** "Where does this
  codebase decide to trust markup?" is one grep — `TrustHTML` — with one
  answer, rather than a hunt through an import list for `template.HTML(`.

**What `Trusted` does NOT prevent, said plainly: a `Trusted` the caller built
wrongly is an XSS the SDK cannot see.** `view.TrustHTML(r.FormValue("bio"))`
compiles, renders, and is a stored cross-site scripting vulnerability. The SDK
has no way to distinguish it from `view.TrustHTML(markdown.Render(post))`,
because both produce the same type holding the same kind of value. The domain
does not prevent a bad trust decision — it prevents an **accidental** one, and
gives the deliberate one a name a reviewer can find. Every call is an assertion
that the string was produced by the server and sanitised; it is never correct
to hand it a value that arrived from a request, from a user-controlled database
field, or from a third-party API.

A build-time ban on the raw `template.HTML(…)` conversion in **consumer** code
belongs in `tools/sdkguard` (ADR 0033), which already reads consumer source for
exactly this class of rule. It is not shipped here, and it is named as the
follow-up rather than left implied.

### D5 — Refusing the six is a scan, the scan is on the hot path, and the type pays for it

The refusal in D4 needs the render data inspected before execution. Done
naively that is a reflective walk of the whole model on every request, which
would make the safety property expensive enough that somebody eventually turns
it off.

It is not naive. Whether a value can transitively hold one of the six is a
property of its **static type**: a struct of strings, ints and slices of those
cannot hold a `template.URL` whatever its contents. That answer is computed
once per type, cached in a `sync.Map` (validation's plan-cache shape), and used
to prune. Only an interface-typed member — `any`, `map[string]any` — forces a
value-level walk, because only there is the dynamic type unknown until render
time.

Measured, on a twenty-row page (`internal/service/view/BENCH.md`):

| model | scan |
|---|---:|
| typed view model | **445.9 ns/op** — inside the noise of `Render` itself |
| equivalent `map[string]any` | **13 657 ns/op**, ≈ 10 % of a 130 µs render |

The gate is therefore also an argument for a typed view model that has nothing
to do with taste. The walk itself was profiled twice and optimised twice —
`SetIterKey`/`SetIterValue` removed 65 % of its allocations, and ordering the
checks by `reflect.Kind` before the type lookups removed 36 % of its time. Both
are recorded with their profiles in `BENCH.md`.

Two limits of the scan, stated because they are invisible:

- **A cycle below 1000 levels deep is followed, not detected.** The threshold
  and the technique are `encoding/json`'s: pointers are recorded only past
  `startDetectingCyclesAfter`, so a normal model allocates no bookkeeping at
  all. A self-referential model terminates — two named tests drive one
  immediate self-loop and one 1200-link ring that closes past the threshold.
- **The scan sees only what `reflect` can reach.** A value produced by a method
  the template calls is not in the model and is not scanned. `html/template`
  still escapes it correctly unless it is one of the six, which is exactly the
  case a method returning `template.URL` creates. That is a hole, it is named
  here, and closing it would mean intercepting the engine's own function calls.

### D6 — Everything permanent is refused at CONSTRUCTION, including the escaping plan

`New` walks `Config.FS`, parses every selected file, and then **forces
`html/template` to build each template's escaping plan** by executing it once
against `nil` into `io.Discard`.

That probe is the answer to the second defect in §Context. All three lazy
failure classes — `ErrEndContext`, `ErrBranchEnd`, `ErrNoSuchTemplate` —
become a refused constructor instead of a 500 on a live request. A tree that
does not parse and a template whose escaping cannot be resolved will fail
identically on every request forever, so a deploy that does not come up is a
cheaper way to learn that than a 500 on the one page nobody smoke-tested.

Only an **escaping** failure is fatal. Executing against `nil` can legitimately
fail — a field chain on nothing — and refusing on that would make every
template that reads its model unusable, so the probe matches
`*template.Error` and ignores everything else. No custom functions are
registered (helpers are out of scope), so nothing user-written runs during the
probe; escaping is idempotent and cached, so it leaves every template ready for
its first real render.

An **empty tree is not an error**. It produces a `Renderer` that refuses every
name with `TemplateNotFound` — loud rather than inert, which is the half of
ADR 0031 that applies to a configuration nobody filled in.

`Config.FS` is an `fs.FS`, so `embed.FS`, `os.DirFS` and `fstest.MapFS` are all
first-class and the domain never touches the filesystem API. It also removes
path traversal from the problem statement: `fs.ValidPath` rejects `..`, so no
template name can address a file outside the tree the caller handed over.

> **On `vfs` (ADR 0056, merged on `main`; not on this branch at the time of
> writing, so this paragraph is a decision taken against its ADR rather than
> against its code).** `vfs.FS` is a type ALIAS of `io/fs.FS`, so depending
> on it here would buy exactly nothing at the type level while adding a
> dependency from `view` to another domain. `Config.FS` is therefore
> `io/fs.FS`, which a `vfs.FS` satisfies with no adapter and no conversion. The
> write half of `vfs` has no meaning for a renderer: this domain reads
> templates and writes nowhere. If `vfs` ever grows a read capability that is
> not `io/fs`, this is the field that would take it — as a sibling, not by
> widening `Config`.

### D7 — Templates are named by their full SLASH PATH

`set.New(entry).Parse(...)` with `entry` the slash path relative to the FS
root, not `filepath.Base`. This is the first defect in §Context, refused rather
than inherited: `admin/page.html` and `user/page.html` are two templates, and
`page.html` resolves to neither.

It also makes `{{template "partial/row.html" .}}` resolve across files in one
namespace, so template composition is `html/template`'s own `{{define}}` and
`{{template}}` — which this domain neither extends nor conventionalises.

`TestTemplatesAreNamedByTheirFullSlashPath` asserts both halves, including that
the basename does **not** resolve.

### D8 — A runaway template is bounded by BYTES, and there is no "unlimited"

`text/template`'s `Execute` takes no `context.Context` and cannot be
interrupted. `Render` therefore checks `ctx.Err()` before the work starts and
does not consult it again — an honest statement of what the engine permits,
rather than a cancellation the port cannot honour.

The only bound that exists is `Config.MaxBytes`, enforced by the writer the
engine executes into, and it has **no spelling for unlimited**. The stdlib's
own guard is a recursion DEPTH limit (100 000 frames), which says nothing about
output size: a `{{range}}` over an attacker-influenced collection has no depth
at all and will write until the machine stops.

The ceiling **stops the render at the limit rather than after it** — the
over-long write is refused whole and never copied into the buffer, so the
process does not have to survive the allocation in order to reject it. The
partial output is discarded, never returned.

A non-positive `MaxBytes` **clamps** to 8 MiB rather than being refused. This
is ADR 0031's other half, and the two are distinguished on the same test the
lock domain used: a lock's zero TTL has two defensible readings that are
opposites, so it must be refused; "an HTML document should not exceed N" has a
defensible universal answer, so refusing `view.Config{FS: templates}` would
make the obvious spelling unusable for no safety gain. Both halves of ADR 0031
therefore appear in this one `Config` — `FS` refuses, `MaxBytes` clamps — and
the contrast is deliberate.

**A template can still loop forever without producing bytes.** `{{range}}` over
an infinite generator that emits nothing, or a deeply mutually recursive
`{{template}}` chain, is bounded by the stdlib's frame limit or by nothing at
all. The domain does not claim otherwise: the ceiling is on OUTPUT, and a
render that produces no output is not stopped by it. A caller who accepts
templates from untrusted authors has a problem this domain does not solve, and
should not be accepting them.

### D9 — Errors: the engine's diagnostic is the most leak-prone string in the domain

`html/template`'s execution error is, verbatim:

```
template: /srv/app/web/admin/page.html:1:7: executing "…" at <.User.Name>:
nil pointer evaluating interface {}.Name
```

An absolute filesystem path, a line, a column, a fragment of the template's own
source, and a Go type name. All of it is useful to an operator and all of it is
reconnaissance to a stranger.

Every render verdict therefore carries a `Public` that names **nothing** — not
a template, not a path, not a line, not a data key, not a fragment of source —
while the engine's message travels as a **Field**, which ADR 0005 keeps out of
`err.Error()`. Two named tests hold both halves: one asserts that four distinct
disclosure classes (the template's path, a fragment of its source, the failing
expression, and the caller's own rendered data) appear in neither `Error()` nor
`PublicOf` nor `PrivateOf`; the other asserts the diagnostic **is** present in
`engine_error` and is **not** folded into `Error()`, because an error that
discloses nothing and helps nobody is not the goal.

`UnsafeValue` follows ADR 0046's rule for the same reason: it names the **path**
and the **type** and never the **value**, because the path is structure the
developer owns and the value is data somebody else does. The path uses Go field
names (`data.Items[1].Body`) rather than json tags — the opposite of
`validation`, and for a stated reason: the vocabulary a template author reads
is `{{.Items}}` and the vocabulary a developer greps for is the struct field,
whereas `validation`'s input arrived as a decoded document.

A framework that serialises `errs.FieldsOf` into a response has re-opened the
split this domain depends on. That is said in `errors.go`, in the package
`CLAUDE.md`, and here.

### D10 — There IS a registry, and this is the one domain where that is the right call

`proc`, `resilience`, `net`, `scheduler`, `token`, `session`, `lock`,
`lifecycle` and `cache` all deliberately have no registry. Their alternatives
are not interchangeable — swapping a memory lock for a file lock changes
whether a lease can be taken from a live holder — so resolving one from a
configuration string would let a typo change semantics silently (ADR 0052 §D10).

A template engine is the other case. `html/template`, pongo2, quicktemplate and
jet all answer the same question — a name plus a model becomes a document — and
the SDK has an opinion about none of them. The registry is what lets the SDK
ship the extension point without shipping the opinion: an engine living in
`third-party/` or in a framework registers itself and is reached through the
same `Open` as the built-in one, with no fork of the port.

What it does **not** buy is worth saying, because the codec and writer
registries do buy it: swapping engines is not a deployment decision. Template
syntax differs between engines, so changing the engine invalidates every
template file on disk. Nobody flips this in a config map between staging and
production, and a reader who assumes otherwise will design a fallback that
cannot work.

The hazard a string-keyed registry creates **here** is sharper than elsewhere,
and it is answered rather than accepted:

- `Open` on an unclaimed name is `EngineUnknown`, never a fallback. A fallback
  would let a typo in a configuration file silently choose how every value in
  the program is escaped.
- Two **distinct** factories under one name **panic at import**. Last-write-wins
  in this registry can mean the engine that escapes was replaced by one that
  does not, at import time, with no call site to blame. Re-registering the
  **same** factory is an idempotent no-op, because re-running an import is not
  an error.
- Registration is a documented **promise**: contextual escaping for the media
  type the engine reports, all-or-nothing bytes, `MaxBytes` honoured, a
  construction-time refusal for a `Config` it cannot honour, and full-path
  naming. It is enforced socially and by D2's audit; it is not enforced by the
  type system, which is stated rather than implied.

### D11 — Parse once. It is the one performance rule the domain has, and it is measured

A `Renderer` is immutable after construction: no `Add`, no `Reload`, no lazy
parse. That means no lock on the read path and no request can observe a
half-built set — and it means the caller owns the lifetime.

Constructing one per request re-reads the tree, re-parses every file and
re-runs the escaping analysis before rendering the one page anybody asked for.
On a forty-two-template tree that is **33.9× the time, 34.4× the bytes and 8.1×
the allocations** (`BENCH.md`). The ratio is a function of tree size — on a
two-file tree the same mistake costs 1.7×, small enough to survive a review and
to look like noise in staging — so a measurement taken on a toy tree
understates the defect by exactly the factor the real tree is bigger.

There is deliberately no reload, no lazy parse and no internal cache. Each
would put a mutex on the read path to solve a problem a package-level variable
already solves.

The contract's own overhead over raw `html/template` is **two allocations and
one copy of the document** — 130 894 vs 132 854 ns/op, 15 082 vs 13 007 B/op.
The copy is `Render` taking the pooled scratch out of the pool's ownership, it
is 13.3 % of every byte a render allocates, and it is deliberately not
optimised away: returning the pooled buffer would let the next render overwrite
the caller's document.

## Consequences

- Consumers get contextual escaping they did not write, full-path template
  names, a construction-time verdict on the whole tree, an all-or-nothing
  render, a byte ceiling, and typed errors that do not leak — for the cost of
  one document copy per render.
- `internal/core/view` `0.2.27.*` and `internal/service/view` `0.3.57.*` are
  allocated in `internal/kernel/errs/registry_ownership_external_test.go` in
  the same change (ADR 0035).
- `pkg/v1/view` publishes `Renderer`, `Config`, `Factory`, `Engine` and
  `TrustedHTML` as aliases. Every one of them is now frozen under ADR 0039.
- The domain has **one** engine. A second one arriving is a `Factory`, not a
  patch to this port.
- `tools/sdkguard` gains a candidate rule — ban `template.HTML(…)`,
  `template.URL(…)` and their four siblings in consumer code — recorded here as
  a follow-up, not shipped.

## Why not …

**… write the escaper?** §D1. It would trade the most heavily audited HTML
escaper in the Go ecosystem for an unaudited one and call the trade "control".
The doctrine asks for no third-party dependency and total understanding of what
ships; `html/template` is the standard library and satisfies both.

**… take an `io.Writer` and stream?** §D3. Streaming makes a mid-execution
failure unreportable: the status line and part of the body are already on the
wire. A caller who genuinely wants streaming has `html/template` directly and
has accepted the consequence explicitly; the SDK's port does not offer it by
default, because a default is what someone gets before they know the question
exists (ADR 0030's reasoning, applied to a different hazard).

**… make `TrustedHTML` a distinct type so the SDK can tell trusted from
converted?** It would not be trusted. `html/template` type-switches on its own
types; a distinct type is escaped like any other string. §D4.

**… allow the six trust types with a warning?** A warning is a log line nobody
reads on a path that produces an exploit. The refusal is at render time,
before execution, with the path named — which is the only form of it a
developer can act on.

**… run the trust-type scan only in a debug build?** Then the property is
absent exactly where it matters. §D5 measures the cost instead: free for a
typed model, 10 % of a render for `map[string]any`.

**… cancel a render on context expiry?** `text/template.Execute` takes no
context and cannot be interrupted. Offering a cancellation the engine cannot
honour would be a lie in the port's signature; the ceiling on OUTPUT is the
bound that actually exists. §D8.

**… add layouts, blocks, helpers, i18n and an asset pipeline?** Each is a
framework opinion. A domain holding them would be a rendering framework wearing
a port's name. Composition is `{{define}}`/`{{template}}`, which the engine
already provides.

**… load templates through `vfs.FS` (ADR 0056)?** `vfs.FS` is a type alias of
`io/fs.FS`.
Depending on it buys nothing at the type level and adds a domain-to-domain
dependency; `io/fs.FS` accepts a `vfs.FS` unchanged. §D6.

## Deferred

- **A second engine.** Nothing is designed against a hypothetical one; the
  registry exists so it needs no change here.
- **The `sdkguard` rule** banning the six raw conversions in consumer code
  (ADR 0033). Named in §D4.
- **A method-return scan.** A `template.URL` returned by a method the template
  calls is not in the model and is not seen. §D5.
- **Streaming for very large documents.** Would require a different port, not a
  widened one, and would have to state what a caller loses.
- **A `Renderer` sibling exposing the parsed set** (`Names() []string`,
  `Has(name) bool`). Plausible, unrequested, and an ADR 0039 sibling when it
  arrives.

## References

- Go stdlib — `html/template`: contextual auto-escaping, the seven trust types,
  `ErrEndContext` / `ErrBranchEnd` / `ErrNoSuchTemplate`, `ZgotmplZ`.
- Go stdlib — `text/template`: `Execute` takes no context; `maxExecDepth`
  bounds recursion depth and not output.
- Go stdlib — `encoding/json`: `startDetectingCyclesAfter`, the cycle policy
  §D5 reuses.
- `internal/service/view/BENCH.md` — every number quoted in §D5, §D8 and §D11,
  with the profiles they came from.
