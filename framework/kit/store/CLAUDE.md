<!-- updated: 2026-10-17T22:00:00Z -->
# framework/kit/store — the stores, for a product that declares them

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/store"` (ADR 0179 §1, step
3). It plugs in the engines kit's stores run on, kit's scope on a database and
the migrations of kit's tables (`storekit.Enable`), and makes kit's own store
code reachable (`ikit.EnableStores`, step 3b) — what kit mounts, starts and
answers for the stores, which names no SDK type and so stays in the core, but
which a program reaches only through this package: everything a store or a
database needs — `kit.Transact` on a database, sealing at rest, history,
revisions, trash, retention, holds, the privacy journal, `Records`, `data
copy`, and Main's commands on them (`migrate`, `privacy`, `revisions`,
`data`, `watch`). A product that declares none links none of them; one that declares a
store or a database without the import is refused at the start, every
declaration at once, the refusal naming this import and carrying
`CodeSubsystemMissing` (0.4.2.83).

Until ADR 0179's step 10, `framework/kit` imports this package itself
(`subsystems.go`, Q8), and the three connectors (`framework/connectors/
{postgres,mysql,sqlite}`) do too (§4), and so does `framework/kit/queue` (step
4, Q4: a queue on a database joins the stores' transactions), and
`framework/kit/workflow` (step 5, Q4: a workflow runs over a store), and
`framework/kit/mail` (step 6, Q4: a mailer's outbox on a database is on a
database of the stores', sealed under data keys kept in a store): no product
needs the line yet, and `kit gen` will write it (§5).

## Contents

| File | Holds |
|---|---|
| `store.go` | the package documentation and `enable`, which calls `storekit.Enable` and `ikit.EnableStores` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the calls it makes are into `framework/internal/kit/storekit` and `framework/internal/kit` (`store_enable.go`); the refusal naming the import lives in the core (`subsystem.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/store/ && GOWORK=off go test ./kit/ -run 'ImportsTheStores|TheSmallestProduct' && GOWORK=off go test ./internal/kit/ -run 'WithoutItsSubsystem|WithItsSubsystem'
```
