# internal/service/writer/nettransport/

## Purpose

Registers three **stdlib** network writer factories — `"tcp"`, `"udp"`, `"http"`
(ADR 0015). Importing the package self-registers all three (no `init()`), so
`writer.Open("tcp", nettransport.NetConfig{…})` and YAML `FromConfig` topologies
resolve. This is the dep-light seam that community adapters (Loki, Elastic,
Datadog, a Kafka bridge) build on **without** pulling a vendor SDK into the tree:
each composes `levelgate(async(netSink))` over a stdlib `net.Conn` or
`http.Client`.

## Contents

| File | Role |
|---|---|
| `nettransport.go` | `WriterTCP/UDP/HTTP` singletons, `netFactory{proto}` (`Name`/`Open`/`build`) |
| `config.go` | `NetConfig` value type + `compose` (the `levelgate(async(netSink))` order) |
| `netsink.go` | `netSink` per-record sink (`Write`/`Flush`/`Close`) + `sendFunc` seam |
| `client.go` | the ONLY net/net-http file: `newConnSeam` (tcp/udp) + `newHTTPSeam`/`postRecord` |
| `decode.go` | `netFactory.Decode` (`core/writer.Decoder`) + key coercion helpers |
| `codes.go`, `errors.go` | sentinels — range 0.3.30.\* (service slot 0x1e) |

## Composition

`compose` wires `levelgate(async(netSink))` — the same order as `dbsink`/`s3`:
the level floor drops below-floor records before the async ring, the ring gives
non-blocking back-pressure (`OnDrop`), and the terminal `netSink` ships each
record synchronously to the transport. The producer **never** blocks on the
network; saturation surfaces as drops, not stalls.

## Performance

- **tcp/udp Write is 0 alloc** (BENCH.md): the payload is written verbatim and
  synchronously, consumed before `Write` returns, so no clone is needed even
  though the caller recycles the buffer. For udp the datagram boundary is the
  frame; for tcp the encoder's trailing newline delimits records.
- **http is NOT 0 alloc** by design: each record is one POST (request + body
  reader). HTTP trades the alloc for batched-collector compatibility.

## Security (CWE-918, SSRF)

When the destination is consumer-controlled, supply an allowlist seam:
`NetConfig.Dialer` (tcp/udp) or `NetConfig.HTTPClient` (http). The raw address /
URL is **never** echoed into an error — only the network identifier and byte
count are attached (secret gate). Plaintext transport: use on trusted networks
or front with a TLS-terminating collector; native TLS is a future addition.

## Decoder (YAML-reachable via `FromConfig`)

Recognised keys under a writer entry's `config:` map:

| Key | Type | Maps to |
|---|---|---|
| `address` | string (required) | `Address` (host:port for tcp/udp, URL for http) |
| `min_level` | string | `MinLevel` via `level.ParseLevel` |
| `buffer_size` | int | `BufferSize` (async ring capacity) |

The `Dialer` / `HTTPClient` SSRF seams and the `OnDrop` / `OnError` callbacks are
**code-only** and never decoded from a config blob. A malformed shape returns the shared `core/writer.WriterConfigInvalid`
(no per-package code), tagged with the protocol only — never the value.

## Error catalogue — range 0.3.30.\*

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.30.1 | `NetTransportDialFailed` | `net.Dial` (tcp/udp) failed or an empty/invalid endpoint (EX_IOERR) |
| 0.3.30.2 | `NetTransportWriteFailed` | a per-record send failed: `net.Conn.Write` error or non-2xx HTTP status (EX_IOERR) |

## Do NOT

- Add a vendor SDK here — this package is the stdlib seam; vendor transports
  (Kafka, NATS) are community adapters over the public `Sink`/`Factory`.
- Echo `address`/`url`/payload into an error (SSRF/secret gate).
- Buffer inside `netSink` — that is `async`'s job.

## Verification

```sh
bazel test --config=race //internal/service/writer/nettransport:nettransport_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/nettransport/...
```
