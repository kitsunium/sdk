<!-- updated: 2026-10-05T00:00:00Z -->
# framework/kit/studio — the Studio's dev API, for a product that wants it

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/studio"`. It lets a serving
app in dev mount the read-only API the kit tool's Studio reads — the graph
and its event stream, traces, instances, items, source, dev tools, the
profiler (`ikit.EnableStudio`). Without it none of those routes is linked;
in dev the start warns that the Studio was asked for.

## Contents

| File | Holds |
|---|---|
| `studio.go` | the package documentation and `enable`, which plugs the event stream and the profiler in (`studiokit.Enable`) and calls `ikit.EnableStudio` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour: the one call it makes is into `framework/internal/kit`, where the behaviour, its tests (`subsystems_test.go`, `subsystems_internal_test.go`) and the refusal naming the import live.
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/studio/ && GOWORK=off go test ./internal/kit/ -run 'NotImportedIsRefused|WithoutItsPackageIsRefused'
```
