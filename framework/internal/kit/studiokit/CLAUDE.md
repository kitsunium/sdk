<!-- updated: 2026-10-17T23:30:00Z -->
# framework/internal/kit/studiokit — the Studio's routes, the tool's variables and kit's languages

What `framework/kit/studio` plugs into kit (`plug`). Since ADR 0180 step 1
the Studio's routes, their guard and their handlers — the graph, the live
events as a Server-Sent Events stream (the SDK's `net/sse`), traces, the
source endpoint, instances, items, and the dev tools: the process, the
profiler (the live heap folded onto the graph's nodes, every goroutine
grouped — the SDK's `profiling`), logs, the mailbox, and on the store
subsystem the databases, former values, versions, the trash and the Privacy
page — behind `plug.Studio`'s `Mount`, and the reading of the kit tool's
variables (`KIT_DEV_*`, `KIT_STUDIO`, `KIT_STUDIO_REMOTE`, `KIT_ANALYZE`)
behind its `Env`, `Settings`, `Forced` and `DevBuild`. And since ADR 0179
step 8 kit's sentences in the languages after English (Q6): the catalogues
the Studio shows its reader's language from, `locales/fr.json` today,
compiled by the SDK's `i18n` behind `plug.Words`.

The routes read the running app through the `plug.StudioHost` kit's core
gives (`studio_host.go` there): what the app keeps — the graph, the hub, the
stores, the mailboxes, the roots of its source — stays beside its state in
the core, which answers through the host as it answers any request
(`Reply`, kit's error codes, `CodeProfileRead`). kit keeps the health probes,
the reserved `/_kit/` outside dev, the start's warning that names this
package, and English — its own table, which it hands `openWords` as the
language every catalogue falls back to. A product that does not import the
Studio links none of this package, reads none of the tool's variables, and
speaks English alone.

## Contents

| File | Holds |
|---|---|
| `studiokit.go` | `Enable`, which sets `plug.Studio` (`driver`: `studioEnv`, `studioSettings`, `devForced`, `devBuildOf`, `mount`) and `plug.Words` |
| `routes.go` | `mount`: every route in kit's order, each behind `guard` — the request's address on this machine unless the host says remote (`loopback`), its Host allowed, then the Studio's CSP, `nosniff` and `no-referrer` —; the graph, traces (`tracesShown`), one trace, instances, items; the not-found answers under `/_kit/api/` and `/_kit/` for every method kit routes but HEAD (`methods`) |
| `events.go` | `serveEvents`: the stream opened with `sse.New`, the hello (the graph's revision, the start, the phase), then every event the host streams until the client, the stream or the app stops |
| `source.go` | `serveSource`: a name with no extension it serves (`sourceLanguages`) or not local is refused before the host is asked; the host reads the file the graph names (`StudioHost.Source`), and the answer is the window around the range (`sourceContext`, `sourceMaxLines`, `atoiOr`) as a `model.Snippet` |
| `devtools.go` | the dev tools' routes: the process (`plug.SampleProcess`, 404 without a sampler), goroutines and the heap profile (`heap`, `goroutines`; a failure is `StudioHost.ProfileUnreadable`), logs (the query handed to the host, which judges the level and the limit), the mailbox (`limit` 1 to `maxMails`, `defaultMails` 200; every mailer's mail, newest first, the identifier breaking a tie) and one mail; on the store subsystem only (`StudioHost.Stores`), the databases, former values, versions, the trash and the Privacy page, each a read of the host |
| `env.go` | the tool's variables: `studioEnv` (`KIT_STUDIO` on or off, `KIT_STUDIO_REMOTE`, `KIT_ANALYZE` off), `studioSettings` (their three rows, each from the code's option, the environment or kit's default), `devForced` (`KIT_DEV_FORCED`'s names; the core keeps those it lets kit dev set), `devBuildOf` (`KIT_DEV_BUILT_AT`, `KIT_DEV_BUILD_MS`, `KIT_DEV_REBUILDS`, `KIT_DEV_CHANGED` — local paths only, at most `maxDevChanged` —, `KIT_DEV_CHANGED_MORE`) |
| `profile.go` | the profiler: `heap` (capture, `foldProfile` onto the graph by `funcOwners`, where a function lies said by the host's `Locate`, a `sourceFunc`), `goroutines` (`goroutinesOf`, grouped by the kit labels) |
| `studiokit_compliance.go` | `var _ plug.Translator = (*translator)(nil)` |
| `words.go`, `locales/fr.json` | kit's catalogues after English: `languages` (`fr`), `openWords` — each catalogue read with `encoding/json` (`catalogueFile`; a file that does not decode is the SDK's `CatalogLoadFailed`), compiled with kit's English as the fallback (`i18n.NewStore`, one `i18n.Printer` per language) —, `translator` (`Languages`, `Keys` sorted, `Render`; a language it does not speak is `UnsupportedLanguage`). Whether a catalogue holds exactly English's keys is the core's check (`openOthers`, `TestKitSpeaksEveryLanguage`); a new sentence adds its key to `../locales/en.json` and here |
| `studio_internal_test.go` | over a fake host: the guard (`TestStudioAnswersThisMachineOnly`), the process without a sampler, the mailbox's order and bounds, the source endpoint's refusals before any read. The routes against a running app — every route, its guard, its answers — are the core's suites (`studio_test.go`, `devtools_test.go`, `trash_view_test.go`, `revisions_view_test.go`…), whose binary imports `framework/kit/studio` |
| `env_internal_test.go` | the dev build read from kit dev's variables, what the tool's variables ask, their settings' origins, `Enable` setting `plug.Studio` |
| `words_internal_test.go` | the catalogues open against an English, French renders with its placeholders and refuses one missing, a language kit does not speak is refused; `Enable` sets `plug.Words` |
| `profile_internal_test.go` | the traceback parsing, `CanonicalName`, and a fold's attribution to one node |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It imports `plug`, `framework/model` and the SDK, never kit's core: what
  it needs of a running app is a method of `plug.StudioHost`, which the core
  implements; a new route that reads something new adds a method there, in
  the design first (`design/framework/kit.yaml`).
- A route answers as kit answered before it left the core (ADR 0180 step 1,
  non-breaking): the same patterns, the same guard, the same bodies and
  statuses, the errors kit's own (`StudioHost.Reply`, `NotFound`, `Invalid`).
  A query parameter judged against what the app keeps — a log level, a page
  of the trash, two version numbers — is handed to the host as received.
- No route acts on the product (D13).

## Verify

```sh
cd framework && GOWORK=off go test ./internal/kit/studiokit/ ./internal/kit/ && GOWORK=off go test ./kit/ -run 'TheSmallestProduct|ImportsTheStudio'
```
