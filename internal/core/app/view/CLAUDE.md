<!-- updated: 2026-10-04T11:15:00Z -->
# internal/core/app/view/

## Purpose

The SDK's server-side rendering **domain**: the `Renderer` port that turns a
named template plus a data value into bytes, the `Factory` registry that
resolves one implementation by name, and the single trust type through which a
caller can deliberately bypass escaping. Admitted by **ADR 0058**. The one
engine that ships — stdlib `html/template` — lives in `internal/service/app/view`.

Code ranges: `0.2.27.*` (ADR 0058) for the port, and `0.3.57.*` for the
html/template engine's construction failures — allocated to
`internal/service/app/view`, which raises them, and declared here since
ADR 0160. A code keeps its value when its declaration moves.

**The ports are generated from the design** (ADR 0163): `Factory` and
`Renderer` are declared, with their doc comments, under `ports:` in
`design/app/view.yaml`, and `kit gen` writes them into `design_gen.go`. A port
or its doc comment changes in the design, then `kit gen`, then `make api` —
never in `design_gen.go`, whose header digests `make api-check` verifies. They
moved there from `factory.go` and `view.go`, content moved and never deleted —
but for the blank line each put between its two methods, which the design does
not record: `go doc` shows both declarations without it.

## Why this shape

**The domain is HTML, and that is a security boundary rather than a scope
note.** `html/template` escapes ACCORDING TO CONTEXT: the same value is escaped
one way between two tags, another inside an attribute, another inside a URL and
another inside a `<script>` block. `text/template` does none of that — it is
string concatenation with a syntax — and the two packages are API-compatible,
so swapping one import for the other **compiles**, passes every test and ships
stored XSS.

The domain therefore has **no `text/template` representation at all**: not an
`Engine` name, not a `Factory`, not a constructor, not an import.
`TestTheDomainNeverReachesTextTemplate` (in `internal/service/app/view`) parses the
source of all three view packages — production and test — and fails the build
on the import.

**Rendering is all-or-nothing, and the signature enforces it.** `Render`
returns `[]byte`, never taking an `io.Writer`. Template execution writes
incrementally and can fail halfway; handed an `http.ResponseWriter` it would
have flushed the status line, the headers and a plausible prefix of the page
before the failure is detectable, and at that point 500 is no longer sendable.
A caller cannot avoid this by being careful, only by not being given the
opportunity.

**Nothing here writes anywhere**, which is how ADR 0030 is satisfied — not by
choosing stderr, but by having no destination in the port at all.

**Both halves of ADR 0031 live in one `Config`**, deliberately, for contrast:

| Field | Zero value | Why |
|---|---|---|
| `FS` | **REFUSED** (`ViewMisconfigured`) | there is nothing to render and no tree to guess at |
| `MaxBytes` | **CLAMPED** to `DefaultMaxBytes` (8 MiB) | unlike a lease TTL, "an HTML document should not exceed N" has a defensible universal answer, and refusing `view.Config{FS: templates}` would cost usability for no safety |

## Surface

