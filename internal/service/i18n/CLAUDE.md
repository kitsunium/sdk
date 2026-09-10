# internal/service/i18n/

## Purpose

The concrete half of the message-translation domain (**ADR 0063**): the
hand-written CLDR plural rule table, the immutable `Store` that compiles a
catalogue, the `Negotiator` that resolves an `Accept-Language` header, and the
`Printer` that renders. The port, the values and the typed refusals live in
`internal/core/i18n`.

Code range: `0.3.60.*` (ADR 0063).

## Contents

| File | Surface |
|---|---|
| `plural.go` | the CLDR rule table (13 entries) + `Rules` / `SupportedTags` + the thirteen transcribed rules |
| `plural_value.go` | `PluralValue` — one language's categories and its rule |
| `entry_value.go` | `EntryValue` + `Catalogue` + `Plain` / `PluralForms` |
| `store.go` | `Store` + `NewStore` + `Lookup` / `Tags` / `Keys` / `Fallback` / `Missing` |
| `store_compliance.go` | the compile-time proof that `Store` satisfies the port and both siblings |
| `load.go` | `LoadFS` — one catalogue file per language, decoded through `codec`, read through `io/fs` |
| `printer.go` | `Printer` + `NewPrinter` + `Tag` / `Render` / `RenderCount` |
| `printer_link.go` | the resolution chain: `link` + `buildChain` / `appendLineage` |
| `negotiator.go` | `Negotiator` + `NewNegotiator` + `Supported` |
| `negotiate.go` | `Negotiate` + RFC 4647 §3.4 lookup / §3.3.1 refusal + `subtagPrefixFold` |
| `accept.go` | the RFC 9110 `Accept-Language` parser (bounded, allocation-free) |
| `codes.go` | `Code*` constants — range 0.3.60.* |
| `errors.go` | `UnsupportedLanguage` / `CatalogInvalid` / `CatalogLoadFailed` / `TranslationIncomplete` / `NegotiationEmpty` + `failLoad` |

## Supported languages

Thirteen entries, each transcribed by hand from the CLDR cardinal chart with
its clause quoted above the function:

| Entry | Categories | Shape |
|---|---|---|
| `ja`, `zh` | `other` | no plural distinction — these carry **no rule at all** |
| `de`, `en`, `nl` | `one`, `other` | `i = 1 and v = 0` |
| `es` | `one`, `many`, `other` | singular on `n = 1`, so "1,0" IS `one` |
| `fr`, `pt` | `one`, `many`, `other` | singular on `i = 0,1`, so **0 is singular** |
| `it` | `one`, `many`, `other` | Germanic singular, Romance `many` |
| `pt-PT` | `one`, `many`, `other` | Germanic singular — CLDR's own divergence from `pt` |
| `pl` | `one`, `few`, `many`, `other` | 21 is `many` |
| `ru` | `one`, `few`, `many`, `other` | 21 is `one` |
| `ar` | all six | the only entry using `zero` and `two` |

`Rules` resolves the **exact tag first, then the bare language subtag**, so
`fr-CA`, `de-AT` and `zh-Hant` inherit their language's rules and `pt-PT` gets
its own. Everything else is `UNSUPPORTED_LANGUAGE` at construction, naming the
tag and the supported set.

### Adding a language

One change, four parts, none optional:

1. the rule function in `plural.go`, with the CLDR clause quoted above it;
2. its category set (reuse a `forms*` slice or add one);
3. the `rulesByTag` entry;
4. its **boundary values** in `plural_external_test.go` — not a sample, the
   values the clauses turn on — and the `supportedCount` constant, which is
   asserted so the addition cannot be silent.

`TestEveryRuleOnlyEverReturnsACategoryItDeclares` and
`TestEveryDeclaredCategoryIsActuallyReachable` then hold the new entry to the
same bar as the thirteen: the set it declares and the set it produces must be
the same set.

