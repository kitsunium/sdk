<!-- generated from third-party/codec/yaml/codec_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=6 ./third-party/codec/yaml/` from the repo root; every time below is the benchstat median of six samples. -->
# Benchmarks — `third-party/codec/yaml`

`gopkg.in/yaml.v3` behind the `"yaml-full"` Format. These are the numbers the
SDK's `"yaml"` Format had while it was this implementation; the fixtures are
the native codec's (`internal/service/codec/yaml/codec_bench_test.go`, copied
verbatim), so the two pages compare row for row.

Machine: Apple M1 Pro, 16 GiB, macOS 26.6.2, go1.27.1 darwin/arm64, measured
2026-10-03 on the reorganisation branch while other builds shared the machine
(load average ≈ 17 on 10 cores): the times carry the noise benchstat reports
beside them; the allocation columns are exact.

| Benchmark | time/op | ± | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Marshal/small | 2.83 µs | 34 % | 6 850 | 28 |
| Marshal/medium | 21.5 µs | 10 % | 37 858 | 148 |
| Marshal/large | 3.17 ms | 16 % | 11 032 047 | 18 435 |
| UnmarshalStruct/small | 5.63 µs | 138 % | 7 960 | 63 |
| UnmarshalStruct/medium | 30.3 µs | 4 % | 21 106 | 347 |
| UnmarshalStruct/large | 3.82 ms | 7 % | 1 948 014 | 43 537 |
| UnmarshalAny/small | 5.35 µs | 6 % | 8 352 | 70 |
| UnmarshalAny/medium | 33.0 µs | 8 % | 22 875 | 381 |
| UnmarshalAny/large | 4.11 ms | 10 % | 2 163 822 | 50 042 |

The fixtures: *small* is three scalars (37 bytes); *medium* a service
configuration with nested sections, a flow list, a map and a literal block
(527 bytes); *large* a 500-record catalogue in block style (47 927 bytes).

`Marshal/large` allocates 10.5 MiB to write a 57 927-byte document: yaml.v3 builds a
node tree and an event stream and grows its emitter buffer per event.

`internal/service/codec/yaml/BENCH.md` runs these benchmarks ALTERNATELY with
the native codec's, eight samples each, so the two columns share the same
machine load: the native subset is 2.9× to 9.9× faster (4.96× in geometric
mean) and makes 1 to 4 752 allocations where this codec makes 28 to 50 042.
Its absolute times are higher than the ones above because the machine was
busier then; its ratios are the comparison to quote.
