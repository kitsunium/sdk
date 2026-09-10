# pkg/v1/i18n/

## Purpose

The public facade for the SDK's message-translation domain (**ADR 0063**): a
catalogue of translated messages, CLDR plural forms that are right in Polish
and Arabic and not only in English, and `Accept-Language` negotiation that
never fails a request.

`README.md` is **generated** from the package doc comment in `i18n.go` by
`gomarkdoc` (rule 10 / ADR 0008). Edit the doc comment, then run
`make docs-readme`. Never hand-edit `README.md`.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Tag` | alias → `corei18n.TagValue` | comparable, canonical, map-key-able |
| `Key` | alias → `corei18n.Key` | a developer identifier; shown on screen when a translation is missing |
| `Message` | alias → `corei18n.MessageValue` | compiled at load, never at render |
| `Form` | alias → `corei18n.Form` | the six CLDR categories; `FormOther` is the zero |
| `Count` | alias → `corei18n.CountValue` | CLDR operands; carries the DISPLAY precision |
| `Args` | alias → `corei18n.Args` | `map[string]string` — see §Why not `any` |
| `Catalog` | alias → `corei18n.Catalog` | frozen at two methods (ADR 0039) |
| `KeyLister` / `Fallbacker` | aliases | the two ADR 0039 siblings, reached by type assertion |
| `PluralRule` | alias | a FUNC port |
| `Entry` / `Catalogue` | aliases → `svci18n.EntryValue` / `Catalogue` | the shape a catalogue FILE has |
| `Store` / `Printer` / `Negotiator` / `Plural` | aliases → the service types | |
| `FormOther` … `FormMany` | consts | one per const, explicitly typed — the VALUES are core's |
| `ParseTag` / `ParseForm` / `Int` / `Decimal` / `ValidateKey` | funcs | value constructors |
| `NewMessage` / `NewPluralMessage` / `Plain` / `PluralForms` | funcs | message and entry constructors |
| `NewStore` / `LoadFS` / `NewPrinter` / `NewNegotiator` | funcs | the four construction points, and where every refusal happens |
| `Rules` / `SupportedTags` | funcs | the CLDR table, at runtime |
| 12 `errs.Define` sentinels | vars | 8 from core, 4 from service |

## How a consumer wires it

```go
english, _ := i18n.ParseTag("en")

store, err := i18n.LoadFS(os.DirFS("locales"), ".", "json", english)   // blank-import pkg/v1/codec
negotiator, err := i18n.NewNegotiator(store.Tags(), english)

// one Printer per language, at startup — NOT per request
printers := map[i18n.Tag]*i18n.Printer{}
for _, tag := range store.Tags() {
    printers[tag], err = i18n.NewPrinter(store, tag)
}

// per request
p := printers[negotiator.Negotiate(r.Header.Get("Accept-Language"))]
text, err := p.RenderCount("cart.items", i18n.Int(n), i18n.Args{"n": strconv.Itoa(n)})
```

And, in the consumer's own test suite — this is the point of `KeyLister`:

```go
for _, tag := range store.Tags() {
    if gaps := store.Missing(tag); len(gaps) != 0 {
        t.Errorf("%s is missing %d keys: %v", tag, len(gaps), gaps)
    }
}
```

## Why not `any`

`Args` is `map[string]string`. A caller who wants `1 234,50 €` formats it — and
finds out at the call site that this SDK ships no locale-aware number
formatter. An `any` would apply Go's default formatting instead and print
`1234.5` to a French reader: the same bug with the discovery removed.

Number, date, time, currency and unit formatting, collation, case mapping,
normalisation and transliteration are each refused BY NAME in ADR 0063. The day
any of them ships, `Args` becomes the wrong type and the ADR is where that is
said.

## Do NOT

- **Put a rendered message into HTML without `pkg/v1/view`.** A render returns
  a plain `string`, never a trusted-HTML type, and escaping is contextual
  (ADR 0058).
- **Build a message pattern from user input.** Patterns are trusted; `Args`
  values are not. A pattern an end user controls lets them name a placeholder
  the caller passes and read its value — the one thing this domain cannot
  detect.
- **Build a `Printer` per request.** It resolves a chain and allocates; the
  documented shape is one per language, indexed by `Tag`. `BENCH.md` prices
  both.
- **Read `Printer.Tag()` as "the language that answered".** It is the language
  REQUESTED. When a key came from the fallback the two differ, and the SDK does
  not report that per call — `Store.Missing` answers it exhaustively, at build
  time.
- **Treat a `Negotiate` result as untrusted.** It is always one of the tags the
  caller declared supported, and never the zero `Tag`.
- **Expect `Accept-Language: en` to select a supported `en-GB`.** RFC 4647 §3.4
  truncates the range and never extends it; name the catalogue after the base
  language.

## Verification

```
bazel test --config=race //pkg/v1/i18n:i18n_test
# OR
cd pkg && GOWORK=off go test -race ./v1/i18n
```
