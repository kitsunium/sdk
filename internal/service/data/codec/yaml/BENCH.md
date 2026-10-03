<!-- generated from internal/service/data/codec/yaml/codec_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=8 ./internal/service/data/codec/yaml/` from the repo root, and the same over ./third-party/codec/yaml/ for the yaml.v3 column; every figure below is the median of eight samples. -->
# Benchmarks — `internal/service/data/codec/yaml`

The native subset against the `gopkg.in/yaml.v3` codec it replaced, on the
same fixtures: `third-party/codec/yaml/codec_bench_test.go` (the `"yaml-full"`
Format) copies this package's fixtures verbatim, so the two run the same
documents and the same Go values.

## Conditions

Apple M1 Pro (10 cores), 16 GiB, macOS 26.6.2, go1.27.1 darwin/arm64,
2026-10-03 00:51–00:55 UTC, on the tree-reorganisation branch **while other
builds and fuzzers shared the machine: the load average ran from 97 down to
48 over the run.** The two codecs' benchmark binaries were therefore run
**alternately**, eight times each, one sample per run, so both saw the same
minutes of load. What that buys and what it does not:

- the **allocation columns are exact** — they do not depend on load;
- the **ratios hold in direction and rough size** — every difference below
  is significant at p ≤ 0.002 (benchstat, n = 8 per side);
- the **absolute times are inflated** — benchstat puts ±26 % to ±600 % around
  them, and the yaml.v3 medians are 1.3 to 2.7 times what the same benchmarks
  gave at a load average of 17 (`third-party/codec/yaml/BENCH.md`). Re-measure
  on a quiet machine before quoting a time.

## Results

| Benchmark | yaml.v3 | native | faster | B/op yaml.v3 → native | allocs/op yaml.v3 → native |
|---|---:|---:|---:|---:|---:|
| Marshal/small | 5.79 µs | 0.59 µs | 9.8× | 6 851 → 48 | 28 → **1** |
| Marshal/medium | 28.8 µs | 8.25 µs | 3.5× | 37 859 → 872 | 148 → **6** |
| Marshal/large | 4.68 ms | 1.17 ms | 4.0× | 11 032 235 → 65 679 | 18 435 → **1** |
| UnmarshalStruct/small | 7.11 µs | 1.68 µs | 4.2× | 7 960 → 96 | 63 → **3** |
| UnmarshalStruct/medium | 47.5 µs | 16.2 µs | 2.9× | 21 107 → 1 241 | 347 → **9** |
| UnmarshalStruct/large | 7.77 ms | 1.65 ms | 4.7× | 1 948 019 → 115 432 | 43 537 → **504** |
| UnmarshalAny/small | 14.7 µs | 1.49 µs | 9.9× | 8 352 → 432 | 70 → **7** |
| UnmarshalAny/medium | 83.6 µs | 18.8 µs | 4.5× | 22 876 → 2 762 | 381 → **33** |
| UnmarshalAny/large | 10.7 ms | 2.10 ms | 5.1× | 2 163 852 → 320 876 | 50 042 → **4 752** |

Geometric means: **4.96× faster** (−79.8 % time), **−96.8 %** bytes, and
**−97.9 %** allocations.

The fixtures: *small* is three scalars (37 bytes); *medium* a service
configuration with nested sections, a flow list, an eight-entry map and a
literal block (527 bytes); *large* a 500-record catalogue in block style
(47 927 bytes to decode; 57 927 bytes written, since a record's tags are
written as a block sequence where the document has them in flow style).

## Where the remaining allocations are

- **Marshal: one** — the returned `[]byte`. The encoder writes into a buffer
  from the shared `internal/core/data/codec/scratch` pool and copies the result out
  once, so the large catalogue (57 927 bytes) costs one allocation of the
  output's size where yaml.v3 built a node tree and an event stream (18 435
  allocations, 10.5 MiB). Medium's five more are all its string-keyed map,
  whatever the map's size: the keys, the values (an array and the slice
  reflection makes of it), one key holder and the sort order.
- **UnmarshalStruct**: the parser and its node arena come from a pool, the
  input is copied ONCE into a string and every scalar is a slice of that copy
  unless an escape or a fold rewrites it, and a struct's tag plan is computed
  once per type — what remains is the target's own storage: one `[]string` per
  record's tags and the records' slice, so 504 for 500 records. (A decoded
  string therefore keeps the document's text alive while the program holds
  it: a few kilobytes for a configuration; for a large document read for one
  field, `strings.Clone` the field.)
- **UnmarshalAny**: the untyped tree is allocations by construction — every
  `map[string]any`, every `[]any`, and every non-pointer value boxed into an
  `any` — about nine per record, against yaml.v3's hundred.

The `B/op` of the two `large` decodes moves by a few percent between samples
(±7 % and ±3 %): the pooled parser is sometimes dropped by a garbage
collection and its arena allocated again.

## Allocation ceilings

`codec_integration_test.go` (`//go:build !race`, run by the alloc lane) pins
the per-call ceilings over a three-entry `map[string]int`: Marshal 8, Unmarshal
9, Append 7 — measured at 6, 7 and 5, where yaml.v3 needed 32, 60 and 31.
