# internal/core/app/forward/

## Purpose

The core mirror of `internal/service/app/forward`, store and forward for an
export (ADR 0185). It holds that package's error codes and sentinels and
nothing else: ADR 0160 puts every `errs.Define` of a service package in the
core package at the same path, so a domain's codes are found in one layer and
the facade's sentinels alias the core.

There is no port here. The forwarder is one engine over `core/data/queue` and
`core/app/resilience`; its `Sender` is a function type, and its
configuration, its policy and its counters are the service's, which
`pkg/v1/app/forward` aliases there (ADR 0074).

## Surface

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `gen_codes.go` | `CodeForwardMisconfigured`, `CodeForwardFull`, `CodeBatchTooLarge` — range `0.3.93.*`; `ForwardMisconfigured`, `ForwardFull`, `BatchTooLarge` (`errs.Static`) — written by kit gen from `design/app/forward.yaml` (ADR 0164) |

## Error codes

Range `0.3.93.*`, allocated to `internal/service/app/forward` by ADR 0185 and
declared here (ADR 0160) — the layer byte reads 3, the engine's.

| Code | Reason | Status | When |
|---|---|---|---|
| `0.3.93.1` | `FORWARD_MISCONFIGURED` | exit 78 | `New` refused its `Config` — no `Broker`, `Send` or `Retryable`, an unknown `WhenFull`, a negative bound or timeout — or an adapter was built over a nil forwarder; the fields name the setting and the problem |
| `0.3.93.2` | `FORWARD_FULL` | HTTP 503 | a batch the queue had no room for, under `DropNewest`, under `Block` once `BlockTimeout` ran out, or under `DropOldest` when nothing older could be taken out; the `policy` field names the policy |
| `0.3.93.3` | `BATCH_TOO_LARGE` | HTTP 413 | a batch larger than `MaxBytes` on its own; the fields carry its size and the bound, never its bytes |

An expired batch and a batch the collector refused raise no code of this
range: the first is acknowledged and counted, the second is dead-lettered with
the collector's own verdict as its recorded reason, code and cause.

## Do NOT

- **Renumber a code** — the value is a wire contract.
- **Put mechanism here.** The bounds, the full policy, the retry and the
  counters are the service's.
- **Name a batch's bytes in a Public or a field.** A batch is telemetry: it
  may carry anything a span attribute carried.
