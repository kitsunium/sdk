# ADR 0063 — message-translation domain (`i18n`): a named CLDR subset, everything outside it refused BY NAME, and the guarantees that are NOT made

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by SIBLING, never by widening), [ADR 0041](0041-sdk-scheduler-domain.md) (the "refused by name" discipline, applied to a cron subset), [ADR 0042](0042-sdk-token-domain.md) (`v4.local` refused by name), [ADR 0046](0046-sdk-validation-domain.md) (a message names the rule and the bound, never the VALUE), [ADR 0051](0051-sdk-trace-domain.md) (a malformed header a stranger wrote is never an error), [ADR 0056](0056-sdk-vfs-domain.md) (`io/fs` unchanged), [ADR 0058](0058-sdk-view-domain.md) (contextual escaping belongs to `view`), [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (the `x/sys` filter, and the discipline of correcting a stated mechanism)
- **Corrects**: the reason previously given in review for building this domain rather than adopting `golang.org/x/text`. See §Context.2 — the doctrinal argument stands and the dependency argument, as it was stated, does not.

## Context

### 1. What was refused, and why it is being built anyway

An adverse review refused this ticket on the grounds that a hand-written i18n
engine is not worth building when `golang.org/x/text` exists. The maintainer's
doctrine reverses that verdict, and the reversal is not a preference:

> The SDK provides every tool, at the lowest level it can, with total control
> end to end and as few dependencies as possible — except for a CONNECTOR to a
> third-party system, which must be completely isolated.

A translation catalogue is not a third-party system. There is no service to
connect to, no protocol to speak and no vendor to isolate: there is a lookup
table, a set of arithmetic rules published by Unicode, and a string
substitution. It is a MECHANISM, and a mechanism the SDK can implement from its
specification is exactly what ADR 0044 (the OpenTelemetry metrics data model),
ADR 0047 (RFC 6455), ADR 0048 (OTLP/JSON) and ADR 0051 (W3C Trace Context)
already did. So it is built.

### 2. The `x/sys` argument was checked, and it is FALSE for `x/text`

The ticket also carried a hard technical reason: that `golang.org/x/text` pulls
in `golang.org/x/sys`, which is banned SDK-wide (ADR 0016 §Dependency
discipline, restated in ADR 0018 and ADR 0029), and that it would therefore be
filtered exactly as the HCL codec was in ADR 0022/0034.

**Measured, it does not.** Go 1.27.1, `GOWORK=off`, a copy of
`internal/service` with a package importing `golang.org/x/text/language`,
`golang.org/x/text/message` and `golang.org/x/text/feature/plural`, then
`go mod tidy`:

| Measurement | Before | After adding `x/text` v0.41.0 |
|---|---|---|
| `golang.org/x/sys` in `go list -m all` | absent | **still absent** |
| `golang.org/x/sys` in `go.sum` | absent | **still absent** |
| `go.sum` module set | 14 modules | 15 — exactly `golang.org/x/text` |
| build list gains | — | `x/text`, `x/tools`, `x/mod`, `x/sync` (module requirements only) |
| non-stdlib packages compiled by `go list -deps` | — | `golang.org/x/text/**` only |

`x/text`'s `go.mod` does require `golang.org/x/tools` — the module that
introduced `x/sys` in the HCL case — but it is marked `// tagx:ignore` and is
used by `x/text`'s own code generators. Under Go's pruned module graph nothing
from it is loaded, so it never reaches `go.sum` and never reaches a build.

This ADR states that out loud rather than repeating the claim, for the reason
ADR 0034 exists: a placement rule quoted from a mechanism that does not happen
is a rule nobody can re-derive, and the next person to check it will conclude
the conclusion was wrong too. **The decision is unchanged; the mechanism is
corrected.** What actually argues against the dependency is smaller and true:

| Measured | Value |
|---|---|
| `x/text` v0.41.0 source tree | **30 MB**, 280 non-test `.go` files |
| generated CLDR tables on the language + plural path alone | **211 KB** (`internal/language/tables.go` 157 KB, `internal/language/compact/tables.go` 32 KB, `feature/plural/tables.go` 23 KB) |
| linked binary, `fmt` only | 2 346 705 B |
| linked binary, + `x/text/message` + `feature/plural` | 2 944 437 B |
| delta | **+597 732 B, +25.5 %** |

That is the honest trade: half a megabyte of binary and 211 KB of generated
tables nobody on the team has read, to answer a question about roughly 200
locales when the product has strings in a dozen. The doctrine decides it; the
numbers describe it.

### 3. The failure this domain exists to prevent

Polish has four cardinal plural categories, Russian four, Arabic six. English
has two. An i18n library that resolves an unknown language by falling back to
English's rules renders a grammatically wrong sentence on every page of the
Polish site — and nothing observes it. The page renders. The tests pass.
Nothing is logged. The only people who can see it have no route to report it
that reaches the right file.

Everything below follows from refusing that outcome.

## Decision

A 26th core sibling, `internal/core/i18n` (block `0.2.30.*`), its
implementation in `internal/service/i18n` (block `0.3.60.*`), and the façade in
`pkg/v1/i18n`. **No registry** — the precedent of `proc` (ADR 0016),
`resilience` (0026), `net` (0029), `scheduler` (0041), `token` (0042),
`session` (0045), `lock` (0052) and `events` (0053).

### D1 — How far CLDR goes: thirteen entries, and every other language REFUSED BY NAME

The supported set is a hand-written table of **thirteen** entries, each
transcribed from the CLDR cardinal-plural chart with its clause quoted in the
comment above it:

| Entry | Categories | Why it is in the set |
|---|---|---|
| `ja`, `zh` | `other` | no plural distinction at all |
| `de`, `en`, `nl` | `one`, `other` | the Germanic shape (`i = 1 and v = 0`) |
| `es`, `fr`, `it`, `pt` | `one`, `many`, `other` | the Romance shape, whose `many` is the compact-decimal category |
| `pl`, `ru` | `one`, `few`, `many`, `other` | the Slavic shape — the one an English fallback destroys |
| `ar` | all six | the only entry that uses `zero` and `two` |
| `pt-PT` | `one`, `many`, `other` | CLDR gives European Portuguese a DIFFERENT singular from Brazilian |

Between them the set exercises every one of the six CLDR categories and every
rule shape, which is asserted rather than claimed
(`TestSupportedTagsIsTheDocumentedSet`).

**Every other language is refused at construction**, by name, with the
supported set beside it — `UNSUPPORTED_LANGUAGE` (`0.3.60.1`). Czech,
Lithuanian, Latvian, Irish, Welsh, Slovenian, Romanian, Croatian, Slovak and
Ukrainian each have categories English does not, and each is pinned as refused
by `TestAnUnsupportedLanguageIsRefusedRatherThanApproximated`. Adding one is a
rule, its CLDR citation and its boundary values in the same change — and the
`supportedCount` constant in the test file has to move with it, so the addition
cannot be silent.

The vendored alternative was considered and refused for the reason the whole
domain exists: a table generated from CLDR is hundreds of kilobytes nobody
reviews, and the review is the point. Thirteen rules with their clauses beside
them can be checked against the chart by one person in an afternoon.

**One operand is not implemented, and it is named.** CLDR's plural operands are
`n`, `i`, `v`, `w`, `f`, `t` and `c`/`e`. This domain implements `n`, `i`, `v`
and `f`. `w` and `t` are refused because no rule in the supported set reads
them. `c`/`e` — the compact-decimal exponent — are refused for a harder reason:
they exist to describe "1M" and "1,2 mln", and this domain ships no
compact-decimal formatter, so the exponent can only ever be 0 here. The four
Romance `many` clauses, which CLDR writes as
`e = 0 and i != 0 and i % 1000000 = 0 and v = 0 or e != 0..5`, are therefore
implemented at `e = 0`: the first half exactly, the second half never firing.
That is stated at all four call sites rather than once, because a reader
checking one rule against CLDR must not have to find this paragraph to know it
is right.

### D2 — Language negotiation: RFC 4647 §3.4 for selection, §3.3.1 for refusal, §3.3.2 refused by name

`Negotiate(header string) TagValue` implements **RFC 4647 §3.4 Lookup** over an
**RFC 9110 §12.5.4** `Accept-Language` header. Lookup, not filtering, because a
renderer needs exactly one language and a filtered list would leave the choice
somewhere else.

Implemented, refused and decided:

- **§3.4 Lookup decides which tag is SELECTED.** The range is truncated one
  subtag at a time and the longest supported tag equal to a truncation wins.
  This is the algorithm exactly, **including its consequence**: a range of `en`
  does NOT select a supported `en-GB`, because Lookup truncates the range and
  never lengthens it. That is documented and tested
  (`TestLookupTruncatesTheRangeAndNeverExtendsIt`) rather than papered over
  with a "most likely subtag" heuristic, which would be a second matching
  algorithm the RFC does not define. The remedy is to name the catalogue file
  after the base language.
- **§3.3.1 Basic Filtering decides which tags a `q=0` element REFUSES.** RFC
  9110 §12.4.2 says `q=0` means "not acceptable"; `en;q=0` therefore refuses
  `en`, `en-GB` and `en-Latn-GB`, which is what a client saying "not English"
  means. The two RFC 4647 mechanisms coexist in one function, on opposite sides
  of the match, on purpose.
- **§3.3.2 Extended Filtering is refused by name.** Its wildcards sit INSIDE a
  range (`de-*-DE`), no such range can match anything here, and implementing it
  would mean a second matcher with its own grammar for a syntax no client
  sends.
- **The wildcard range `*` is SKIPPED during selection**, which is what §3.4
  requires. `*;q=0` — "nothing but what I listed" — therefore changes nothing:
  this function always returns a tag, because a page has to render, and RFC
  9110 §12.5.4 itself discourages answering 406 to an `Accept-Language` a
  server cannot satisfy.

**A malformed tag is NOT an error.** `Negotiate` has no error return at all.
An unparsable element is skipped, an unparsable `q` takes its element with it,
an unknown parameter takes its element with it, an empty header resolves to the
fallback, and a header of pure garbage resolves to the fallback — sixteen named
cases in `TestAMalformedHeaderIsNeverAnError`. This is the call ADR 0051 made
for a malformed `traceparent`, for the same reason: the header is written by a
stranger, RFC 4647 §3.4 prescribes exactly one response to a range it cannot
use, and returning an error invites a caller to fail a request over a peculiar
browser setting.

Everything that CAN be wrong is refused when the `Negotiator` is BUILT: an
empty supported set is `NEGOTIATION_EMPTY` (ADR 0031's refuse half — an empty
set is an unfinished wiring, not "accept anything"), an unset fallback is
`CATALOG_INVALID`, and a fallback outside the supported set is `CATALOG_INVALID`
because otherwise the common path returns a language the caller declared it
does not serve.

The header parse is **bounded at 32 elements** and the tail is ignored rather
than treated as an error: a client-controlled header can be arbitrarily long,
and parsing all of it lets a client spend the server's time on its own behalf.

### D3 — The plural rules follow the MESSAGE, not the request

This is the decision the domain exists to get right, and it is the one an
implementation gets wrong by default.

A request asks for Polish. The key was never translated into Polish. The
renderer walks its chain and serves the English message. **The plural form must
now be selected with ENGLISH rules**, because the English message carries `one`
and `other` and nothing else. Selecting with Polish rules asks that message for
`few` and gets either a crash, a blank, or — worst — the `other` form silently
standing in for `few`, which is a wrong sentence in a language nobody on the
team reads.

So a `Printer` holds a CHAIN of `(tag, rules)` pairs rather than a chain of
tags plus one rule, and `RenderCount` uses the rules of the step that ANSWERED.
`TestThePluralRulesFollowTheMessageAndNotTheRequest` is the executable form of
this paragraph.

The port makes the mistake hard to reintroduce: `Catalog.Lookup` is EXACT and
performs no fallback and no plural selection, so the walk exists in exactly one
place and that place necessarily knows which language answered.

### D4 — A missing key renders the KEY, and returns an error

`Render` returns `(string, error)`. On a miss the string is the key itself —
`"checkout.button.pay"` — and the error is `MESSAGE_NOT_FOUND` (`0.2.30.5`).

The alternatives were weighed against what a person actually does with them:

| Behaviour | What ships | Who notices |
|---|---|---|
| empty string | a blank space in a layout that still looks finished | nobody |
| the key | `checkout.button.pay` on screen, greppable | everybody, within the hour |
| error only, empty string | a page that fails to render, or a caller that ignores it and ships a blank | see row 1 |

So both: a caller that checks the error fails the render, a caller that ignores
it ships something visibly wrong. ADR 0031 is satisfied by the REFUSAL half —
there is no inert policy here, and the zero `TagValue` is refused at every
constructor rather than read as "use English".

The key is a developer identifier, never user data, so putting it on screen
discloses nothing an attacker could not read in the client bundle.

**The fallback is a construction-time decision, not a render-time guess.**
`NewStore` refuses a zero fallback tag and refuses a fallback the catalogues do
not hold — the second because a fallback resolving to nothing turns every miss
into a returned key, and the wiring fault then looks exactly like a missing
translation.

**A counted message incomplete for its language is refused at LOAD**, naming
the tag, the key and the missing category — `TRANSLATION_INCOMPLETE`
(`0.3.60.4`). It has its own code because it is the one catalogue defect a
reviewer cannot see by reading the file: a Polish entry with `one` and `other`
looks finished, and is incomplete only against a rule table stored elsewhere.
Deferring it to render time means discovering it on the request that first
needed `few`, in production, in a language the on-call engineer does not read,
where the only available repair is to show something wrong.

### D5 — Interpolation: named placeholders, one pass, and no escaping

The syntax is literal text with named placeholders — `Welcome back, {name}` —
and a literal brace is `{{` or `}}`. That is the entire grammar. Refused BY
NAME, each because the alternative is a language:

- **positional placeholders** (`{0}`, `%s`, `%1$s`) — a translator reorders a
  sentence, which is most of the job, and a positional argument that moves
  changes meaning silently;
- **format specifiers** (`{count:03d}`, `%.2f`) — this domain does not format
  numbers, and a specifier would announce that it does;
- **ICU MessageFormat's nested `plural`/`select` constructs** — plural
  selection here is the `Form` the renderer picks from the language's own CLDR
  rules, so a message body never encodes a rule; and ICU MessageFormat is a
  parser living inside a translation file, which is a place nobody reviews a
  parser;
- **filter calls** (`{name|upper}`) — case mapping is language dependent,
  Turkish dotless i being the standing example, and this domain ships no case
  mapper;
- comments, whitespace control, conditionals.

A pattern is compiled ONCE, when the catalogue is built, so a malformed
translation fails at startup and the render path has nothing to fail at.

**Where the injection surface is, and where it is not.** A message PATTERN is
trusted input: it comes from a catalogue file the developer ships. An `Args`
VALUE is not: it is a username, a filename, a count. Substitution is literal
and single-pass, so a value containing `{admin_token}` produces those thirteen
characters and is never rescanned — proved by
`TestMessageFormatNeverRescansASubstitutedValue`, which passes both a
`{admin_token}` display name and a real `admin_token` argument and asserts the
second never appears in the output. If a pattern were assembled from user
input, that user could name a placeholder the caller happens to pass and read
its value; the domain cannot detect that and does not try. See §"What is NOT
guaranteed".

**No error in this domain ever names an argument value** — not in `Error()`,
not in `Public`, not in `Private`, not in a field. Errors name the key, the
placeholder, the CLDR category and the tag, which are the developer's own
identifiers. This is `validation`'s rule (ADR 0046), `authz`'s (ADR 0057) and
`view`'s (ADR 0058), and it has the same shape of guard here:
`TestNoErrorEverNamesAnArgumentValue` drives every error path with a
recognisable secret and asserts it appears nowhere, and
`TestRenderNeverDisclosesAnArgumentValue` repeats it one layer up where the
service adds tag/key/chain fields.

**Nothing here escapes anything.** A rendered message is a `string`, never a
trusted-HTML type. Placing it in a page goes through `view` (ADR 0058), whose
contextual escaping knows where in the document the cursor is; escaping here as
well would double-escape every apostrophe in every French sentence in the
catalogue. The SDK never mints a `view.TrustedHTML` from an i18n render.

`Args` is `map[string]string` and deliberately not `map[string]any`. A caller
who wants `1 234,50 €` formats it, and discovers at the call site that this SDK
does not ship a locale-aware number formatter. An `any` would apply Go's
default formatting instead and print `1234.5` to a French reader — the same bug
with the discovery removed.

### D6 — The catalogue format is not invented: `codec` decodes it, `io/fs` reads it

`LoadFS(fsys fs.FS, dir string, format codec.Format, fallback TagValue)`.

- The bytes are decoded by the **codec domain** (ADR 0003) through
  `codec.Lookup(format)`, so a catalogue is JSON, YAML, TOML, CBOR, MessagePack
  or any other registered format and this package contains **no parser**. The
  codec must be registered — blank-import `pkg/v1/codec` — exactly as
  `config.FileSource` requires; an unregistered format is `CATALOG_LOAD_FAILED`
  reported BEFORE the directory is read, because it is a wiring fault and not a
  filesystem one.
- The filesystem is **`io/fs.FS`**, which is `vfs.FS` unchanged (ADR 0056), so
  an `embed.FS`, an `os.DirFS`, a `vfs.NewOS` root and a `vfs.NewMem` are the
  same call — and this package imports neither `vfs` nor `os`.
- The **language comes from the file name**: `en.json`, `pt-PT.yaml`. The whole
  `ParseTag` refusal list therefore applies to filenames, including the POSIX
  spelling: `fr_FR.json` is refused, because accepting a second separator would
  mint a second tag for French and split the catalogue in half. Two files that
  canonicalise to one tag are `CATALOG_INVALID` — one would win, the choice
  would depend on directory order, and half the strings would vanish.
  Subdirectories are ignored rather than walked, because a nested layout is a
  convention this package does not own.

The decode target is `map[string]any` and not a typed struct, for a mechanical
reason rather than a stylistic one: a catalogue entry is legitimately one of
two SHAPES — a pattern string, or an object of CLDR forms — and no single Go
field holds both. Every coercion beyond those two is refused by shape: a YAML
`1.0` would become `"1"` and a `no` would become `"false"`, and both would
render as a translation the file does not contain.

### D7 — The port is frozen at two methods; capability arrives as a SIBLING

`Catalog` has `Lookup` and `Tags`, and `TestCatalogIsFrozenAtTwoMethods` is a
package-level two-method double that stops compiling if a third is added.
`pkg/v1/i18n` aliases the port and Go interfaces are structural, so a third
method breaks every downstream double at compile time with no deprecation
window (ADR 0039).

Two siblings, discovered by type assertion:

- **`KeyLister`** (`Keys(tag) []Key`) exists for a TEST rather than a request.
  `Store.Missing(tag)` compares a language's key set against the fallback's, so
  "this string was never translated" becomes a build that does not go out —
  which is the only place the SDK can help, because at render time the string
  is already missing and something has to be shown.
- **`Fallbacker`** (`Fallback() TagValue`) names the language that stands in.
  It is a sibling rather than a method because a catalogue with no fallback is a
  legitimate thing — a single-language bundle, a test double — and the ABSENCE
  of the method is how the renderer finds out, rather than a zero tag it would
  have to interpret. That is ADR 0052's lesson (`Deadliner`) applied here.

`PluralRule` is a FUNC port, which satisfies ADR 0039 structurally: a func type
cannot grow a method at all.

### D8 — Measured, and optimised only where a profile said so

The render path is on every page of a translated application, so it is measured
rather than asserted — `internal/service/i18n/BENCH.md`, from real numbers.
Two results are worth recording here:

1. **`Negotiate` allocates nothing.** It did allocate: `strings.Split` was
   31.6 % of the allocated objects and 12.5 % of the CPU on that path
   (`go tool pprof`, `strings.genSplit`), building a `[]string` the loop read
   once and dropped. Replacing it with a `strings.Cut` walk took a single-range
   header from 249 ns/1 alloc to ~100 ns/**0 allocs** and a real browser header
   from 856 ns/1 alloc to ~500 ns/**0 allocs**. The profile came first.
2. **A render allocates exactly one object — the returned string — and that is
   left alone and written down.** The rest of the render path is two map
   lookups (`byTag`, then the key) plus the `Args` lookup, which `pprof` shows
   as 17 % `memHashAES` and 26 % cumulative `mapaccess2`. Flattening them would
   mean a composite key (which hashes the same two things) or binding the
   `Printer` to the concrete `*Store` instead of the port — and the port is
   what `pkg/v1` publishes. It is not corrected, and BENCH.md says so rather
   than leaving a reader to discover the map traffic in a profile of their own.

## Consequences

- A language outside the thirteen **does not start the program**. That is the
  intended cost. It is a table entry, a CLDR citation and a boundary test away.
- A counted message missing one of its language's categories **does not start
  the program**, naming the file, the key and the category.
- `Negotiate` never fails and never returns the zero tag, so a request path
  needs no error branch for a header.
- A missing key is visible on screen and reported as an error; it is never
  blank.
- No new module dependency: `git diff -- '*/go.mod' go.mod` is empty.
- One `.ktn-linter.yaml` addition per rule whose suggestion would damage the
  design, each scoped to the i18n trees and each carrying its rationale beside
  the existing precedent it follows (`KTN-VAR-STRMAP` after `core/sql`,
  `KTN-INTERFACE-ANYUSE` after `codec`/`view`, `KTN-API-MINIF` after the #385
  block, `KTN-STRUCT-CTOR` after `jwk`/`metrics`, and one
  `adapter_role_interfaces` entry for `KeyLister` after `EntryFetcher`).

## What is NOT guaranteed

Stated as loudly as what is, in the shape ADR 0052 uses.

1. **Nothing about numbers, dates, times, currencies, units, collation, case
   mapping, normalisation, transliteration or text direction.** `Args` is
   `map[string]string`; the caller formats. A product that needs
   locale-correct number formatting needs something this SDK does not ship, and
   the type says so at every call site rather than in a release note.
2. **A render does not report which language actually answered.** When a key is
   absent from the requested language and present in the fallback, the render
   succeeds with the fallback's text and no signal — so a `Content-Language`
   header or an `html lang` attribute taken from `Printer.Tag()` can be wrong
   for that particular string. A per-call signal would be checked by nobody and
   would put a branch on the hot path; the same question is answered
   exhaustively and at build time by `Store.Missing`. A caller who needs the
   header to be exact asserts there that the gap is empty.
3. **A message pattern is TRUSTED input.** Patterns come from catalogue files
   the developer ships. A pattern assembled from data an end user controls lets
   that user name a placeholder the caller happens to pass and read its value.
   The domain cannot detect it and does not try; the guarantee is one-sided,
   and it is the value side.
4. **A rendered message is not escaped for any output format.** It is a
   `string`. HTML goes through `view`; SQL, shell and JSON go wherever they
   already go. The SDK never produces a trusted-HTML type from a render.
5. **RFC 4647 §3.4 does not extend a range.** A supported catalogue of only
   `en-GB` is not selected by a request for `en`. This is the algorithm, not a
   defect, and the remedy is a catalogue named after the base language.
6. **`pt-PT` is the only region-specific plural rule implemented.** CLDR
   defines a handful; the others resolve to their base language, which is
   correct for every one of them at the time of writing and is a claim that
   must be rechecked when the table grows.
7. **The rule table is a snapshot of CLDR**, transcribed by hand. Unicode
   revises plural rules between releases; a change upstream is not detected
   here, and the boundary-value tests are what a maintainer re-runs against a
   newer chart.
8. **A `Store` never reloads.** There is no Add, no Set and no Reload: a
   catalogue that changes under a request is a page whose two paragraphs came
   from two versions of the text. Reloading is a process restart, or a second
   `Store` the caller swaps in.

## Why not …

- **… `golang.org/x/text`?** See §Context.1 and §Context.2. Doctrine decides
  it; the measured cost is +25.5 % binary and 211 KB of unreviewed generated
  tables; the `x/sys` argument is false and is corrected here rather than
  repeated.
- **… vendor the full CLDR plural table?** Hundreds of kilobytes of generated
  data nobody reads, to answer for languages the product has no strings in.
  Thirteen hand-written rules with their clauses beside them can be checked
  against the chart; a generated table can only be trusted.
- **… fall back to English's rules for an unsupported language?** That is the
  failure in §Context.3. It renders, it passes, and it is wrong.
- **… ICU MessageFormat?** A parser inside a translation file. Plural selection
  here comes from the language's rules, so a message body never has to encode
  one.
- **… a registry of catalogues?** The set of implementations is closed and
  composition is by value, not by name — and a registry keyed on a
  configuration string lets a typo swap a language silently, which in this
  domain means silently swapping what a page says.
- **… `text/template` or the `view` engine for interpolation?** A translation
  is not a template: it is a sentence with holes, written by someone who is not
  a programmer, in a file that is not reviewed as code. The grammar is narrow
  on purpose.

## Deferred

Each by name, so a later ADR extends rather than rediscovers:

- **Ordinal plural rules** (`1st`, `2nd`, `3rd`). CLDR defines a second rule
  set; nothing in the domain assumes its absence, and `Form` is already the
  right type.
- **Gender and `select`-style branching.** It is a real need in several
  languages and it is not plural selection; it deserves its own decision about
  where the branch lives.
- **Compact decimals**, and with them the `c`/`e` operands and the second half
  of the four Romance `many` clauses.
- **Number, date and currency formatting.** The moment any of these ships,
  `Args` becomes the wrong type and this ADR is the place to say so.
- **More languages.** The addition procedure is fixed by D1 and is the point of
  the `supportedCount` constant.

## References

- Unicode CLDR — Language Plural Rules, cardinal chart (the source of the
  thirteen transcribed rules)
- RFC 4647 — Matching of Language Tags (§3.3.1 basic filtering, §3.3.2 extended
  filtering, §3.4 lookup)
- RFC 5646 / BCP 47 — Tags for Identifying Languages
- RFC 9110 §12.4.2 (qvalue), §12.5.4 (`Accept-Language`)
- `internal/core/i18n/CLAUDE.md`, `internal/service/i18n/CLAUDE.md`,
  `pkg/v1/i18n/CLAUDE.md`
- `internal/service/i18n/BENCH.md`
