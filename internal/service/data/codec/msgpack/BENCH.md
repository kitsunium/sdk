<!-- generated from internal/service/data/codec/msgpack/msgpack_bench_test.go — run `go test -run='^$' -bench=. -benchmem -count=10 ./internal/service/data/codec/msgpack/` from the repository root to refresh the native column; the vendor column needs the procedure in "How this was measured" -->
# Benchmarks — `internal/service/data/codec/msgpack`

The suite drives the codec only through the `core/data/codec` contract —
`Marshal`, `Append`, `Unmarshal`, `NewEncoder`, `NewDecoder` — so the same file
measures whichever implementation sits behind `msgpack.New()`. It landed while
the codec was still backed by `github.com/vmihailenco/msgpack/v5` v5.4.1, was
run against that first, and then against the native implementation that
replaced it. This report is that comparison.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro (10 cores) |
| RAM | 16 GiB |
| OS | macOS 26.6.2 (Darwin 25.6.0), arm64 |
| Go toolchain | go1.27.1 darwin/arm64 |
| Vendor binary | `go test -c` at df4ddb0e (vendor-backed codec, this benchmark file) |
| Native binary | `go test -c` at 23fa0431 for every row but StreamDecode — the commits after it change only the stream decoder and a struct-layout path no benchmark reaches; StreamDecode at 9cd0b0b6 (in-place decoding), measured in its own session |
| Generated | 2026-10-03 |
| Machine load | 1-minute load average between 28 and 84 throughout — other builds and a fuzzer shared the machine |

## How this was measured

Both test binaries were built once and run **interleaved**: vendor pass, native
pass, ten times over, each pass `-test.count=1 -test.benchtime=1s
-test.benchmem`. Load that came and went therefore fell on both columns alike.
`benchstat` reports the median of the ten, the change, and the Mann-Whitney
p-value; `~` marks a change it cannot tell from noise at p < 0.05. The
confidence intervals are wide (30–130 %) because of the load, so read a `~` as
"not shown to differ", not as "equal".

To reproduce the vendor column, build its binary from the commit that still
had the vendor:

```
git worktree add /tmp/msgpack-vendor df4ddb0e
(cd /tmp/msgpack-vendor/internal/service/data/codec/msgpack && go test -c -o /tmp/old.test .)
go test -c -o /tmp/new.test ./internal/service/data/codec/msgpack/
cd internal/service/data/codec/msgpack
for i in $(seq 10); do
  /tmp/old.test -test.run '^$' -test.bench . -test.benchmem -test.count 1 >> /tmp/old.txt
  /tmp/new.test -test.run '^$' -test.bench . -test.benchmem -test.count 1 >> /tmp/new.txt
done
benchstat /tmp/old.txt /tmp/new.txt
```

## Payloads

`benchRecord` mirrors `pkg/v1/data/codec`'s `complexRT` fixture — one field of every
family the codec writes (bools, every integer width, floats, strings, bytes,
slices, string-keyed maps, nested structs by value, pointer and slice, a
timestamp). `small` is the record itself, `medium` and `large` multiply its
collections by 100 and 1 000.

| payload | encoded |
|---|---:|
| small | 611 B |
| medium | 11 753 B |
| large | 122 085 B |

`StreamEncode` / `StreamDecode` write and read back 64 `small` records per
iteration through one Encoder / Decoder. Every `Marshal` / `Append` /
`StreamEncode` operation also pays one allocation of 400 B in the HARNESS, for
boxing the record into `any` — in both columns.

## Native against vendor

| benchmark | vendor | native | Δ time | p | B/op vendor → native | allocs vendor → native |
|---|---:|---:|---:|---|---:|---:|
| Marshal/small | 2.66 µs | 2.17 µs | −18.6 % | 0.011 | 1 169 → 1 040 | 13 → 4 |
| Marshal/medium | 82.0 µs | 42.7 µs | −47.9 % | 0.004 | 25 650 → 12 704 | 513 → 4 |
| Marshal/large | 817 µs | 531 µs | −35.1 % | 0.035 | 252 194 → 124 368 | 5 013 → 4 |
| Append/small | 3.46 µs | 1.78 µs | −48.6 % | 0.004 | 528 → 400 | 12 → 3 |
| Append/medium | 89.8 µs | 32.0 µs | −64.4 % | 0.005 | 13 346 → 400 | 512 → 3 |
| Append/large | 781 µs | 361 µs | ~ | 0.052 | 128 642 → 400 | 5 012 → 3 |
| Unmarshal/small | 9.39 µs | 5.48 µs | −41.6 % | 0.029 | 3 387 → 3 152 | 62 → 53 |
| Unmarshal/medium | 238 µs | 135 µs | ~ | 0.052 | 82 689 → 68 546 | 1 556 → 1 347 |
| Unmarshal/large | 2.21 ms | 1.15 ms | −47.9 % | 0.007 | 888 448 → 754 109 | 15 060 → 13 051 |
| UnmarshalAny/small | 13.7 µs | 12.2 µs | ~ | 0.353 | 5 780 → 5 800 | 136 → 136 |
| UnmarshalAny/medium | 299 µs | 215 µs | ~ | 0.529 | 149 002 → 148 938 | 3 527 → 3 527 |
| UnmarshalAny/large | 1.72 ms | 1.77 ms | ~ | 0.684 | 1 526 293 → 1 525 636 | 35 620 → 35 619 |
| StreamEncode (64 × small) | 179 µs | 110 µs | −38.5 % | 0.000 | 33 944 → 25 636 | 772 → 193 |
| StreamDecode (64 × small) | 592 µs | 524 µs | ~ | 0.052 | 221 404 → 206 574 | 3 976 → 3 398 |

