<!-- updated: 2026-10-22T00:00:00Z -->
# framework/app/testdata/otlpprobe — the smallest product, with the OTLP export imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/app/otlp` blank-imported — and no other registration package: the
export keeps its queues itself, in the data directory or in memory, and
needs neither the store nor the queue subsystem — and an export declared
(`app.OTLP`, an endpoint on the loopback). `initgraph_external_test.go`'s
`TestAProductThatImportsTheOTLPExportLinksIt` builds it and finds linked the
export's engine (`framework/internal/app/otlpengine`) and the SDK's
`app/forward` under it, beyond its inits, and nothing of the store, queue,
workflow, mail and jobs subsystems' engines — the half of ADR 0179 §7 that
keeps `TestTheSmallestProductLinksNoSubsystem` from passing by testing
nothing, for issue #483's export.

It holds kit's English table and nothing of its languages after English
(`linksNoWords`), nor anything of the Studio (`linksNoStudio`).

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, its export, `main`, and the blank import of `framework/app/otlp` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/app`, `framework/app/otlp` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./app/ -run 'ImportsTheOTLP' -v
```
