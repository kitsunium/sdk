<!-- updated: 2026-10-17T23:00:00Z -->
# framework/internal/kit/studiokit — the Studio's event stream, profiler and languages

What `framework/kit/studio` plugs into kit (`plug`): the live events as a
Server-Sent Events stream (the SDK's `net/sse`), the profiler — the
live heap folded onto the graph's nodes, every goroutine grouped (the SDK's
`profiling`) —, and kit's sentences in the languages after English (ADR 0179
step 8, Q6): the catalogues the Studio shows its reader's language from,
`locales/fr.json` today, compiled by the SDK's `i18n` behind `plug.Words`.
kit keeps the routes, the guard, the error words and English — its own table,
which it hands `openWords` as the language every catalogue falls back to —;
this package keeps what needs those three SDK packages, so a product without
the Studio links and initialises none of them, and speaks English alone.

## Contents

| File | Holds |
|---|---|
| `studiokit.go` | `Enable`, which sets `plug.OpenEventStream`, `plug.StudioProfiler` and `plug.Words` |
| `events.go` | `eventStream` over `sse.Stream` |
| `profile.go` | the profiler: `heap` (capture, `foldProfile` onto the graph by `funcOwners`), `goroutines` (`goroutinesOf`, grouped by the kit labels) |
| `studiokit_compliance.go` | `var _ plug.EventStream = eventStream{}`, `var _ plug.Translator = (*translator)(nil)` |
| `words.go`, `locales/fr.json` | kit's catalogues after English: `languages` (`fr`), `openWords` — each catalogue read with `encoding/json` (`catalogueFile`; a file that does not decode is the SDK's `CatalogLoadFailed`), compiled with kit's English as the fallback (`i18n.NewStore`, one `i18n.Printer` per language) —, `translator` (`Languages`, `Keys` sorted, `Render`; a language it does not speak is `UnsupportedLanguage`). Whether a catalogue holds exactly English's keys is the core's check (`openOthers`, `TestKitSpeaksEveryLanguage`); a new sentence adds its key to `../locales/en.json` and here |
| `words_internal_test.go` | the catalogues open against an English, French renders with its placeholders and refuses one missing, a language kit does not speak is refused; `Enable` sets `plug.Words` |
| `profile_internal_test.go` | the traceback parsing, `CanonicalName`, and a fold's attribution to one node |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Verify

```sh
cd framework && GOWORK=off go test ./internal/kit/studiokit/
```
