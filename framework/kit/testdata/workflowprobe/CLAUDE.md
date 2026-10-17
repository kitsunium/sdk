<!-- updated: 2026-10-17T23:30:00Z -->
# framework/kit/testdata/workflowprobe — the smallest product, with the workflows imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/workflow` blank-imported — and through it `framework/kit/store`
(ADR 0179 Q4). `initgraph_external_test.go`'s
`TestAProductThatImportsTheWorkflowsLinksThem` builds it and finds linked the
workflow subsystem's engine (`framework/internal/kit/workflowkit`) and the
SDK's statemachine under it — instantiated once, at kit's entity (Q3), whatever
the product declares —, beside the store subsystem's engines, and nothing of
the queue subsystem's brokers: the half of ADR 0179 §7 that keeps the other
from passing by testing nothing, for step 5.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main`, and the blank import of `framework/kit/workflow` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/workflow` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheWorkflows' -v
```
