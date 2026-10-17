<!-- updated: 2026-10-17T23:00:00Z -->
# framework/app/testdata/cliprobe — the smallest product

A test fixture, not a package a product imports: the smallest product a
status line is — one service, one default fail-safe CLI command that writes a
line. `fresh_process_bench_test.go` in `framework/app` builds and execs it to
measure what a fresh process pays per render, and since ADR 0179's step 10 —
the facade importing no registration package — `initgraph_external_test.go`'s
`TestTheSmallestProductLinksNoSubsystem` builds it and finds no subsystem
linked: it is the measure of that ADR (its §Step 10, done).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It stays minimal: it imports none of the subsystems (`kit/store`, `kit/queue`, `kit/workflow`, `kit/mail`, `kit/jobs`, `kit/server`, `kit/studio`, `kit/config/*`), since it declares none.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run '^$' -bench AFreshProcess -benchtime 1x
cd framework && GOWORK=off go test ./kit/ -run 'TheSmallestProduct' -v
```
