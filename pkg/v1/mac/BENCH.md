<!-- generated from pkg/v1/mac/mac_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=5 ./v1/mac/` to refresh -->
# Benchmarks — `pkg/v1/mac`

HMAC-SHA256, and the one question this package exists to answer with a number:
**what does authentication add over hashing the same bytes?** Both halves are
measured here, in one run on one machine, so the comparison is not assembled
from two pages. The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md).

## The choice table

| | 64 B | 4 KiB | 1 MiB | throughput @1 MiB |
|---|---:|---:|---:|---:|
| `Tag` (HMAC-SHA256) | 994 ns | 3 817 ns | 743 µs | **1 346 MiB/s** |
| `Verify` | 1 055 ns | 3 909 ns | — | — |
| bare SHA-256, same bytes | **155 ns** | — | **741 µs** | 1 348 MiB/s |

Every call is **7 allocations / 544 B**, at every size.

## What authentication costs: everything at 64 B, nothing at 1 MiB

| | `Tag` | bare `sha256.Sum256` | ratio |
|---|---:|---:|---:|
| 64 B | 994 ns | 155 ns | **6.42×** |
| 1 MiB | 743 160 ns | 741 255 ns | **1.0026×** |

**A MAC over a megabyte is free.** HMAC is two extra compression-function calls
around the message hash; at 16 384 blocks those two are 0.01 % of the work, and
the measurement agrees to within a quarter of a percent.

**A MAC over 64 bytes costs 6.4× the hash**, and none of that is the extra
compressions either — it is *constructing the keyed object*. A CPU profile of
`Tag` at 64 bytes puts `fips140/hmac.New` at **45.05 % cumulative** against
`sha256.blockSHANI`'s **18.46 %**: building the HMAC costs 2.4× the hashing it
then performs.

That is the same shape `pkg/v1/crypto/BENCH.md` reports for AES-GCM's key
schedule, and it has the same cause — a byte-slice-in port has nowhere to keep a
prepared object — and the same practical consequence: **tag batches, not
records.** Tagging 16 384 × 64 B costs 16.3 ms; tagging the same megabyte in one
call costs 0.74 ms.

## What the SDK costs over the bare primitive: one allocation

`BenchmarkBareHMAC*` drives `crypto/hmac` directly, constructing the keyed hash
per call exactly as the service scheme does.

| 64 B | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `mac.Tag` | 994 | 544 | **7** |
| bare `hmac.New` + `Write` + `Sum` | 881 | 512 | **6** |

**+113 ns and exactly one allocation of 32 bytes.** An allocation profile names
it without ambiguity: of the seven, `fips140/hmac.New` accounts for three,
`sha256.New` for two, `Digest.Sum` for one, and **`bytes.Clone` — that is
`Key.Bytes()` — for one**. Six of the seven are the stdlib's; the SDK's entire
contribution to the allocation profile is the defensive copy of the key.

That copy is the `Key` type's whole promise (`Bytes` hands a cipher a fresh
copy so the caller cannot mutate the shared backing array, and `Zeroize` on any
copy clears the secret everywhere), so it is **not** proposed for removal here.
It now has a price: **32 bytes and about a hundred nanoseconds per call, and
0.09 % at a megabyte.**

## `Verify` shows no timing signal on a bad tag

| 4 KiB | ns/op | allocs |
|---|---:|---:|
| `Verify`, correct tag | 3 909 | 7 |
| `Verify`, one byte flipped | **3 883** | 7 |

**0.7 % apart, with the wrong tag marginally faster** — which is to say
indistinguishable, across five runs each, and identical in allocations.

Stated precisely, because a benchmark cannot prove constant time: the spread
across those five runs is itself ~0.9 %, so a difference of tens of nanoseconds
would hide inside it. The guarantee is **structural** — `Verify` recomputes the
full tag and routes the comparison through `hmac.Equal`, so there is no branch
on secret data and no early exit on the first mismatching byte, which is exactly
the property the package doc demands and the reason `==` is refused. What the
measurement rules out is the gross failure: an implementation that bailed on the
first bad byte would refuse in the hundreds of nanoseconds rather than 3 883,
and that would be unmissable here.

`Verify` costs 6 % more than `Tag` at 64 bytes (1 055 vs 994 ns) — that is the
constant-time compare of 32 bytes plus the second traversal of the registry, and
it is the only difference between them.

## Reproducibility envelope

> **Numbers vary across machines.** This box is shared with fifteen other agent
> jobs. This package was measured with **five runs** rather than three: an
> earlier three-run block had its entire `VerifyBadTag4KiB` group land inside a
> load spike and reported a bad tag as 56 % *dearer* than a good one, which
> would have been a false and alarming claim. Five runs separate the signal
> from the spike; every figure quoted above is the **fastest of the five**, and
> all five are printed below.
>
> The allocation columns are exact and do not vary. The RATIOS are what this
> page claims.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_2`, `avx2` |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 (`GOAMD64=v1`) |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 94dd6e7 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=5`, machine under load |

Throughput is quoted in **MiB/s**; Go's own `MB/s` column below is decimal
(10⁶ B/s), so the two differ by 1.048576.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/mac
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkTag64B-8             	 1207822	       995.5 ns/op	  64.29 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag64B-8             	 1000000	      1010 ns/op	  63.37 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag64B-8             	 1000000	      1010 ns/op	  63.36 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag64B-8             	 1197124	       998.7 ns/op	  64.09 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag64B-8             	 1205628	       993.7 ns/op	  64.41 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag4KiB-8            	  304730	      3889 ns/op	1053.22 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag4KiB-8            	  299662	      3846 ns/op	1064.98 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag4KiB-8            	  303438	      3837 ns/op	1067.61 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag4KiB-8            	  300667	      3817 ns/op	1073.02 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag4KiB-8            	  310255	      3861 ns/op	1060.82 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag1MiB-8            	    1617	    748456 ns/op	1400.99 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag1MiB-8            	    1617	    743166 ns/op	1410.96 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag1MiB-8            	    1614	    744119 ns/op	1409.15 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag1MiB-8            	    1618	    743160 ns/op	1410.97 MB/s	     544 B/op	       7 allocs/op
BenchmarkTag1MiB-8            	    1610	    746457 ns/op	1404.74 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify64B-8          	 1000000	      1066 ns/op	  60.01 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify64B-8          	 1000000	      1055 ns/op	  60.64 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify64B-8          	 1000000	      1057 ns/op	  60.54 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify64B-8          	 1000000	      1057 ns/op	  60.57 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify64B-8          	 1000000	      1062 ns/op	  60.26 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify4KiB-8         	  300308	      3909 ns/op	1047.97 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify4KiB-8         	  303735	      3924 ns/op	1043.82 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify4KiB-8         	  276168	      3934 ns/op	1041.09 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify4KiB-8         	  295725	      3913 ns/op	1046.88 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerify4KiB-8         	  295612	      3918 ns/op	1045.42 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerifyBadTag4KiB-8   	  299413	      3918 ns/op	1045.41 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerifyBadTag4KiB-8   	  300783	      3883 ns/op	1054.78 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerifyBadTag4KiB-8   	  299133	      3907 ns/op	1048.26 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerifyBadTag4KiB-8   	  286458	      3906 ns/op	1048.74 MB/s	     544 B/op	       7 allocs/op
BenchmarkVerifyBadTag4KiB-8   	  296984	      3885 ns/op	1054.29 MB/s	     544 B/op	       7 allocs/op
BenchmarkBareHMAC64B-8        	 1361455	       884.3 ns/op	  72.37 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC64B-8        	 1345718	       910.8 ns/op	  70.26 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC64B-8        	 1364170	       880.6 ns/op	  72.68 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC64B-8        	 1347333	       887.6 ns/op	  72.11 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC64B-8        	 1369270	       882.8 ns/op	  72.50 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC1MiB-8       	    1605	    742504 ns/op	1412.22 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC1MiB-8       	    1612	    743286 ns/op	1410.73 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC1MiB-8       	    1593	    742688 ns/op	1411.87 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC1MiB-8       	    1615	    743976 ns/op	1409.42 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareHMAC1MiB-8       	    1609	    743495 ns/op	1410.33 MB/s	     512 B/op	       6 allocs/op
BenchmarkBareSHA256_64B-8     	 7748042	       156.3 ns/op	 409.34 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8     	 7742242	       154.9 ns/op	 413.29 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8     	 7761434	       155.3 ns/op	 412.17 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8     	 7366327	       155.9 ns/op	 410.61 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_64B-8     	 7730130	       154.8 ns/op	 413.41 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8    	    1620	    742880 ns/op	1411.50 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8    	    1621	    741361 ns/op	1414.39 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8    	    1620	    745621 ns/op	1406.31 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8    	    1616	    741255 ns/op	1414.60 MB/s	       0 B/op	       0 allocs/op
BenchmarkBareSHA256_1MiB-8    	    1610	    746488 ns/op	1404.68 MB/s	       0 B/op	       0 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/mac	58.399s
```
