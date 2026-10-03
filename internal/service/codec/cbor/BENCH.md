<!-- measured from internal/service/codec/cbor/codec_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 ./codec/cbor/` to refresh -->
# Benchmarks — `internal/service/codec/cbor`

This codec used to wrap `github.com/fxamacker/cbor/v2`. It is now written on
the standard library alone, and this page answers the question that change
raises: what did it cost, and what did it save? It also says how the answer was
obtained, because the machine was not quiet when it was.

The short answer: **at parity or faster on every shape but two**, and both of
the two are explained — a map now has to be sorted, and a two-field message
pays for a validation pass that refuses a bad document before the target is
touched. **Allocations fell or held on every benchmark**, and encoding a
thousand records in one message went from 1 002 allocations to 2.

## How it was measured

- **Old** is the adapter as it stood at `390aa80f`, copied verbatim into a
  throwaway module beside this package — its two error codes moved out of
  range and its registration dropped, so that both implementations link into
  one binary. **New** is this package at `b6609749`. The fixtures are the ones
  in `codec_bench_test.go`: `benchSmall`, `benchRecord`, a sixteen-entry
  `map[string]int`, an untyped document, a thousand records, a hundred records
  through one stream.
- The old numbers were taken first: the package benchmarks ran on the
  fxamacker adapter, unchanged, before any of this was written.
- **The figure is CPU time, not wall time.** The machine was shared with
  concurrent builds throughout — load averages between 17 and 94 on ten
  cores — and wall-clock runs of the same benchmarks scattered by as much as
  ±150 %, which makes a comparison meaningless. So each operation was timed
  by `getrusage(RUSAGE_SELF)` (user + system), under `GOMAXPROCS=1`, over 15
  rounds alternating old and new with a GC before each; the table gives the
  median, and the interquartile range in brackets. CPU time is far less
  sensitive to a loaded machine but not immune to it — caches and clock
  frequency still move — so **a difference under about 5 % is noise**.

## Old against new

| benchmark | fxamacker ns/op | native ns/op | native / old | allocs/op | B/op |
|---|---:|---:|---:|---|---|
| `MarshalSmall` | 104 [99..107] | 104 [102..111] | **1.01** | 1 → 1 | 16 → 16 |
| `UnmarshalSmall` | 217 [211..227] | 268 [255..277] | **1.23** | 2 → 2 | 27 → 27 |
| `MarshalRecord` | 578 [555..613] | 539 [489..572] | 0.93 | 1 → 1 | 176 → 176 |
| `AppendRecord` | 546 [538..553] | 472 [469..489] | 0.86 | 1 → 1 | 144 → 144 |
| `UnmarshalRecord` | 1 566 [1 552..1 598] | 1 517 [1 503..1 542] | 0.97 | 19 → **16** | 696 → 640 |
| `MarshalMap16` | 903 [872..942] | 1 397 [1 364..1 469] | **1.55** | 1 → 1 | 128 → 128 |
| `UnmarshalMap16` | 2 521 [2 452..2 564] | 2 481 [2 410..2 590] | 0.98 | 23 → **21** | 1 120 → 1 088 |
| `MarshalDocAny` | 999 [912..1 087] | 548 [502..596] | **0.55** | 1 → 1 | 80 → 80 |
| `UnmarshalDocAny` | 1 900 [1 781..2 019] | 1 648 [1 578..1 727] | 0.87 | 32 → 32 | 1 032 → 1 032 |
| `MarshalRecords1000` | 602 740 [572 850..631 490] | 506 910 [490 850..530 580] | 0.84 | 1 002 → **1.4** | 201 320 → 178 913 |
| `UnmarshalRecords1000` | 1 811 050 [1 763 350..1 954 750] | 1 696 800 [1 637 425..1 776 850] | 0.94 | 17 993 → **14 994** | 694 130 → 642 549 |
| `StreamEncode` | 63 880 [57 095..67 410] | 57 352 [53 972..59 300] | 0.90 | 102 → 101 | 14 477 → 14 418 |
| `StreamDecode` | 200 525 [187 432..207 930] | 197 938 [176 762..208 560] | 0.99 | 1 895 → **1 594** | 71 888 → 66 101 |

The allocation columns are the harness's, which hands each value to the codec
already boxed. `-benchmem` on the package benchmarks also counts the boxing of
the argument and of the result, so a few rows read higher there —
`MarshalRecord` 2 → 2, `UnmarshalRecord` 20 → 17, `MarshalRecords1000`
1 002 → 2 — and the difference between old and new is the same in every row.

## A sorted map costs what fxamacker charges for one

`MarshalMap16` is 1.55× slower, and the difference is the order. fxamacker
wrote a map's pairs in Go's iteration order, which changes from one run to the
next; the native codec writes them in the bytewise order of their encoded keys
(RFC 8949 §4.2.1), so equal values give equal bytes. Asked for that same order
— `SortBytewiseLexical`, measured the same way — fxamacker takes the same time:

| benchmark | fxamacker, sorted | native | native / sorted | allocs/op |
|---|---:|---:|---:|---|
| `MarshalMap16` | 1 447 | 1 434 | **0.99** | 2 → 1 |
| `MarshalDocAny` | 1 569 | 569 | 0.36 | 5 → 1 |
| `MarshalRecord` | 745 | 562 | 0.75 | 2 → 1 |
| `MarshalRecords1000` | 698 600 | 506 830 | 0.73 | 2 002 → 1.4 |

The two `Map16` outputs are byte-identical. The record rows differ in bytes as
well as time: in that mode fxamacker sorts struct fields too, and the native
codec keeps them in declaration order, as fxamacker's default did. A profile of
the native `MarshalMap16` puts about a fifth of its time in the ordering itself
— sorting the pairs' spans by 8-byte key prefixes, then copying the pairs back
in order — and the rest in walking the map by reflection and encoding the
pairs.

## A two-field message pays for the validator

`UnmarshalSmall` is 1.23× slower: 268 ns against 217. The document is 19
bytes, and the native decoder reads it twice — once to accept exactly one
well-formed, valid item within the bounds, before anything is allocated or the
caller's value is touched, and once to decode it. That first pass costs about
80 ns here, measured on its own, and about 360 ns for a six-field record
carrying a three-string slice and a three-pair map. fxamacker also runs a
well-formedness pass before it decodes, but leaves validity — UTF-8, the
content of tags 0 to 3 — to the decoding, so a value it skips is never checked;
the native pass checks the whole item. The validator was trimmed once already:
a frame went from 32 bytes to 16, because every `Unmarshal` zeroes a stack of
33 of them, and the calls a head does not need are skipped — 99 → 80 ns and
400 → 361 ns. What remains is the fixed price of refusing a whole document up
front, and from one `benchRecord` upward the native decoder is at parity or
faster overall (`UnmarshalRecord` 0.97, `UnmarshalDocAny` 0.87).

## Refreshing these numbers

```
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 ./codec/cbor/
```

Against fxamacker: check out `390aa80f`, run the same command, keep its output,
run it again here, and compare with `benchstat old.txt new.txt` — on a quiet
machine. On a busy one, measure CPU time as described above, or the comparison
says more about the neighbours than about the codec.

Measured on an Apple M1 Pro (10 cores), 16 GiB, macOS 26.6.2 (25G83),
go1.27.1 darwin/arm64, on 2026-10-03 between 02:14 and 02:34 CEST; old at
`390aa80f`, new at `b6609749`.
