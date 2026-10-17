<!-- updated: 2026-10-17T22:00:00Z -->
# framework/app/store — the stores, for a product that declares them

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/store"` (ADR 0179 §1, step
3). It plugs in the engines kit's stores run on, kit's scope on a database and
the migrations of kit's tables (`storeengine.Enable`), and makes kit's own store
code reachable (`iapp.EnableStores`, step 3b) — what kit mounts, starts and
answers for the stores, which names no SDK type and so stays in the core, but
which a program reaches only through this package: everything a store or a
database needs — `app.Transact` on a database, sealing at rest, history,
revisions, trash, retention, holds, the privacy journal, `Records`, `data
copy`, and Main's commands on them (`migrate`, `privacy`, `revisions`,
`data`, `watch`). A product that declares none links none of them; one that declares a
store or a database without the import is refused at the start, every
declaration at once, the refusal naming this import and carrying
`CodeSubsystemMissing` (0.4.2.83).

`framework/app` does not import this package (ADR 0179 step 10; it did
until then, Q8): a product that declares a store or a database imports it
— `kit gen` writes the line for a designed product (§5, the platform's
step 9) —, and so do the three connectors (`framework/connectors/
{postgres,mysql,sqlite}`, §4), `framework/app/queue` (step 4, Q4: a queue on
a database joins the stores' transactions), `framework/app/workflow` (step
5, Q4: a workflow runs over a store) and `framework/app/mail` (step 6, Q4: a
mailer's outbox on a database is on a database of the stores', sealed under
data keys kept in a store). It runs a product's migrations, `app.Migration`s
— kit's own type since step 10 —, on the SDK's migrator.

## Contents

| File | Holds |
|---|---|
| `store.go` | the package documentation and `enable`, which calls `storeengine.Enable` and `iapp.EnableStores` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the calls it makes are into `framework/internal/app/storeengine` and `framework/internal/app` (`store_enable.go`); the refusal naming the import lives in the core (`subsystem.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./app/store/ && GOWORK=off go test ./app/ -run 'ImportsTheStores|TheSmallestProduct' && GOWORK=off go test ./internal/app/ -run 'WithoutItsSubsystem|WithItsSubsystem'
```
