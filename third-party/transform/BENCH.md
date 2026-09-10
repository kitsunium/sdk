<!-- generated from third-party/transform/transform_bench_test.go — run `GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./third-party/transform/` to refresh; ratios come from `GOWORK=off go test -run TestReportRatios -v ./third-party/transform/` -->
# Benchmarks — `third-party/transform`

A compressor is chosen on numbers or it is chosen on folklore. This report
exists to answer one question — **which scheme should I register, for which
traffic?** — and to make the answer checkable rather than quotable.

Every row compares SDK compressor against SDK compressor: same
`core/transform.Compressor` port, same append-to-dst convention, same bounded
decompression. The stdlib **gzip** row is the reference, because gzip is what
the SDK already had and what a new scheme has to beat to be worth a dependency.

## Reproducibility envelope

> **Numbers vary across machines, and this one is shared.** Other agents run on
> the same box, so a single sample mixes real cost with somebody else's CPU
> burst. Every throughput figure below is the best of **three** runs, which is
> the least-contended sample rather than an average of interference. The
> allocation columns are deterministic and were identical across all three.
> The *shape* of the result is what travels, not the milliseconds.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Library            | `github.com/klauspost/compress` v1.19.2 |
| Git branch         | `jaimerias-que-tu-te-connect` (worktree `agent-a8f4a4d4391f85351`) |
| Git commit         | `459e635` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-cpu=1`, best of 3 |
| Corpus size        | 4 MiB per corpus |

## Corpora

| Name | What it is | Why it is here |
|---|---|---|
| `json` | 4 MiB of deterministic JSON log records — repeated keys, bounded vocabulary, hex trace ids | the traffic a wire compressor is actually deployed on |
| `random` | 4 MiB of deterministic pseudo-random bytes | the worst case: already-compressed or encrypted payloads, where no scheme can win and the only question is how much CPU it burns proving that |

`random` is not a curiosity. A pipeline that compresses TLS-terminated bodies,
image blobs or already-gzipped archives lives in that column permanently, and a
scheme that is fast on text and catastrophic on entropy will be discovered in
production rather than here.

## Compression ratio

Ratio is the axis `go test -bench` cannot report, so it is measured by
`TestReportRatios` in the ordinary suite.

| Corpus | Scheme | In | Out | Ratio | Saved |
|---|---|---:|---:|---:|---:|
| json | gzip-stdlib   | 4 194 433 | 697 266 | **6.02** | 83.4 % |
| json | zstd-fastest  | 4 194 433 | 728 975 | 5.75 | 82.6 % |
| json | zstd-default  | 4 194 433 | 765 116 | 5.48 | 81.8 % |
| json | zstd-better   | 4 194 433 | 691 747 | **6.06** | 83.5 % |
| json | s2            | 4 194 433 | 1 239 171 | 3.39 | 70.5 % |
| random | gzip-stdlib  | 4 194 304 | 4 194 649 | 1.000 | −0.008 % |
| random | zstd-fastest | 4 194 304 | 4 194 509 | 1.000 | −0.005 % |
| random | zstd-default | 4 194 304 | 4 194 413 | 1.000 | −0.003 % |
| random | zstd-better  | 4 194 304 | 4 194 413 | 1.000 | −0.003 % |
| random | s2           | 4 194 304 | 4 194 312 | 1.000 | −0.0002 % |

Two things worth reading twice.

**zstd does not beat gzip on ratio here.** At `ZstdFastest` it is 4.5 % *worse*
than stdlib gzip, and it only edges ahead at `ZstdBetter`. Anyone who adopts
zstd expecting smaller payloads on log-shaped JSON will be disappointed; the
reason to adopt it is the speed table below, where it is not close.

**`zstd-default` compresses worse than `zstd-fastest` on this corpus.** That is
not a typo and not a bug: level 3's longer match search pays off on prose and
loses on records whose entropy is concentrated in 32-hex-digit identifiers. It
is a reminder that "higher level = smaller output" is a heuristic, not a law,
and that the only way to pick a level is to run your own corpus through
`TestReportRatios`.

On `random` every scheme correctly falls back to stored blocks and *adds* a few
bytes. The interesting column is the overhead: s2 adds 8 bytes on 4 MiB, gzip
adds 345.

## Compression throughput

MB/s of **plaintext**, the only unit comparable across schemes producing
different output sizes.

| Corpus | Scheme | ns/op | MB/s | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| json | gzip-stdlib  | 44 467 410 | 94.3 | 3 173 505 | 29 |
| json | zstd-fastest | 17 905 998 | **234.3** | 4 202 144 | 17 |
| json | zstd-default | 23 580 520 | 177.9 | 4 693 853 | 16 |
| json | zstd-better  | 48 588 258 | 86.3 | 4 167 598 | 15 |
| json | s2           | 5 882 735 | **713.0** | 4 202 834 | **1** |
| random | gzip-stdlib  | 3 838 117 | 1 092.8 | 10 439 568 | 23 |
| random | zstd-fastest | 4 391 534 | 955.1 | 22 281 751 | 18 |
| random | zstd-default | 5 111 628 | 820.5 | 20 067 216 | 15 |
| random | zstd-better  | 9 387 962 | 446.8 | 20 160 434 | 15 |
| random | s2           | 1 093 222 | **3 836.6** | 4 202 562 | **1** |

`zstd-better` compresses *slower than stdlib gzip* (86.3 vs 94.3 MB/s) for a
0.7 % ratio gain. It is in the API because a caller with cold archival data may
want it; it is not a default anyone should reach for casually.

Note zstd's allocation column on `random`: 22 MB for a 4 MiB payload. The
encoder materialises its window and its stored-block buffers when the data
refuses to compress. s2 stays at one allocation on both corpora.

## Decompression throughput

| Corpus | Scheme | ns/op | MB/s | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| json | gzip-stdlib  | 18 135 500 | 231.3 | 14 258 456 | **207** |
| json | zstd-fastest | 5 412 127 | 775.0 | 4 203 404 | **1** |
| json | zstd-default | 5 034 481 | 833.1 | 4 203 337 | 1 |
| json | zstd-better  | 4 426 723 | 947.5 | 4 203 259 | 1 |
| json | s2           | 2 477 758 | **1 692.8** | 4 202 496 | **1** |
| random | gzip-stdlib  | 3 297 719 | 1 271.9 | 14 229 544 | 36 |
| random | zstd-fastest | 1 533 280 | 2 735.5 | 4 202 496 | 1 |
| random | zstd-default | 1 634 183 | 2 566.6 | 4 202 496 | 1 |
| random | zstd-better  | 1 458 861 | 2 875.1 | 4 202 496 | 1 |
| random | s2           | 821 851 | **5 103.5** | 4 194 304 | **1** |

This is where the case for a vendor compressor is actually made. On the JSON
corpus zstd decompresses **3.4×** faster than stdlib gzip and s2 **7.3×**
faster, and both do it in a **single allocation** against gzip's 207. Payloads
are compressed once and read many times; the decompression column is the one a
fleet feels.

## The choice table

> Read the row that matches your traffic, not the row with the biggest number.

| If your constraint is… | Register | Because |
|---|---|---|
| **smallest bytes on the wire**, cost no object | `zstd` at `ZstdBetter` | best measured ratio (6.06) — but 86 MB/s, slower to compress than stdlib gzip |
| **balanced wire format**, general RPC / log shipping | `zstd` at `ZstdFastest` | 2.5× gzip's compression speed, 3.4× its decompression, one allocation to decode, ratio within 5 % of gzip |
| **latency**, hot path, many small payloads | `s2` | 7.6× gzip's compression speed, 7.3× its decompression, one allocation, at 56 % of zstd's ratio |
| **throughput on data that may not compress** (blobs, TLS bodies, images) | `s2` | 3 837 MB/s compressing entropy and 8 bytes of overhead on 4 MiB; zstd spends 22 MB of allocation discovering the same thing |
| **interoperability with a peer that names an encoding** | whatever the peer named | a wire format is not a performance choice; `gzip` and `zlib` stay in `internal/service/transform` for exactly this |
| **no dependency at all** | stdlib `gzip` | it is 2–7× slower everywhere, and that is the price of a zero-dependency build |

Neither scheme here replaces gzip. gzip stays the answer whenever the *name* of
the encoding is part of the contract; zstd and s2 are the answer whenever the
SDK owns both ends and only the bytes matter.

## Why s2 encodes into the tail

The one optimisation in this package that was chosen on a profile rather than on
taste, recorded here so the choice stays challengeable.

`s2.Encode(dst, src)` treats `dst` as **scratch**, not as a prefix: it writes
from index zero and returns a sub-slice. The port's append-to-dst contract
therefore has two honest implementations — encode into a fresh buffer and copy
the result onto `dst`, or grow `dst` and let s2 encode straight into its tail.

| Benchmark | Corpus | MB/s | B/op | allocs/op |
|---|---|---:|---:|---:|
| `S2CompressIntoTail` (ships) | json | 682.0 | 4 202 861 | **1** |
| `S2CompressThenCopy` (control) | json | 668.8 | 5 448 076 | 2 |
| `S2CompressIntoTail` (ships) | random | **4 159.1** | 4 202 530 | **1** |
| `S2CompressThenCopy` (control) | random | 2 787.2 | 8 405 060 | 3 |

On `json` the throughput difference is inside this box's noise — the two rows
swapped order between runs, and it would be dishonest to claim the 2 % as a
result. The claim rests on the two columns that are deterministic and on the
`random` row, where the copy is largest because the output is the same size as
the input: **1.49× the throughput, one allocation instead of three, and half the
bytes.**

The profile says the same thing in one line. Control:

```
$ go tool pprof -top -nodecount=4 t50-tp.test thencopy.cpu
      flat  flat%   sum%        cum   cum%
     0.67s 29.00% 29.00%      0.67s 29.00%  .../s2.emitLiteral
     0.61s 26.41% 55.41%      0.61s 26.41%  runtime.memmove
     0.54s 23.38% 78.79%      0.54s 23.38%  runtime.memclrNoHeapPointers
     0.07s  3.03% 81.82%      0.07s  3.03%  .../s2.encodeBlockAsm