| Symbol | Notes |
|---|---|
| `Renderer` | `Render` / `ContentType`. **FROZEN at two** (ADR 0039) — guarded by `TestATwoMethodDoubleStillSatisfiesRenderer` |
| `Factory` | `Engine` / `New`. Registering is a promise the security model rests on |
| `Engine` / `HTML` | the registry key; `Engine("")` is the reserved invalid zero value |
| `Config` | `FS` (required) / `Ext` / `MaxBytes` |
| `TrustedHTML` / `TrustHTML` | the ONE bypass, and the only spelling the SDK offers for it |
| `ContentTypeHTML` | carries `charset=utf-8` — a charset-less response is sniffed |
| `DefaultMaxBytes` / `MaxPooledBytes` | 8 MiB render ceiling; 1 MiB pool ceiling |
| `Register` / `Lookup` / `Available` / `Open` | the process-wide registry — an instance of `kernel/plugin.Registry` (ADR 0159); what stays here is its refusals and `Open` |
| `ViewMisconfigured` `0.2.27.1` | constructor refusal — a nil FS, an unparseable tree, an unresolvable escaping context |
| `TemplateNotFound` `0.2.27.2` | a name the engine does not hold, including `""` |
| `RenderFailed` `0.2.27.3` | execution started and could not finish |
| `RenderTooLarge` `0.2.27.4` | `Config.MaxBytes` reached; the partial output is discarded |
| `UnsafeValue` `0.2.27.5` | render data carries one of the six refused trust types |
| `EngineUnknown` `0.2.27.6` | `Open` for a name no factory claims |
| `EngineInvalid` `0.2.27.7` | boot-time panic: an unusable `Factory` — nil, a typed nil pointer or a non-comparable value (`internal/kernel/plugin`, ADR 0071) — or an empty `Engine` |
| `DuplicateEngine` `0.2.27.8` | boot-time panic: two DISTINCT factories under one name |
| `TemplateSourceFailed` `0.3.57.1` | the html/template engine could not READ its tree — a walk or an open failed |
| `TemplateParseFailed` `0.3.57.2` | the engine refused a template at parse time, or could not resolve its escaping context |

## Conventions

- **Template names are full SLASH PATHS** within `Config.FS`. `html/template`'s
  own `ParseFS` names by `filepath.Base`, so `admin/page.html` and
  `user/page.html` collapse into one `page.html` — measured, the second parse
  silently wins. Full-path naming is the SDK refusing to inherit that.
- **`TrustedHTML` is a type ALIAS**, not a defined type. `html/template`
  recognises trust by an exact type switch, so a defined type would be escaped
  like any other string and would confer no trust at all. The consequence — the
  engine cannot tell a `TrustHTML` fragment from a raw conversion — is stated
  in `trusted.go` and in ADR 0058 §D4, not hidden.
- **Six trust types have no spelling here.** `CSS`, `HTMLAttr`, `JS`, `JSStr`,
  `URL` and `Srcset` are refused in render data.
  `TestTrustHTMLIsTheOnlyTrustConstructorTheDomainOffers` parses this package's
  own source and fails if a `TrustCSS`, `TrustURL`, … ever appears.
- **No Public names a template, a path, a line, a data key or a fragment of
  source.** Two named tests hold it. What an operator needs travels in Fields
  and in Private.
- **A context cancellation returns `ctx.Err()`**, not an SDK sentinel — the
  caller supplied the deadline. Follows `resilience` and `lock`.
- **Registration goes through a package-level `var` initialiser, not `init()`**
  — mirrors `core/data/codec` and `core/observe/logger/writer`.

## Do NOT

- **Add a method to `Renderer`.** `pkg/v1/app/view` aliases it and Go interfaces
  are structural: a third method breaks every downstream implementer at compile
  time with no deprecation window (ADR 0039). New capabilities are siblings.
- **Import `text/template`, here or anywhere in the domain.** See §Why this
  shape. The audit fails the build.
- **Add a second trust constructor.** Six of the seven types are refused by
  design; a `TrustURL` would hand a caller the exact primitive
  `template.URL("javascript:alert(1)")` exploits.
- **Give `Config.FS` a default, or add a directory-path field.** A nil FS is
  refused; an `fs.FS` also removes path traversal from the problem statement,
  since `fs.ValidPath` rejects `..`.
- **Add an "unlimited" spelling for `MaxBytes`.** `Execute` takes no context
  and cannot be cancelled; the byte cap is the only bound that exists.
- **Let `Open` fall back to a default engine.** A typo in a configuration file
  would silently choose how every value in the program is escaped.
- **Weaken the paragraph about what `TrustedHTML` does not prevent.** A
  `TrustedHTML` the caller built wrongly is an XSS the SDK cannot see. It is stated in four
  places on purpose.

## Verification

```
bazel test --config=race //internal/core/app/view:view_test
# or: cd internal/core && GOWORK=off go test -race ./app/view/...
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `Config` and `Engine` — each struct with every field, unexported ones included; `Available` and `TrustHTML`, each one call of its unexported body, measured to inline with the body inlined into it. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name.