## Conventions

- **Everything that can be wrong is refused at CONSTRUCTION.** `NewStore`,
  `NewPrinter` and `NewNegotiator` each fail loudly; `Render`, `RenderCount`
  and `Negotiate` have almost nothing left to fail at. At startup a human is
  present and the catalogue file is in front of them; at render time the caller
  is a request and the only repairs are to show something wrong or nothing.
- **`Negotiate` NEVER fails.** No error return at all. An unusable element is
  skipped, an empty or garbage header resolves to the fallback, and the result
  is never the zero tag. ADR 0051's posture for a header a stranger wrote.
- **The plural rules follow the MESSAGE.** A `Printer`'s chain is a list of
  `(tag, rules)` pairs, and `RenderCount` uses the rules of the step that
  ANSWERED — see ADR 0063 §D3 and
  `TestThePluralRulesFollowTheMessageAndNotTheRequest`.
- **A counted entry is checked against its language at load.**
  `TRANSLATION_INCOMPLETE` names the tag, the key and the first missing
  category. The FORMS shape in a catalogue file is the declaration that a
  message is counted; a plain string is not counted and is never checked.
- **Load order is deterministic.** Languages, keys and category names are all
  walked sorted, so a catalogue with two defects always reports the same one
  first and a failing build can be bisected.
- **`LoadFS` invents no format and no filesystem.** Bytes go through
  `codec.Lookup` (blank-import `pkg/v1/codec`); the tree is `io/fs.FS`, which
  is `vfs.FS` unchanged (ADR 0056). This package imports neither `vfs` nor
  `os`.
- **The language comes from the file NAME**, through `ParseTag` — so
  `fr_FR.json` is refused, subdirectories are ignored rather than walked, and
  two files resolving to one tag are `CATALOG_INVALID`.
- **`failLoad` keeps the filesystem cause IN THE CHAIN**, so
  `errors.Is(err, fs.ErrNotExist)` still answers. Same shape and same reason as
  `service/vfs.failRead`.
- **A `Store` never mutates after construction.** No Add, no Set, no Reload.
- **`ja` and `zh` carry no rule.** `PluralValue.Select` answers `FormOther`
  when the rule is nil, and `Valid` reads the category SET rather than the
  rule, so a language with no distinction is a valid entry and only the zero
  value is not.

## Do NOT

- **Add a language without its boundary tests and the `supportedCount` bump.**
  That constant exists so the addition is deliberate.
- **Make an unsupported language fall back to English's rules.** It is the one
  failure this whole domain is built to prevent.
- **Move the completeness check to render time.** In production, in a language
  the on-call engineer does not read, the only repair is to show something
  wrong.
- **Give `Negotiate` an error return**, or make it answer 406.
- **Implement RFC 4647 §3.3.2 extended filtering**, or make §3.4 lookup extend
  a range instead of truncating it.
- **Construct a `PluralValue` outside the table.** A caller could mint a
  language whose declared categories and whose rule disagree — the one
  inconsistency the load-time check assumes away.
- **Add a reload, a watcher or a mutable catalogue.** Swapping a second `Store`
  in is the caller's move and needs no lock on the render path.
- **Bind the `Printer` to the concrete `*Store`** to save a map lookup. It
  holds the PORT on purpose (ADR 0039), and BENCH.md prices the difference.

## Benchmarks

`BENCH.md` — regenerate with:

```
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./i18n/
```

Highlights: a literal render is 105 ns and **zero** allocations; a render with
a substitution is 184 ns and **one** (the returned string); plural selection is
3–4 ns and never allocates, in every language including Arabic's six
categories; and negotiation allocates **nothing** for any header, after a
profile showed `strings.Split` was 31.6 % of the allocated objects on that path.

## Verification

```
bazel test --config=race //internal/service/i18n:i18n_test
# OR
cd internal/service && GOWORK=off go test -race ./i18n
```
