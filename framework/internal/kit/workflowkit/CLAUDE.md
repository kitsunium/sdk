<!-- updated: 2026-10-17T23:30:00Z -->
# framework/internal/kit/workflowkit — the workflows' engine

What `framework/kit/workflow` plugs into kit (`plug.Workflows`, ADR 0179 §1,
step 5): the engine kit's workflows run on — the SDK's statemachine,
instantiated once (Q3), at `plug.WorkflowEntityValue` (the product's entity
held as `any`, and its state spelled) and `string` (the spelling, the name
kit's workflow file and the Studio write a state with) —, its loop of timers,
deadlines and guards on the app's clock, and the conversions between the
engine's values and plug's. kit's core imports none of the SDK's
statemachine: it declares every workflow with a `plug.WorkflowConfig` and
holds its engine as a `plug.WorkflowEngine`, and a program that does not
import `framework/kit/workflow` links none of this package (`framework/kit`'s
`TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore`) and is refused
at the start for a workflow it declares (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `workflowkit.go` | `Enable`, which sets `plug.Workflows` (`open`, `failed`); `open` — the SDK's `statemachine.New` over `definitionOf(cfg)`, the core's store as the engine's port as it is, the core's journal behind `journal`, the app's clock, kit's brackets (`Observe`, `Report`, `OnLoop`) and the backoff at kit's bounds; `definitionOf` and `declare` (a guard's, a deadline's, a delay's or an event's transition); `failed`, which tells a failure kit words by the SDK's sentinel (`sentinels`) |
| `engine.go` | `engine`, the one `statemachine.Machine[plug.WorkflowEntityValue, string]` as kit's core uses it; `journal`, the core's `plug.WorkflowJournal` as the engine reads and writes it; the conversions of records, steps, changes, firings, loop events, triggers and wakes |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK's engine stamps and waits on it — a test's manual clock included |
| `workflowkit_compliance.go` | the compile-time assertions: the engine is a `plug.WorkflowEngine`, a `plug.WorkflowStore` is the SDK's `Store` port as it is, the journal adapter the SDK's `Journal`, and the clock the SDK's |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug` and the SDK (`app/statemachine`, `app/resilience` for the backoff, `clock`), never kit's core nor `storekit`: the core imports no `*kit` package (ADR 0179 §3), the store is the core's port, and kit's own suites plug it in through `framework/kit/workflow` (`framework/internal/kit/subsystems_test.go`).
- One instantiation of the engine, whatever a product declares: nothing here names a product's entity or state type. The core's `WorkflowService[E, S]` wraps what the product declares — the state field, the guards, the instants, the hooks — into functions of a `plug.WorkflowEntityValue`, and a state is told by its spelling (`fmt.Sprint`), as the workflow's file always spelled it — so the core refuses, at the start, a workflow that declares two states spelled alike (`workflow.state-spelled-twice`). `framework/kit`'s `TestAProductThatImportsTheWorkflowsLinksThem` holds the engine to that one instantiation.
- The bytes at rest are unchanged: the workflow's file (`<service>/<workflow>.workflow.json`) and its table on a database (`<service>__<workflow>__workflow`) are the core's journals, written as before.
- A failure of the engine reaches the core as the SDK's error, its fields kept; the core tells the ones it words apart through `plug.WorkflowDriverConfig`'s `Failed` (`plug.WorkflowFailure`), never by matching an SDK sentinel itself.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/kit/workflowkit/ && GOWORK=off go test -race ./internal/kit/ ./kit/ -run 'Workflow|Timer|Deadline|Guard|Transition|Census|ImportsThe|TheSmallest|TheCore'
```
