<!-- updated: 2026-10-17T23:00:00Z -->
# framework/app/testdata/coreprobe — the smallest product, on kit's core alone

A test fixture, not a package a product imports: `cliprobe`'s service and
command — one service, one default fail-safe CLI command that writes a line —
on `framework/internal/app` directly. Until ADR 0179's step 10 the facade
(`framework/app`) imported every registration package, so a product built on
it linked every subsystem, and this probe was the program that imported
none. Since step 10 the facade imports none either: `cliprobe` is that
program again, and this probe stays as its control on the core alone —
`initgraph_external_test.go`'s `TestTheSmallestProductLinksNoSubsystem`
builds both and reads their symbols: none of the store subsystem's engines,
none of kit's own store code (`kitStoreCode`, step 3b), none of the queue
subsystem's brokers (step 4) nor of the SDK's queue and sql, not even their
inits; nothing of the workflow subsystem's engine nor of the SDK's
statemachine (step 5); nothing of the mail subsystem's outbox nor of the
SDK's spool (step 6), nor of the SDK's mail transports beyond their inits;
nothing of the jobs subsystem's scheduler nor of the SDK's scheduler (step
7). The other probes (`storeprobe`, `queueprobe`, `workflowprobe`,
`mailprobe`, `jobsprobe`, `studioprobe`) are this one with one registration
package imported.

It holds kit's English table and nothing of its languages after English —
neither the SDK's i18n, not even its inits, nor the French catalogue: those
are `framework/app/studio`'s (step 8). There is no allow-list.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/app` and the standard library only: no registration package, no subsystem.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./app/ -run 'TheSmallestProduct' -v
```
