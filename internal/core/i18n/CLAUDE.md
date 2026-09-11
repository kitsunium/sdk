# internal/core/i18n/

## Purpose

Declares the **message-translation port**: `Catalog` (frozen at two methods),
the `TagValue` that names a language, the `MessageValue` a translator wrote,
the CLDR plural `Form` a quantity falls in, the `CountValue` that decides
which, and the `Args` that fill a message's holes. The 27th core sibling,
admitted by **ADR 0063**. The CLDR rule table, the concrete catalogue, the
Accept-Language negotiation and the renderer live in `internal/service/i18n`.

Code range: `0.2.30.*` (ADR 0063).

## Contents

| File | Surface |
|---|---|
| `i18n.go` | package doc · `PluralRule func(CountValue) Form` · `Args map[string]string` |
| `i18n_interface.go` | `Catalog` (frozen) + the ADR 0039 siblings `KeyLister` and `Fallbacker` |
| `tag_value.go` | `TagValue` + `ParseTag` + `String` / `IsZero` / `Language` / `Script` / `Region` / `Parent` |
| `count_value.go` | `CountValue` + `Int` / `Decimal` + `IntegerPart` / `FractionValue` / `VisibleFractionDigits` / `IsIntegerValued` |
| `form.go` | `Form` + the six categories + `ParseForm` / `String` / `Valid` |
| `message_value.go` | `Key` + `ValidateKey` + `MessageValue` + `NewMessage` / `NewPluralMessage` / `Format` / `HasForm` / `IsPlural` |
| `form_pattern.go` | `formPattern` — one category paired with its compiled body |
| `pattern.go` | `pattern` + `compilePattern` + `literal` / `expand` + the placeholder grammar |
| `pattern_compiler.go` | `patternCompiler` — the one-pass parser run at catalogue load |
| `part.go` | `part` — one span of a compiled pattern |
| `codes.go` | `Code*` constants — range 0.2.30.* |
| `errors.go` | `InvalidTag` / `InvalidKey` / `InvalidPattern` / `ArgumentMissing` / `MessageNotFound` / `PluralFormMissing` / `InvalidCount` / `InvalidForm` (`errs.Define`) |

## The frontier

The SDK ships the **mechanism**. Everything below the line is the
application's, because it needs to know what the product says.

| The SDK ships | The application ships |
|---|---|
| `Catalog` / `TagValue` / `MessageValue` / `Form` / `CountValue` | the catalogue files, and the sentences in them |
| CLDR plural selection for thirteen named languages | which languages the product is sold in |
| RFC 4647 lookup over `Accept-Language` | how the chosen language reaches the request (cookie, path prefix, header) |
| A missing key rendered as the key, plus an error | what a page does about it |
| `Store.Missing` as a build-time gate | the test that calls it |
| Named placeholders, substituted literally | the HTML escaping (`view`, ADR 0058) and the number formatting (nothing) |

## Conventions

- **`Catalog` is FROZEN at two methods.** `pkg/v1/i18n` aliases it and Go
  interfaces are structural, so a third method breaks every downstream
  two-method double at compile time with no deprecation window (ADR 0039).
  `TestCatalogIsFrozenAtTwoMethods` is a package-level double in
  `internal/service/i18n` and is the executable guard.
- **`Lookup` is EXACT.** No fallback, no parent truncation, no plural
  selection. All three are policy, and a Catalog that decided them would decide
  them invisibly for every caller. The walk lives in exactly one place —
  `service/i18n`'s renderer — which therefore knows which language answered and
  can pick THAT language's rules (ADR 0063 §D3).
- **`FormOther` is the zero `Form`.** ADR 0031 on a func port with no
  constructor to refuse in: `other` is the one category CLDR guarantees in
  every language, so a rule that forgets to set a result names the form every
  message must carry.
- **The zero `TagValue` is REFUSED, never defaulted.** No constructor in this
  domain reads an unset tag as "use English".
- **`Args` is `map[string]string`, never `map[string]any`.** The values are
  substituted verbatim; a caller who wants a locale-formatted number formats
  it, and learns at the call site that this SDK does not ship a formatter.
- **A pattern is compiled ONCE**, at catalogue load. A render never parses,
  which is why a malformed translation fails at startup and the hot path has
  nothing to fail at.
- **Substitution is single-pass and literal.** A value containing `{other}`
  produces those seven characters. `TestMessageFormatNeverRescansASubstitutedValue`
  is the guard, and it is a security property: patterns are trusted, values are
  not.
- **No error ever names an argument VALUE** — not in `Error()`, not in
  `Public`, not in `Private`, not in a field. Errors name the key, the
  placeholder, the category and the tag. `TestNoErrorEverNamesAnArgumentValue`
  is the guard. Same rule as `validation` (ADR 0046), `authz` (ADR 0057) and
  `view` (ADR 0058).
- **A surplus `Args` entry is ignored; a missing one is an error.** One map is
  handed to every language in turn and languages legitimately use different
  subsets, so refusing a surplus would make the English render fail for a
  reason living in the French catalogue. A missing one is a hole in the
  sentence being rendered right now.
- **`Format` refuses a category the message does not carry.** It never falls
  back to `other`, because that renders a grammatically wrong sentence nobody
  on the team can read.
- **The zero `MessageValue` is not an empty translation.** `pattern.set`
  distinguishes a compiled empty pattern from one that was never compiled, so
  `var m MessageValue` reports `PLURAL_FORM_MISSING` rather than rendering `""`.

## Do NOT

- **Add a method to `Catalog`.** A capability arrives as a SIBLING —
  `KeyLister` and `Fallbacker` are the two that exist (ADR 0039).
- **Make `Lookup` fall back**, truncate a tag, or select a plural form.
- **Widen `Args` to `any`**, or add a format specifier, a positional
  placeholder, a filter call or an ICU `plural`/`select` construct to the
  pattern grammar. Each is refused BY NAME in ADR 0063 §D5.
- **Escape anything here.** A rendered message is a `string`; HTML escaping is
  `view`'s and is contextual.
- **Put a plural rule, a language table or a negotiation policy in this
  package.** They are facts about the world, not contracts, and they live in
  `internal/service/i18n`.
- **Give `TagValue`, `CountValue` or `MessageValue` a from-parts constructor.**
  Every one of them comes out of a call that VALIDATES — `ParseTag`, `Int` /
  `Decimal`, `NewMessage` / `NewPluralMessage` — and a second way in would skip
  it.
- **Read a zero `TagValue` as a default language**, anywhere.

## Verification

```
bazel test --config=race //internal/core/i18n:i18n_test
# OR
cd internal/core && GOWORK=off go test -race ./i18n
```
