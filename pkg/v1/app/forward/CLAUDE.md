# pkg/v1/app/forward/

## Purpose

The public facade for store and forward (ADR 0185): type aliases over
`internal/service/app/forward`, its three sentinels aliased from
`internal/core/app/forward` (ADR 0160), and three functions — `New`, and the
two producer adapters `NewSpanExporter` and `NewMetricExporter`.

Consumer-facing prose lives in the package doc comment in `doc.go`, which kit
writes from `design/app/forward.yaml`, and reaches `README.md` through
`docs/api` and `tools/genindex` (rule 10 / ADR 0167). This file is the
maintainer's half.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Forwarder` | alias | `Enqueue`, `Run`, `Stats` |
| `Config` | alias | `Broker`, `Send` and `Retryable` required; `Clock`, `MaxBatches`, `MaxBytes`, `WhenFull`, `BlockTimeout`, `SendTimeout`, `MaxAge`, `Backoff`, `Parallelism`, `PollInterval`, `DrainTimeout`, `SettleTimeout` optional |
| `FullPolicy`, `DropOldest` (the zero), `DropNewest`, `Block` | alias, constants | what `Enqueue` does at the bound |
| `Stats` | alias | onto `StatsValue`: pending, sent, retries, dropped, expired, dead-lettered, the last `Retry-After` |
| `Sender` | alias | `func(ctx, batch []byte) error` — a `DocumentExporter`'s `ExportDocument` |
| `DefaultMaxBatches`, `DefaultMaxBytes`, `DefaultBlockTimeout`, `DefaultSendTimeout`, `DefaultMaxAge`, `DefaultRetryBase`, `DefaultRetryMax` | constants | the clamps of a zero field |
| `Misconfigured`, `Full`, `BatchTooLarge` | vars | the sentinels (`0.3.93.*`), aliased from the core |
| `New`, `NewSpanExporter`, `NewMetricExporter` | funcs | the constructor and the two producer adapters |

The sentinels drop the `Forward` prefix their core names carry
(`ForwardFull` is `forward.Full`), because the package name already says it.

## Why this shape

- **In `app`, not in `observe`.** The forwarder runs `app/resilience`'s retry
  and the `pkg/v1/app` module already requires `pkg/v1/observe`: a facade in
  `observe` would close a cycle between two modules (ADR 0185 D1).
- **Generic over bytes.** The adapters know a signal; the forwarder does not,
  so the same queue serves any export.
- **The configuration is the engine's** (ADR 0074): the alias points at the
  service.

## Do NOT

- **Do NOT hand-edit `README.md` or `gen_facade.go`.** Edit the design, run
  `kit gen`, then `make api` and `make docs-readme`.
- **Do NOT add a network exporter here.** The sender is the signal's own
  OTLP/HTTP exporter, through its `DocumentExporter`.
