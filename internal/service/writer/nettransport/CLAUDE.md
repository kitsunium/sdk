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
| `client.go` | the ONLY net/net-http file: `newConnSeam` (tcp/udp) + `newHTTPSeam`/`postRecord` + the default client and its own pool (`newHTTPClient`/`newHTTPTransport`) |
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

## The HTTP seam: whose pool, and how much of a body

**The default client owns its connection pool, because a shared one lost
deliveries.** It used to ride `http.DefaultTransport`, and net/http puts a
BODILESS response's connection — a 204 is how Loki answers a push — back in the
idle pool *before* handing the response to the waiting round trip; a
`CloseIdleConnections` on that pool landing in between closes the connection
under it, and the round trip reports `HTTP/1.x transport connection broken: http:
CloseIdleConnections called` for a record that had been delivered. The sink then
reports a failed write, and a failover or retry sends the record again: a
duplicated log line. Every `httptest.Server.Close`, every
`http.DefaultClient.CloseIdleConnections` — and this package's own closer, which
used to empty that pool whenever an http sink closed — is such a call. Measured
with 64 parallel senders whose own server closes did the emptying, at
`-race -cpu=64`: 23 delivered records reported as failed in 128 000 sends before,
0 in 128 000 after. The same defect was found as a flake in the OTLP exporters of
`service/metrics` and `service/trace`, which were fixed first on identical code.

The pool is a clone of `http.DefaultTransport` taken at construction, proxy
environment included; where that default has been replaced by something other
than an `*http.Transport`, a fresh transport with `Proxy:
http.ProxyFromEnvironment` and net/http's 90 s idle timeout stands in.
`CheckRedirect` (CWE-918) and the 30 s `Timeout` are unchanged.

**The closer releases the pool the sink owns, and only that one.** For the
default client it closes that client's idle connections. A caller-supplied
`HTTPClient` is used as given, `Transport` included, and its pool is the
caller's: the closer leaves it alone — a behaviour change, since it used to call
`CloseIdleConnections` on it, which for a client riding `http.DefaultTransport`
emptied the whole process's pool. A caller that built a client for this writer
alone releases it after closing the writer.

**The drain is bounded** by `httpDrainMaxBytes` (1 MiB). `postRecord` judges a
delivery by its status alone and reads nothing of the body, so the drain is the
only read of that remote-controlled length, and there was no bounded-read
constant here to share; the figure is the one the OTLP exporters use
(`DefaultOTLPMaxResponseBytes`), so the SDK's three HTTP emitters read a
collector's body the same way. Unbounded, a collector streaming an endless body
held the async drainer forever whenever the client had no deadline. The bound is
on bytes, not time.

The tests that pin all of this, each seen failing under a mutation named in its
doc comment: `Test_newHTTPClient_ownsItsPool` (the process pool emptied between
sends; the connection must survive), `Test_postRecord_keepAlive` (a 512 KiB
answer under a declared length — above the 256 KiB net/http drains by itself
after an early Close, so only this drain can recycle the connection),
`Test_postRecord_boundedDrain` (an endless body through a client with no
timeout, under a context budget that severs the socket so a regression fails
instead of hanging), `Test_newHTTPSeam_closer`, and
`Test_newHTTPTransport_proxy`, which drives both transport branches through a
real `HTTP_PROXY` in a CHILD process, because net/http reads the proxy
environment once per process and never proxies loopback.
`Test_newHTTPTransport_proxyChild` is that child: it returns at once unless
`KTN_NETTRANSPORT_PROXY_CHILD` is set, so it is discovered and run like any test
and needs no tag and no compensating lane (CLAUDE.md rule 12).

A test that needs a transport fault uses a live server that hangs up
(`hangUpOn`), never a closed one: a freed loopback port is handed to one of the
next 50 listeners 0.8 % of the time, so dialling it can reach another parallel
test's server.

**`loopbackConnectWorks` now tells the truth, so the real-socket arms run.** It
and `assertNilDialerShipsOverTCP` wrote `defer swallowErr(ln.Close())`, and a
deferred call's arguments are evaluated at the `defer` statement: the listener
closed before the dial. The probe answered false on every host — 0 of 1 000
calls where loopback plainly works, 1 000 of 1 000 once the Close moved into a
closure — so the nil-dialer TCP send, the real-server cases of
`Test_newHTTPSeam_realServer` and the real-server assertion of the CWE-918 test
`Test_defaultClientRejectsRedirect` never ran; that test passed on "a dial to
port 1 is refused". And when a parallel test's server was handed the freed port
in between, the probe said true against somebody else's listener and the TCP arm
failed with `connection refused` — 7 times in 400 stressed runs once the HTTP
tests above added servers, 0 since.

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
- Build the default HTTP client without its own `Transport`: a nil one is
  `http.DefaultTransport`, which any code in the process can empty. Nor replace,
  wrap, clone or close the pool of a caller-supplied `HTTPClient` — that is
  theirs.
- Drain a response without a bound.
- Dial a freed port to provoke a transport fault in a test — hang up instead.
- Write `defer swallowErr(x.Close())`: the Close runs at the defer statement.
  Use `defer func() { swallowErr(x.Close()) }()`.

## Verification

```sh
bazel test --config=race //internal/service/writer/nettransport:nettransport_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/nettransport/...
```
