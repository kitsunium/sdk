<!-- updated: 2026-10-17T23:30:00Z -->
# framework/app/testdata/workflowprobe — the smallest product, with the workflows imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/app/workflow` blank-imported — and through it `framework/app/store`
(ADR 0179 Q4) —, and two workflows declared over entities and states of
distinct types (`Ticket` and its string `Phase`, `Job` and its integer
`Step`). `initgraph_external_test.go`'s
`TestAProductThatImportsTheWorkflowsLinksThem` builds it and finds linked the
workflow subsystem's engine (`framework/internal/app/workflowengine`) and the
SDK's statemachine under it — instantiated once, at kit's entity (Q3), whatever
the product declares, while the facade is compiled at both workflows' types —, beside the store subsystem's engines, and nothing of the mailers' (step 6), nor of
the jobs' (step 7), nor of the queue subsystem's brokers: the half of ADR 0179 §7 that keeps the other
from passing by testing nothing, for step 5.

It holds kit's English table and nothing of its languages after English —
neither the SDK's i18n, not even its inits, nor the French catalogue: those
are `framework/app/studio`'s (step 8, `linksNoWords`).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, its two stores and workflows, `main`, and the blank import of `framework/app/workflow` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/app`, `framework/app/workflow` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./app/ -run 'ImportsTheWorkflows' -v
```
