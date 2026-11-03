<!-- updated: 2026-11-03T15:00:00Z -->
# internal/service/observe/metrics/statsbridge/

## Purpose

The numbers the SDK's engines already count, on a dashboard (#871). Each
adapter wraps an engine's Stats METHOD VALUE as a
`core/observe/metrics.Observer`; `metrics.RegisterObservers` creates the
observable instruments once and reads the Stats once per collection.

## What is covered — every engine with a stable Stats value on main

| Adapter | Engine | Statistics (kind, unit) |
|---|---|---|
| `Cache(name, stats)` | `internal/kernel/collections/cache` — `Cache.Stats`, `Striped.Stats`; the `data/cache` memory engines are built on it | `kitsunium.cache.hits`, `.misses` (counters, `{lookup}`), `.evictions` (counter, `{entry}`), `.weight` (gauge, `{weight}`) |
| `Docstore(name, stats)` | `internal/service/data/docstore` — `Store.Stats` | `kitsunium.docstore.documents` (gauge, `{document}`), `.pending` (gauge, `{entry}`), `.folds` (counter, `{fold}`), `.versions.former` (gauge, `{version}`), `.versions.trashed` (gauge, `{document}`), `.versions.bytes` (gauge, `By`) |
| `Forward(name, stats)` | `internal/service/app/forward` — `Forwarder.Stats` | `kitsunium.forward.pending` (gauge, `{batch}`), `.pending_bytes` (gauge, `By`), `.enqueued`, `.sent`, `.retries`, `.dropped`, `.expired`, `.dead_lettered` (counters, `{batch}`) |
| `Queue(name, counter, timeout)` | `internal/core/data/queue.Counter` — the memory, file and SQL brokers | `kitsunium.queue.queued`, `.leased`, `.dead` (gauges, `{message}`), `.oldest_age` (gauge, `ms`) |

**Not covered, and why.** The issue's first wave also named resilience (breaker
state, bulkhead, throttle), `lock` and the `net/server` State: none of them
has a Stats VALUE on main — a breaker exposes its state, the server a `State`
of another shape —, and #871 says not to invent one. `proc/self`'s
`StatsValue` is the runtime's, which `goruntime.RuntimeInstruments` already
publishes under the OpenTelemetry names (#868). `FoldError` (an error) and the
cache's `Policy` (an enum) are not numbers and are left out.

## Why-this-shape

- **A method value, not the engine.** The kernel cache may import nothing of
  the core (rule 1), and the docstore is generic over its document type: a
  `func() StatsValue` covers both without a type parameter, and no engine
  learns that metrics exist.
- **Read once per collection.** `RegisterObservers` counts the callbacks a
  collection runs and reads the engine on the first (`statReader`), so ten
  instruments cost one Stats call.
- **`InstanceKey` is `kitsunium_instance`.** The Prometheus connector refuses
  a dotted label name; the underscore lets one key travel on both wires.
- **The queue's timeout.** `Counter.Stats` is a query on SQL and a directory
  walk on file. Zero reads under no deadline and allocates nothing (what a
  memory broker wants); a positive timeout bounds the read at the cost of the
  context's allocations, per collection.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/observe/metrics.yaml` |
| `statsbridge.go` | `InstanceKey`, `Cache`, `Docstore`, `Forward`, `Queue` and their observers; `instance` (the attribute set built once), `clampUint` |
| `statsbridge_external_test.go` | the four engines moved, registered, collected — values, kinds, units and instance — and read through the Prometheus exporter (underscored names); an observer read once at registration and once per collection, a kind-less statistic dropped |
| `statsbridge_alloc_test.go` | race-off lane (`tools/alloc-lane-targets.txt`): each adapter's `Observe` allocates nothing |

## Verification

```
cd internal && go test -race ./service/observe/metrics/statsbridge/
cd internal && go test -run AllocationFree ./service/observe/metrics/statsbridge/
```
