<!-- generated from internal/service/mail/compose_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./mail/` to refresh -->
# Benchmarks — `internal/service/mail`

Composition, header encoding, and the in-memory transport. Nothing here opens a
socket, so these numbers are the part of a send that is this SDK's own — the
network is milliseconds and lives on the other side of `net/smtp`.

The actionable question is not "is composing a mail fast enough" (a send costs
milliseconds of network; composition costs microseconds). It is **what does an
attachment cost per megabyte, and how much memory does the SDK touch to produce
it** — because a mail server's size limit is expressed in the ENCODED size, and
a service that composes several large messages at once is sized by the peak.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This particular box
> also runs several agents concurrently, so the microsecond-scale rows move by
> roughly ±30 % between runs; the megabyte-scale row and every allocation count
> are stable.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` (worktree `agent-ace2e3861a9550f84`) |
| Git commit         | `0b9dee0` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkComposeSimple-8           102555     11570 ns/op                              4064 B/op   42 allocs/op
BenchmarkComposeAlternative-8       55668     20459 ns/op                              7104 B/op   66 allocs/op
BenchmarkComposeAttachment1MiB-8      501   2243739 ns/op   467.33 MB/s   1.370 exp  1446876 B/op   80 allocs/op
BenchmarkEncodeHeaderASCII-8        96529     12518 ns/op                              4096 B/op   42 allocs/op
BenchmarkEncodeHeaderNonASCII-8     60232     20358 ns/op                              8096 B/op   53 allocs/op
BenchmarkMemoryTransportSend-8      88161     14318 ns/op                              4152 B/op   45 allocs/op
BenchmarkValidateOnly-8            856642      1442 ns/op                                16 B/op    3 allocs/op
```

`MB/s` is the INPUT rate: megabytes of attachment consumed per second. `exp` is
the expansion ratio — composed message size divided by attachment size.

## What the numbers say

**An attachment costs 2.2 ms per mebibyte and expands by 1.370×.** Both are
facts a caller can size a system with. The expansion is not an implementation
detail: base64 is 4/3 by construction (RFC 2045 §6.8) and the 76-octet line
breaks add 2/76, giving 1.368 — the measured 1.370 is that plus the header
block. So a relay advertising a 25 MB message limit accepts an attachment of
about **18.2 MB**, and a caller who sizes on the raw bytes will have messages
rejected at 25 MB with no idea why.

Per megabyte: **≈2.24 ms/MiB, ≈2.14 ms/MB**, at 467 MB/s of input. That is
base64 plus one copy; the profile below says so.

**Memory touched is now 1.01× the composed output, and used to be 2.9×.** This
is the one optimisation in the package and it was measured before it was made.
`go tool pprof -top -sample_index=alloc_space` on the pre-change binary put
`bytes.growSlice` at **43.7 % of all bytes allocated** — the output buffer
doubling its way up from 64 bytes to 2 MiB. `estimateSize` now pre-sizes it, and
the base64 half of that estimate is exact rather than guessed:

| | before | after |
|---|---|---|
| 1 MiB attachment, B/op | 4 199 396 | 1 446 876 |
| 1 MiB attachment, ns/op | 3 822 255 | 2 243 739 |
| 1 MiB attachment, allocs/op | 95 | 80 |
| simple message, B/op | 4 256 | 4 064 |

The allocated bytes for the big case fell by **65 %** and the time by **41 %**,
for a function that computes an integer. Nothing else in this package was
optimised, because nothing else showed up: the CPU profile of a simple compose
is dominated by `mime/quotedprintable.(*Writer).write` at 26 % cumulative, which
is the standard library doing the work the RFC requires.

**The injection gate costs 1.4 µs and 16 B for a five-address message.** That is
`Validate` alone — every header value scanned for CR, LF and NUL, every
addr-spec walked, every attachment checked. It is 12 % of a simple compose and
0.06 % of a compose with a 1 MiB attachment. There is no configuration to turn
it off and this row is why there does not need to be one.

The 16 B and 3 allocations are the `[]addressGroup`
table `validateAllAddresses` builds and the two index strings it formats. A
refusal costs more — a typed `*errs.Error` with fields — and that asymmetry is
deliberate: the accepting path is the hot one.

**Encoding a non-ASCII header costs 1.6× an ASCII one** (20.4 µs against
12.5 µs, 8 096 B against 4 096 B). That is the whole case for encoding ONLY when
necessary rather than always: an ASCII subject skips the RFC 2047 path
completely and stays readable in the raw message, and the difference is not
rounding error. The non-ASCII row also folds across five continuation lines,
which is where its extra 11 allocations go.

**A multipart/alternative costs 1.8× a single part** (20.5 µs against 11.6 µs)
for two bodies rather than one, plus a container and a boundary. Sending both
forms is not free, and it is the right default anyway — but a caller sending
only plain text should not populate `HTML` "just in case".

**The in-memory transport costs 14.3 µs per message**, which is a compose plus
the record. It is deliberately not cheaper: it runs the SAME composer as the
SMTP transport, which is what makes it a double rather than a stub. A consumer's
test suite sending a thousand mails pays 14 ms for the guarantee that a message
production would refuse is refused in the test.

## What is not measured here

The SMTP session. Dialling, the TLS handshake, EHLO, and the server's own
latency are the dominant cost of an actual send by three orders of magnitude,
and none of them is this SDK's code. The `BatchSender` capability exists because
of that ratio — one session for N messages amortises the handshake, and no
benchmark in this file could show it.
