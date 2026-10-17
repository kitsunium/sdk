<!-- updated: 2026-10-18T02:00:00Z -->
# framework/kit/testdata/jobsprobe — the smallest product, with the jobs imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/jobs` blank-imported — and no other registration package: a job
needs none (ADR 0179 Q4 names no import for it) — and two jobs declared, one
every minute (`refresh`) and one on a cron expression (`rotate`).
`initgraph_external_test.go`'s `TestAProductThatImportsTheJobsLinksThem`
builds it and finds linked the jobs subsystem's scheduler
(`framework/internal/kit/jobkit`) and the SDK's scheduler under it, and
nothing of the store, queue, workflow and mail subsystems — the half of ADR
0179 §7 that keeps the other from passing by testing nothing, for step 7.

It holds kit's English table and nothing of its languages after English —
neither the SDK's i18n, not even its inits, nor the French catalogue: those
are `framework/kit/studio`'s (step 8, `linksNoWords`).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, its two jobs, `main`, and the blank import of `framework/kit/jobs` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/jobs` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheJobs' -v
```
