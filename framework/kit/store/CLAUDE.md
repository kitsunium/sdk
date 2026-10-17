<!-- updated: 2026-10-17T12:00:00Z -->
# framework/kit/store — the stores, for a product that declares them

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/store"` (ADR 0179 §1, step
3). It plugs in the engines kit's stores run on, kit's scope on a database and
the migrations of kit's tables (`storekit.Enable`): everything a store or a
database needs — `kit.Transact` on a database, sealing at rest, history,
revisions, trash, retention, holds, the privacy journal, `Records`, `data
copy`. A product that declares none links none of them; one that declares a
store or a database without the import is refused at the start, every
declaration at once, the refusal naming this import and carrying
`CodeSubsystemMissing` (0.4.2.83).

Until ADR 0179's step 10, `framework/kit` imports this package itself
(`subsystems.go`, Q8), and the three connectors (`framework/connectors/
{postgres,mysql,sqlite}`) do too (§4): no product needs the line yet, and
`kit gen` will write it (§5).

## Contents

| File | Holds |
|---|---|
| `store.go` | the package documentation and `enable`, which calls `storekit.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/kit/storekit`; the refusal naming the import lives in the core (`subsystem.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/store/ && GOWORK=off go test ./kit/ -run 'ImportsTheStores|TheSmallestProduct' && GOWORK=off go test ./internal/kit/ -run 'WithoutItsSubsystem|WithItsSubsystem'
```
