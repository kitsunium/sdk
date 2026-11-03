<!-- generated from internal/service/observe/logger/writer/otlpjson/sink_bench_test.go — run `cd internal && go test -run=NONE -bench=. -benchmem -count=3 ./service/observe/logger/writer/otlpjson` to refresh -->
# Benchmarks — `internal/service/observe/logger/writer/otlpjson`

What one record costs the "otlpjson" writer's terminal sink (#867): its
encoding as an OTLP LogRecord into the document buffer the sink reuses, the
exporter a no-op so the figure is the sink's own. A typical record — a
message, three attributes (a string, an int, a duration), a span.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core Processor, 4 of its cores (`GOMAXPROCS=4`) |
| RAM                | 14 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git branch         | `wip/sdk-lot-5-obs` |
| Generated          | 2026-11-03 |
| Bench wall-clock   | `-benchtime=1s`, `-count=3` |

## Results

```
BenchmarkSink_OTLP_Batch16-4   172996   6931 ns/op   433.2 ns/record   0 B/op   0 allocs/op
BenchmarkSink_OTLP_Batch16-4   158904   7378 ns/op   461.2 ns/record   0 B/op   0 allocs/op
BenchmarkSink_OTLP_Batch16-4   175434   6956 ns/op   434.8 ns/record   0 B/op   0 allocs/op
BenchmarkSink_OTLP_Write-4    2319835    512.9 ns/op                  0 B/op   0 allocs/op
BenchmarkSink_OTLP_Write-4    2306455    514.9 ns/op                  0 B/op   0 allocs/op
BenchmarkSink_OTLP_Write-4    2242407    526.6 ns/op                  0 B/op   0 allocs/op
```

## Reading

- **Zero allocations per record**, as the JSON console sink
  (`internal/service/observe/logger/BENCH.md`, `Sink_Console_Discard`: 0) —
  #867's bound is "within one". The document buffer grows to the largest
  batch once and is reused under the sink's mutex; Write reuses a one-record
  scratch. A GROUPED attribute costs one allocation for its dotted key.
- **~435 ns per record in a batch of 16**, ~515 ns alone: the resource and
  scope are written once per document, so a batch shares them. The exporter
  — a POST through the shared OTLP sender, or a publication into
  app/forward's queue — is the caller's cost and is not measured here.
