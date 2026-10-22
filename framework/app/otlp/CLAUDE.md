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
the queue subsystem. `framework/app` does not import this package (ADR 0179
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
