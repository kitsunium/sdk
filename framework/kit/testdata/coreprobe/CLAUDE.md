<!-- updated: 2026-10-17T22:00:00Z -->
# framework/kit/testdata/coreprobe — the smallest product, on kit's core alone

A test fixture, not a package a product imports: `cliprobe`'s service and
command — one service, one default fail-safe CLI command that writes a line —
on `framework/internal/kit` directly. The facade (`framework/kit`) imports
every registration package of ADR 0179 until its step 10, so a product built
on it links every subsystem; this probe imports none, and is what
`initgraph_external_test.go`'s
`TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore` builds and reads
the symbols of: none of the store subsystem's engines, none of kit's own
store code (`kitStoreCode`, step 3b), none of the queue subsystem's brokers
(step 4) nor of the SDK's queue and sql, out of its graph since step 6, not
even their inits; nothing of the workflow subsystem's engine nor of the SDK's
statemachine, out of its graph since step 5; and nothing of the mail
subsystem's outbox nor of the SDK's spool, out of its graph since step 6, nor
of the SDK's mail transports beyond their inits; and nothing of the jobs
subsystem's scheduler nor of the SDK's scheduler, out of its graph since step
7, not even its inits. Once step 10 drops the facade's imports, `cliprobe` is that
program again and this probe can go.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, `main` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit` and the standard library only: no registration package, no subsystem.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'TheSmallestProduct' -v
```
