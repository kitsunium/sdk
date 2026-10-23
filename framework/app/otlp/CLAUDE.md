<!-- updated: 2026-10-22T00:00:00Z -->
# framework/app/otlp — the OTLP export, for a product that declares it

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/otlp"` (issue #483, ADR
0185, in the shape of ADR 0179 §1). It plugs in the export `app.OTLP`
declares — the product's spans and the metrics the framework derives from
them, each signal in a queue of its own over the SDK's `app/forward`,
forwarded to an OpenTelemetry collector over OTLP/HTTP or to the product's
own sender (`otlpengine.Enable`). A product that declares no export links
none of it; one that declares an export without the import is refused at the
start, the refusal naming this import and carrying `CodeSubsystemMissing`
(0.4.2.83).

It imports no other registration package: the export keeps its queues
itself, in the data directory or in memory, and needs neither the store nor
the queue subsystem — but when `otlp-database` (`app.OTLPDatabase`, issue
#537) places them on one of the product's databases, which the store
subsystem opens and on which the queue subsystem's SQL broker keeps them
(`plug.QueueDriverConfig.OpenSDK`): the product then imports
`framework/app/queue`, and the start refuses it otherwise, naming the import
(`CodeSubsystemMissing`). A product that keeps its export off a database
links none of the queue subsystem.

Importing it also gives `App.Main` the `otlp` command of a product that
declares an export (issue #536): `otlp dead-letters` lists the batches the
collector refused for good, never their payload, and `otlp replay` puts them
back into the live queue, or with `--drop` deletes them — never a route
(D13); a replay while the product runs is sent at its next poll and counted
pending by the running export within the forwarder's recount interval
(issue #580). Switching to `otlp-database` with batches left under
`<data>/.otlp` is said at the start (`otlp.file-queue-left`, logged and a
problem), and `otlp migrate [traces|metrics]` moves them — waiting and dead
— onto the database, in one transaction verified by fingerprints, the file
queues then moved aside under `<data>/.data-copy/` (issue #580); letting
the release before the switch send them is the other way. A product that
does not import it has no such command. `framework/app` does not import this package (ADR 0179
step 10): a product that declares an export imports it, and `kit gen` writes
the line for a designed product (§5).

The export is a channel of the product's, apart from `framework/telemetry`'s
(ADR 0149), which reports numbers to a tool attached on a private socket and
which the export leaves as it is. The framework's own export code — the
declaration and its settings (`otlp.go`), the component, the tracer's sink,
the readiness check and the stop budget (`otlp_run.go`) — names no SDK type
of the export's and stays in the core, reached only through the hooks
`EnableOTLP` sets (`otlp_enable.go`), so the smallest product links none of
it; its settings are read at the start, through the subsystem, never at the
declaration.

## Contents

| File | Holds |
|---|---|
| `otlp.go` | the package documentation and `enable`, which calls `iapp.EnableOTLP` — the framework's own export code, reachable from then on — and `otlpengine.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the calls it makes are into `framework/internal/app` (`EnableOTLP`) and `framework/internal/app/otlpengine`; the refusal naming the import lives in the core (`subsystem.go`, `otlp_run.go`, `otlp_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./app/otlp/ && GOWORK=off go test ./app/ -run 'ImportsTheOTLP' && GOWORK=off go test ./internal/app/ -run 'OTLP'
```
