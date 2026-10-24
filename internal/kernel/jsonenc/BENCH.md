<!-- generated from internal/kernel/jsonenc/bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 -benchtime=50ms ./jsonenc/` to refresh; not `make bench`, which regenerates the codec's report only. The emitter table is a manual section: two test binaries (this branch and origin/main with only the benchmark files copied in) run alternately, 8 rounds each, the minimum of the rounds kept -->
# Benchmarks — `internal/kernel/jsonenc`

The one JSON string escaper, against what the SDK's emitters carried before it
(a byte loop that copies runs) and against `encoding/json`. Two questions: **does
it beat the byte loop on the strings the emitters really write**, and **what does
UTF-8 validation cost** where the loop did none.

## Reproducibility envelope

> **These were taken on a busy machine** (load average 17 to 69 on 10 cores,
> other agents compiling), so every figure is the MINIMUM over the rounds, which
> is the least-disturbed one, and only the ratios between rows measured in the
> same process (or interleaved, for the emitter table) mean anything. Medians
> were 1.1 to 4 times the minimum. No x86-64 machine was available: the
> word search is `swar`'s SWAR form (no AVX2), measured on arm64 only. Re-run
> on the target before reading a ratio as a promise.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.3 darwin/arm64 |
| Git branch         | `feat/kernel-jsonenc` |
| Git commit         | base `6f89b0852` (uncommitted when measured) |
| Generated (UTC)    | 2026-10-24 |
| Bench wall-clock   | `-benchtime=40ms` to `100ms`, 8 rounds, minimum kept |

## Results — `AppendString`, ns/op, by input length in bytes

`jsonenc` is this package; `loop` is the run-copying byte loop the logger
encoder carried (no UTF-8 validation, so no `unicode` row); `stdlib` is
`json.Marshal` appended to the destination. `plain` is ASCII with nothing to
escape; `escaped` has a quote in every fourth byte (a dense, adversarial shape);
`unicode` is `aé東` repeated, valid, nothing to escape.

```
                 L8     L32    L128   L1024
plain   jsonenc   9.1   13.4   33.8   218.0
        loop      8.3   24.6   96.9   744.2
        stdlib  110    154    233   1007
escaped jsonenc  24.1   73.2  254    1945
        loop     18.6   52.4  194    1469
        stdlib  160    189    440    2725
unicode jsonenc  21.2   51.2  164    1141
        stdlib  135    174    355    1922
```

### Reading it

- **Plain text is what the package is for**: from 32 bytes the word search is
  1.8 to 3.4 times the loop (L32 to L1024), and at 8 bytes it is within a
  nanosecond of it — the byte path under 32 bytes is a table lookup per byte.
- **Dense escapes are 30 to 40 % slower than the loop** and still 1.4 to 6.6
  times faster than `json.Marshal` (the margin narrows with length). This is the
  shape the package gives up for the plain-text win, and it is bounded: a window
  with two hits keeps the byte loop reading (`scalarWindow`, `dense`) instead of
  paying a search call per hit, which on this input cost about 50 % over the loop.
  The 30 % that remains is the escape itself going through `appendEscapedByte`
  and a slice append per hit, where the loop's switch was inlined by hand into
  one function the lint's complexity cap (8) no longer allows. Under 32 bytes a
  table lookup per byte replaced the search call, which had made every short
  plain string 5 ns slower than the loop.
- **Validation is paid for non-ASCII only**: valid two- and three-byte runes
  are read in place, no decode call; four-byte runes, E0, ED and bad bytes are
  decoded. The loop validated nothing, which is what left invalid UTF-8 in the
  logger's output.

## Results — the migrated emitters, before and after

Each row: the emitter's own benchmark, the origin/main binary and this branch's
alternated, minimum of 8 rounds, ns/op. All rows 0 allocs/op except where
stated; allocations are unchanged unless noted.

```
logger/encoder (whole record, four attributes where stated)
                                 before    after    delta
  JSON_Attrs4                     185.3    167.0    -9.9 %
  JSON_EscapePlain                179.6    161.8    -9.9 %     4 x 26-byte plain
  JSON_EscapeQuoted               249.2    269.5    +8.1 %     4 x 26 bytes, 6 quotes
  JSON_EscapeControl              227.4    270.4   +18.9 %     4 x control bytes
  JSON_EscapeLong                 664.6    377.3   -43.2 %     4 x 192 bytes, 2 escapes
  JSON_EscapeUnicode              283.2    429.6   +51.7 %     4 x 48 bytes non-ASCII, valid
otlp.AppendString
  short (20 B)                     23.3     13.7   -41.2 %
  long (190 B, 2 escapes)         210.5     65.7   -68.8 %
  unicode (48 B)                   94.8     81.4   -14.1 %
token.quoteJSONString
  short (5 B)                      27.1     19.1   -29.5 %     1 alloc, unchanged
  issuer (42 B)                   125.7     37.8   -69.9 %     1 alloc, unchanged
  long (204 B)                    523.9    156.7   -70.1 %     1 alloc before, 2 after
redact.JSON (whole document)
  short                          1233.0    969.3   -21.4 %
  unicode                        1945.0   1627.0   -16.3 %
  long                          10487.0   5815.0   -44.6 %
```

The two regressions are the declared ones: **the logger's non-ASCII strings pay
for the validation it never did** (+52 % on a record of four 48-byte values,
about 36 ns a value), and control and quote bytes take a call per hit (+19 % and +8 % on four
values full of them). `token.quoteJSONString` allocates once more past its 128-byte
stack buffer, on a string no claim reaches today. `redact` and the logger's
other rows are noise-bounded: the loaded machine moved Attrs4 and EscapePlain,
whose strings are plain, by 30 % between two runs of the same binary.

## What the figures do not settle

- **x86-64.** Only the SWAR form of `swar` was measured, on arm64. The first
  match is a `TrailingZeros` there too, but the crossover between the byte path
  and the search (`searchMin = 32`) was set on this machine.
- **`Object`**: 79 ns for a five-member object against 743 ns for `json.Marshal`
  of the same struct — a comparison with reflection, not with another builder.
