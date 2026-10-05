<!-- updated: 2026-10-05T00:00:00Z -->
# framework/kit/config/yaml — YAML configuration files

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/config/yaml"`. It gives kit
the `pkg/v1/data/codec/yaml` codec for configuration files of extension `yaml` and `yml`
(`ikit.RegisterConfigFormat`). kit reads JSON itself; a product with a
YAML file and without this import is refused at the start, the refusal
naming the import.

## Contents

| File | Holds |
|---|---|
| `yaml.go` | the package documentation and `register`, which registers the YAML codec |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour: the one call it makes is into `framework/internal/kit`, where the behaviour, its tests (`subsystems_test.go`, `subsystems_internal_test.go`) and the refusal naming the import live.
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/config/yaml/ && GOWORK=off go test ./internal/kit/ -run 'NotImportedIsRefused|WithoutItsPackageIsRefused'
```
