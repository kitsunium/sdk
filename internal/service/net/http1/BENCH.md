<!-- generated from internal/service/net/http1/http1_bench_test.go — run `cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=6 -benchtime=300ms ./service/net/http1/` to refresh; not `make bench`, which regenerates the codec's report only -->
# Benchmarks — `internal/service/net/http1`

The restricted HTTP/1.1 profile's codec. The benchmarks answer two
questions: **what does framing a request cost against what the default engine
pays** (`net/http.ReadRequest`, the competitor), and **how far is it from the
C reference** (`bench/cref/src/http_parse.c`, the same scope and the same
refusals, compiled with `clang -O2`). The requests are the challenge API's two
shapes: a GET carrying a bearer token and a POST carrying a JSON body.

## Reproducibility envelope

> **Numbers vary across machines, and these were taken on a busy one** (load
> average 27 to 50 on 10 cores, fuzzing and other agents running). Every
> figure is benchstat's median over six runs; the ± is its spread. The ratios
> between rows measured in the same process, and the C harness interleaved
> run by run with the Go benchmark, are the trustworthy part. No x86-64 figure
> yet: the score bench (S7) runs on the VM the plan names.

| Dimension | Value |
|---|---|
| CPU                | Apple M1 Pro, 10 cores |
| OS / kernel        | macOS 26.6 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.3 darwin/arm64, no GOEXPERIMENT (the SWAR forms) |
| C toolchain        | Apple clang, `-std=c23 -O2` |
| Git branch         | `feat/net-http1-codec` |
| Git commit         | base `88ad7f3ea` (uncommitted when measured) |
| Generated (UTC)    | 2026-10-24 |
| Bench wall-clock   | `-benchtime=300ms -count=6` |

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `Parse/get` (266 B) | **164.8** ± 8% | 0 | 0 |
| `Parse/post` (240 B, 44 B body) | **206.0** ± 16% | 0 | 0 |
| `Parse/refused` (Transfer-Encoding) | 81.1 ± 14% | 0 | 0 |
| `Parse/pipelined16` (16 GETs, one buffer) | 2 647 ± 8% (165 a request) | 0 | 0 |
| `NetHTTPReadRequest/get` | 1 441 ± 39% | 1 188 | 11 |
| `NetHTTPReadRequest/post` | 1 664 ± 59% | 1 204 | 14 |
| `AppendResponse` (JSON, one extra field) | 21.5 ± 210% (15.2 to 15.9 on a quieter run) | 0 | 0 |
| `AppendRefusal` | 4.8 ± 31% | 0 | 0 |

`Parse` frames a request in about an eighth of what `net/http` takes to read
it, and allocates nothing where `net/http` allocates 11 to 14 times.

## Against the C reference

`ParseCRefStream` parses the C harness's stream: `build_stream` of
`parse_bench.c`, 32 GET and POST pairs pipelined, 23 734 bytes, each request
carrying a 188-byte bearer token. The C side is `http_parse_request` over the
same bytes, in a loop of 20 000 passes (the harness is in the PR description;
it is not committed: `bench/cref` is a diagnostic, never a gate).

| Parser | ns/request, median of six | spread |
|---|---|---|
| C reference (`http_parse.c`) | **118.4** | 114.6 – 127.0 |
| Go (`Parser.Parse`) | **249.4** | 235.8 – 260.5 |

Eight runs, C and Go interleaved run by run. **The Go codec is about 2.1× the
C reference on this machine**, with no allocation on either side. Before the
functions were split to the linter's size and complexity bounds the same
interleaving gave about 1.9× (219 against 115 ns): the split costs about 30 ns
a request, in calls the compiler does not inline. The profile puts the time in the field-value word
scan (`swar.Set.Index`, about a third), the tchar run of names (a sixth), and
the per-field calls the Go compiler does not inline; the C reference keeps
everything in one function. What was tried and kept: a `tchar` run unrolled by
four with no bounds check, an OR-0x20 case fold (exact for the literals it
compares), a length gate before the known-name switch, and a `keep-alive`
shortcut for `Connection`. What the figures do not settle: x86-64, where both
the SWAR loop and `clang` behave differently — S7 measures it.

The codec checks more than the C reference per field (every control byte in a
value, not only NUL, CR and LF; the target's high bytes), so the two are not
doing exactly the same work: the comparison is the cost of the profile as
specified, not of an identical loop.
