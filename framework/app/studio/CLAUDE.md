<!-- updated: 2026-10-17T23:00:00Z -->
# framework/app/studio — the Studio's dev API and kit's languages, for a product that wants them

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/studio"`. It lets a serving
app in dev mount the read-only API the kit tool's Studio reads — the graph
and its event stream, traces, instances, items, source, dev tools, the
profiler — whose routes, guard and handlers are `studioengine`'s since ADR 0180
step 1, read over the app's host (`iapp.EnableStudio`) —, and it reads the
kit tool's variables (`KIT_DEV_*`, `KIT_STUDIO`, `KIT_STUDIO_REMOTE`,
`KIT_ANALYZE`), which the core reads no more. Since ADR 0179 step 8 it also plugs in kit's
sentences in the languages after English — French today, the catalogues and
the SDK's `i18n` in `studioengine` (`plug.Words`) —, which only the Studio shows,
in its reader's language (Q6). Without it none of those routes is linked, a
diagnostic of the graph carries its English alone (`Texts` holds `en`), and
neither the SDK's i18n nor the French catalogue is in the binary; the
terminal, the logs and an error read English either way, byte for byte. In
dev the start warns that the Studio was asked for. The facade
(`framework/app`) does not import it: the Studio stays opt-in, and a product
shown in it imports it — the platform's `kit` writes that import, under its
dev build tag, for the product it serves.

## Contents

| File | Holds |
|---|---|
| `studio.go` | the package documentation and `enable`, which plugs the Studio in — the tool's variables, its routes with the event stream and the profiler, and kit's catalogues after English (`studioengine.Enable`: `plug.Studio`, `plug.Words`) — and calls `iapp.EnableStudio` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour: its two calls are into `framework/internal/app/studioengine`, where the routes, their handlers and the tool's variables live with their tests (`studio_internal_test.go`, `env_internal_test.go`), and into `framework/internal/app`, where the host they read, the start's warning naming the import and the suites that serve the Studio (`studio_test.go`, `devtools_test.go`) live.
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/studio/ && GOWORK=off go test ./internal/kit/ -run 'NotImportedIsRefused|WithoutItsPackageIsRefused'
```
