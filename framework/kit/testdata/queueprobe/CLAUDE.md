<!-- updated: 2026-10-17T22:00:00Z -->
# framework/kit/testdata/queueprobe — the smallest product, with the queues imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/queue` blank-imported — and through it `framework/kit/store`
(ADR 0179 Q4). `initgraph_external_test.go`'s
`TestAProductThatImportsTheQueuesLinksThem` builds it and finds linked the
queue subsystem's brokers (`framework/internal/kit/queuekit`) and the SDK's
queue under them, beside the store subsystem's engines, and nothing of the workflows' nor of the mailers' (step 6) nor of the jobs' (step 7) — the half of ADR 0179
§7 that keeps the other from passing by testing nothing, for step 4.

It holds kit's English table and nothing of its languages after English —
neither the SDK's i18n, not even its inits, nor the French catalogue: those
are `framework/kit/studio`'s (step 8, `linksNoWords`).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main`, and the blank import of `framework/kit/queue` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/queue` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheQueues' -v
```