Geomean time over the first thirteen rows: **−36.8 %** (120 µs → 75.9 µs in
that session). An earlier interleaved session, run before the stream changes,
agreed on every direction and gave −36.0 %. The StreamDecode row comes from a
third, focused session interleaving three binaries — vendor, framed-only
native (23fa0431) and in-place native (9cd0b0b6) — ten rounds each: 592 µs,
586 µs and 524 µs. A full-table session run while this machine also ran this
track's own vet and test sweeps is not reported: its intervals were too wide
to separate anything.

## Reading the numbers

**Encoding no longer allocates per element.** The vendor allocated once per
entry of the record's `map[string]benchInner` (13 → 513 → 5 013 as the payload
grows); the native encoder's count is flat at 4 whatever the size: the
exact-size result, the harness's boxing, and one reusable key and one reusable
value for the one map that takes the reflective path. `Append` drops to those
last three and its B/op to the harness's 400: it encodes straight onto the
caller's buffer. Struct keys are appended pre-encoded and reflection runs once
per type, which is where the time goes: 19–64 % faster wherever benchstat could
tell.

**Typed decoding is faster or level, with fewer allocations** — −13 % objects
and −15 % bytes at large, every remaining allocation being a string, slice or
map the result owns. Declared lengths are checked against the input before
anything is reserved; that check is a comparison and does not show here.

**Untyped decoding is level, by construction.** It produces the vendor's Go
types (`int8` for a fixint, `map[string]any`, …) — pinned by the golden file —
and boxing each such value into an interface costs the same allocations in
either implementation: 136 / 3 527 / 35 619 against 136 / 3 527 / 35 620.

**Streams.** Encoding is 38 % faster and writes one `Write` per value. The
first native stream decoder framed every value — copied its bytes into a pooled
buffer, header by header — before decoding it exactly as `Unmarshal` does, and
a CPU profile put that framing at about 30 % of the stream decode: level with
the vendor (586 µs against 592 µs). A value the 4 KiB read-ahead already holds
whole is now decoded where it lies after a non-allocating extent scan, which
brings the row to 524 µs (−11 %, p = 0.052 under this load), with 15 % fewer
allocations than the vendor; a longer or straddling value is still framed.

What the table does not show is what each implementation does with HOSTILE
input: five bytes (`dd ff ff ff ff`) made the vendor size a four-billion-element
slice; the native decoder refuses them before allocating. That is bounded by
construction (CLAUDE.md §Bounds) and tested, not benchmarked.

## Vendor-backed codec, first run (before the replacement)

Recorded alone, with `-count=10`, when the harness landed. Absolute numbers
differ from the interleaved run above because the load did; the allocation
columns are identical.

| benchmark | time/op | B/op | allocs/op |
|---|---:|---:|---:|
| Marshal/small | 2.43 µs | 1 169 | 13 |
| Marshal/medium | 66.6 µs | 25 648 | 513 |
| Marshal/large | 652 µs | 252 394 | 5 013 |
| Append/small | 2.38 µs | 528 | 12 |
| Append/medium | 61.5 µs | 13 343 | 512 |
| Append/large | 595 µs | 128 640 | 5 012 |
| Unmarshal/small | 5.23 µs | 3 387 | 62 |
| Unmarshal/medium | 119 µs | 82 691 | 1 556 |
| Unmarshal/large | 1.23 ms | 888 453 | 15 060 |
| UnmarshalAny/small | 6.36 µs | 5 780 | 136 |
| UnmarshalAny/medium | 127 µs | 149 001 | 3 527 |
| UnmarshalAny/large | 1.15 ms | 1 526 299 | 35 619 |
| StreamEncode (64 × small) | 276 µs | 33 945 | 772 |
| StreamDecode (64 × small) | 349 µs | 221 385 | 3 976 |