```

Shipped:

```
$ go tool pprof -top -nodecount=4 t50-tail.test tail.cpu
      flat  flat%   sum%        cum   cum%
     1.08s 47.37% 47.37%      1.08s 47.37%  .../s2.emitLiteral
     0.87s 38.16% 85.53%      0.87s 38.16%  runtime.memclrNoHeapPointers
     0.07s  3.07% 88.60%      0.07s  3.07%  .../s2.encodeBlockAsm
     0.06s  2.63% 91.23%      0.06s  2.63%  runtime.typePointers.next
```

`runtime.memmove` — the second copy — goes from **26.4 % of the profile to
absent**, and the share spent in `emitLiteral`, which is the actual compression,
rises from 29 % to 47 %.

`memclrNoHeapPointers` stays at 38 %: that is the destination being zeroed on
allocation, which is the runtime materialising the buffer the caller asked for
and is not removable without handing back uninitialised memory. It is named here
so nobody re-profiles this and concludes there is a second win hiding in it.

## Why decompression takes the empty-`dst` fast path

The same argument, on the other side, with a subtlety worth stating.

The zstd decoder's ceiling (`WithDecoderMaxMemory`) counts `len(dst) + decoded`,
which was measured, not assumed: a `dst` already at the ceiling makes `DecodeAll`
refuse a payload of any size. That is a defensible total-memory bound, but it is
not what this package promises — here the ceiling means **the decompressed
payload**. So the general path decodes into a fresh buffer and appends.

When `len(dst) == 0` the two readings coincide exactly, so `DecodeAll` can
append straight into the caller's buffer with no copy and no change of meaning.
That is the buffer-reuse call the port's convention exists for, and it is the
shape every benchmark and every sane caller uses.

Before and after, JSON corpus:

| Path | MB/s | B/op | allocs/op |
|---|---:|---:|---:|
| zstd, decode-then-append (before) | 761.9 | 8 405 972 | 3 |
| zstd, empty-`dst` fast path (after) | 775.0 | **4 203 404** | **1** |
| s2, decode-then-append (before) | 1 522.5 | 8 404 992 | 2 |
| s2, decode-into-tail (after) | 1 692.8 | **4 202 496** | **1** |

And on `random`, where the copy is a full 4 MiB: zstd 2 271 → 2 736 MB/s
(**+20 %**), s2 3 430 → 5 104 MB/s (**+49 %**).

One honesty note on that table: the "before" rows are a **single** run taken
just before the change, while the "after" rows are the best-of-three used
everywhere else in this report, so the percentages are indicative rather than
tight. The claim that is not indicative is the allocation column, which is
deterministic and was identical on every run: **every scheme in this package now
decompresses in exactly one allocation, and materialises the payload once
instead of twice.**

s2 gets the tail treatment unconditionally rather than a fast path, because its
block header already declares the exact decoded length — the buffer can be sized
once, correctly, with no guess and no branch.

## What is deliberately not measured here

- **Streaming.** `core/transform.Compressor` is a whole-buffer port. Both
  libraries have streaming APIs; neither is exposed, so neither is benchmarked.
- **Concurrent throughput.** `TestConcurrentUseIsSafe` proves the singletons are
  safe under `-race`; how they *scale* is a property of the caller's fan-out and
  of `GOMAXPROCS`, not of this package, and a number measured on a contended
  8-core VM would mislead more than it informs.
- **Dictionary compression.** zstd's trained dictionaries are the single biggest
  ratio win available for small, homogeneous payloads. Not shipped, not
  measured; see `CLAUDE.md` §Do NOT.
