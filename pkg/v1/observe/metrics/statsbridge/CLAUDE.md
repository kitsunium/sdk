<!-- updated: 2026-11-03T15:00:00Z -->
# pkg/v1/observe/metrics/statsbridge/

## Purpose

The public facade of `internal/service/observe/metrics/statsbridge` (#871):
each SDK engine's Stats value as a metrics `Observer`, for
`metrics.RegisterObservers`. Pure forwarders (`gen_facade.go`, kit's, from
`design/observe/metrics.yaml`); the README is generated (`make docs-readme`).

## Surface

| Symbol | Kind | What |
|---|---|---|
| `Cache(name, stats)` | func | a cache's `Stats` method value — `kitsunium.cache.{hits,misses,evictions,weight}` |
| `Docstore(name, stats)` | func | a docstore's — `kitsunium.docstore.{documents,pending,folds,versions.*}` |
| `Forward(name, stats)` | func | a forwarder's — `kitsunium.forward.{pending,pending_bytes,enqueued,sent,retries,dropped,expired,dead_lettered}` |
| `Queue(name, counter, timeout)` | func | a queue broker's `Counter` — `kitsunium.queue.{queued,leased,dead,oldest_age}` |
| `InstanceKey` | const | `"kitsunium_instance"`, the attribute a named adapter stamps |

## Do NOT

- Add an engine's logic here: the adapters are the internal package's.
