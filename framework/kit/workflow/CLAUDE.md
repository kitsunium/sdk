<!-- updated: 2026-10-17T23:30:00Z -->
# framework/kit/workflow — the workflows, for a product that declares them

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/workflow"` (ADR 0179 §1,
step 5). It plugs in the engine kit's workflows run on — the SDK's
statemachine, instantiated once over kit's entity and the state's spelling
(Q3) — and its loop of timers and guards (`workflowkit.Enable`). A product
that declares no workflow links none of it; one that declares a workflow
without the import is refused at the start, every declaration at once, the
refusal naming this import and carrying `CodeSubsystemMissing` (0.4.2.83).

It imports `framework/kit/store` (ADR 0179 Q4): a workflow runs over a store.
Until ADR 0179's step 10, `framework/kit` imports this package itself
(`subsystems.go`, Q8): no product needs the line yet, and `kit gen` will
write it (§5).

kit's own workflow code — `WorkflowService[E, S]`, the typed facade, its
port over the store, its journal in the data directory or on a database, the
Studio's census and transitions — names no SDK type and stays in the core; it
is generic in the product's types, so a program that declares no workflow
compiles none of it, and this package needs no `Enable` of the core's
(unlike `framework/kit/store`'s `EnableStores`).

## Contents

| File | Holds |
|---|---|
| `workflow.go` | the package documentation, the blank import of `framework/kit/store`, and `enable`, which calls `workflowkit.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/kit/workflowkit`; the refusal naming the import lives in the core (`subsystem.go`, `workflow_plug.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/workflow/ && GOWORK=off go test ./kit/ -run 'ImportsTheWorkflows|TheSmallestProduct' && GOWORK=off go test ./internal/kit/ -run 'AWorkflow'
```
