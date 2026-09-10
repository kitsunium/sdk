<!-- generated from pkg/v1/client/client_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=500ms -count=3 ./v1/client/` and take medians -->
# Benchmarks — `pkg/v1/client`

The outbound half of the `net` domain (ADR 0029): TLS/mTLS identity, per-phase
deadlines, policy, a bounded response read. Every number below is measured
against a **loopback** origin that answers immediately, so the network is held
at its floor and what remains is the SDK's own contribution.

## The wrapper is 5.5 % of an outbound call

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Get`, admitted | 186 823 | 8 969 | 101 |
| `Get`, four query parameters | 193 312 | 9 392 | 108 |
| `New` | 1 201 | 1 368 | 9 |

101 allocations sounds like a lot until the profile attributes them. On
`alloc_objects`, the SDK's own flat share — `guard.RoundTrip`, which is where
the policy check and the per-phase deadlines live — is **5.53 %**. Everything
else under `Client.Get` is `net/http`: `Request.write`, `setRequestCancel`,
`persistConn.roundTrip`, `MIMEHeader.Set`, `Transport.getConn`.

So **94 % of what an outbound call costs is the standard library doing HTTP**,
and adding identity, policy and bounded reads on top costs about a twentieth of
it. That is the number that judges a wrapper, and it is the reason ADR 0029
adapts `net/http` rather than reimplementing it.

Four query parameters cost 6.5 µs and seven allocations — real, but 3 % of the
call.

## A refused path costs 5 % of an admitted one, and never dials

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Get`, admitted | 186 823 | 8 969 | 101 |
| `Get`, **denied by policy** | **9 123** | 2 826 | 35 |

**20× cheaper**, and the socket is never touched — the policy runs before the
transport. That is what makes a broad policy affordable: a service can deny by
default and enumerate what it allows without paying for the denials.

The 35 allocations that remain are the typed refusal and its fields, which is
what lets a caller route on `errs.HasCode` and see the path that was refused in
the private half.

## The connection pool scales

`Get` across eight goroutines sharing one client is **37 843 ns** against
186 823 serial — **4.9×**. The pool is doing its job; the residue from a
theoretical 8× is the loopback origin, which is a single `httptest` server
answering all eight.

The practical reading: one `Client` per dependency, shared across the whole
process. Constructing one per request would cost 1.2 µs and, far worse, a fresh
connection pool that never warms.

## What these numbers are NOT

They are a loopback. A real dependency across a network adds hundreds of
microseconds to milliseconds, against which every figure above becomes noise —
which is precisely why they were measured this way. The question a wrapper has
to answer is "what do I add", and 5.5 % of the allocations is that answer.

## Reproducibility envelope

> **Numbers vary across machines.** Spreads across three runs were 2–4 %, which
> is why medians are reported without further ceremony. The allocation column is
> exact.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 9ca128c |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=500ms -count=3`, medians |

## Results (medians of three)

```
BenchmarkGet_Admitted-8             186823.0 ns/op    8969 B/op  101 allocs/op
BenchmarkGet_WithQuery-8            193312.0 ns/op    9392 B/op  108 allocs/op
BenchmarkGet_PolicyRefused-8          9123.0 ns/op    2826 B/op   35 allocs/op
BenchmarkGet_Parallel-8              37843.0 ns/op   12717 B/op  119 allocs/op
BenchmarkNew-8                        1201.0 ns/op    1368 B/op    9 allocs/op
```
