<!-- updated: 2026-10-05T00:00:00Z -->
# framework/app/server — HTTP, for a product that serves it

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/server"`. It makes an app
in the server profile serve HTTP — endpoints, frontends, health probes — on
the SDK's server engine (`iapp.EnableServer`), and gives the runtime graph a
serving app answers its process sample (ADR 0172). A daemon or a CLI does not
import it and links none of the engine, `runtime/metrics` or the SDK's process
package; a server without it is refused at the start, the refusal naming this
import.

## Contents

| File | Holds |
|---|---|
| `server.go` | the package documentation and `enable`, which plugs the engine, the frontends' file server and the process sampler in (`serverengine.Enable`) and calls `iapp.EnableServer` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour: the one call it makes is into `framework/internal/app`, where the behaviour, its tests (`subsystems_test.go`, `subsystems_internal_test.go`) and the refusal naming the import live.
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/server/ && GOWORK=off go test ./internal/kit/ -run 'NotImportedIsRefused|WithoutItsPackageIsRefused'
```
