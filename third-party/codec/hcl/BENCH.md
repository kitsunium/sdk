<!-- generated from third-party/codec/hcl/codec_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./third-party/codec/hcl/` from the repo root; every figure below is the median of NINE samples over THREE such processes. Wire sizes come from `go test -run TestReportWireSize -v ./third-party/codec/hcl/` -->
# Benchmarks — `third-party/codec/hcl`

ADR 0022 quarantines this codec for a **dependency** reason: `hashicorp/hcl/v2`
drags `go-cty` and, through `x/tools`, the `x/sys` that the four inner modules
ban (ADR 0034 corrected the mechanism; the placement stood). Nothing measured
what it costs to *use*.

It costs a great deal, and the number is not close. This page exists so that
"opt-in" is understood as a real decision rather than a formality.

Every row encodes the **same Go value**: the fixtures carry `hcl` and `json`
struct tags side by side, and `fxamacker/cbor` falls back to the `json` tag, so
JSON and CBOR — the two codecs a consumer gets without opting into anything —
encode exactly what HCL encodes. The documents are configuration files, because
that is what HCL is for.

> **These numbers are not comparable with `third-party/codec/protobuf/BENCH.md`.**
> That page's `json` and `cbor` rows encode a `map[string]any`; these encode a Go
> struct, which is a different and much cheaper job. Compare within a file, never
> across.

## The headline

| Marshal | HCL | JSON | CBOR | HCL vs JSON |
|---|---:|---:|---:|---:|
| small (2 attributes) | 18 887 ns | 642.7 ns | 208.3 ns | **29.4× slower** |
| medium (12 attributes, 2 blocks) | 104 911 ns | 2 180 ns | 918.7 ns | **48.1× slower** |
| large (257 attributes, 64 blocks) | 2 957 891 ns | 37 731 ns | 16 570 ns | **78.4× slower** |

| Unmarshal | HCL | JSON | CBOR | HCL vs JSON |
|---|---:|---:|---:|---:|
| small | 17 828 ns | 818.2 ns | 573.0 ns | **21.8× slower** |
| medium | 98 923 ns | 3 780 ns | 3 213 ns | **26.2× slower** |
| large | 2 667 091 ns | 76 682 ns | 54 412 ns | **34.8× slower** |

And the column that is harder to shrug off:

| Marshal, large | bytes allocated | allocations | wire bytes produced |
|---|---:|---:|---:|
| HCL | **2 672 515** | **14 215** | 7 851 |
| JSON | 6 579 | **2** | 6 391 |
| CBOR | 5 378 | **1** | 5 275 |

**HCL allocates 2.67 MB and takes 14 215 trips to the heap to produce 7 851
bytes** — 340× the output size, and 7 108× as many allocations as JSON needs for
the same document.

## The cost has a unit: about 11.6 µs and 55 allocations per attribute

The two larger fixtures differ by 245 attributes, which gives a slope rather than
a pair of totals:

| per attribute encoded | HCL | JSON | CBOR |
|---|---:|---:|---:|
| time | **11 645 ns** | 145.1 ns | 63.9 ns |
| allocations | **55.3** | ~0 | ~0 |

Those slopes are a genuine cross-check rather than a restatement, because they
were derived from the *difference* between the medium and large rows and they
reproduce the *totals* independently: 11 645 / 145.1 = **80× against JSON**
where the large row measured 78.4×, and 11 645 / 63.9 = **182× against CBOR**
where the large row measured 178.5×. The small fixture agrees too — the model
predicts 118 allocations for its two attributes and 126 were measured.

So the cost is not a fixed startup penalty that amortises away on bigger
documents. **It scales per attribute, and it gets relatively worse as the
document grows**, because HCL's fixed cost is small and its marginal cost is
enormous.

## Why: the encoder is a code formatter, not a serialiser

The allocation profile of `Marshal` on the large document names every part:

```
      flat  flat%   sum%        cum   cum%
   1660316 13.08% 13.08%    1660316 13.08%  github.com/hashicorp/hcl/v2/hclwrite.(*nodes).Append (inline)
   1190598  9.38% 22.45%    4238125 33.38%  github.com/hashicorp/hcl/v2/hclwrite.(*Attribute).init
    977606  7.70% 30.15%    2154566 16.97%  github.com/hashicorp/hcl/v2/hclwrite.appendTokensForValue
    830158  6.54% 36.69%     830158  6.54%  github.com/hashicorp/hcl/v2/hclwrite.newNode (inline)
    830140  6.54% 43.23%    1660298 13.08%  github.com/hashicorp/hcl/v2/hclwrite.(*nodes).AppendUnstructuredTokens (inline)
    699065  5.51% 48.73%     699065  5.51%  github.com/hashicorp/hcl/v2/hclwrite.newComments (inline)
    636194  5.01% 53.74%     636194  5.01%  github.com/hashicorp/hcl/v2/gohcl.getFieldTags
    600761  4.73% 58.47%    1671207 13.16%  github.com/hashicorp/hcl/v2/hclwrite.(*Block).init
    589830  4.65% 63.12%     589830  4.65%  github.com/hashicorp/hcl/v2/hclwrite.newInTree (inline)
    531092  4.18% 67.30%     531092  4.18%  bufio.(*Scanner).Scan
```

`gohcl.EncodeIntoBody` does not write bytes. It builds an **editable syntax
tree** — `newNode`, `newInTree`, `nodes.Append`, and a `newComments` slot on
every node so a later caller could attach a comment that this path will never
have — and only then renders it. Two entries make the point beyond doubt:

- `bufio.(*Scanner).Scan` at 4.18 % of objects, and `textseg.ScanGraphemeClusters`
  at 3.20 % of CPU. The encoder **re-tokenises its own output and walks it by
  Unicode grapheme cluster**, because `hclwrite` computes column positions in
  order to align `=` signs the way `terraform fmt` does.
- `math/big.rsh` at 1.96 % of CPU. Every number goes through `cty`'s
  arbitrary-precision `big.Float`. `Replica: 3` is an infinite-precision decimal
  before it is the byte `3`.

`gohcl.getFieldTags` at 5.01 % is the other half: the struct tags are re-parsed
by reflection **on every call**, with no per-type cache — the mechanism
`internal/service/validation` uses to make its tag front end 48× cheaper, and
which this library does not have.

None of that is a bug. It is `hclwrite` doing its actual job, which is to be the
engine behind `terraform fmt`. It is simply not what a wire codec does, and the
`codec.Codec` port makes the two look interchangeable when they are not.

## `Append` saves nothing here, and that is worth knowing

The codec domain treats `Appender` as the interface that lets a caller encode
into a buffer it already owns. Measured on the large document:

| Append, large | ns/op | B/op | allocs |
|---|---:|---:|---:|
| HCL | 3 095 510 | **2 672 508** | **14 215** |
| JSON | 35 755 | 49 | 1 |
| CBOR | 15 002 | **0** | **0** |

CBOR appends in **zero allocations**; JSON drops from two to one by not
allocating the result. **HCL's `Append` allocates exactly what its `Marshal`
allocates**, because — as `codec.go` says in its own comment — HCL has no
in-place API, so `Append` is `Marshal` followed by a copy. The measurement turns
that comment into a fact: identical `allocs/op`, `B/op` equal to within the seven
bytes that separate two sampled lines, and a time 4.7 % higher.

The practical consequence: **a caller who reaches for `Append` to avoid an
allocation gets nothing from this codec.** The Appender is implemented for
interface conformance, not for a saving.

## Wire size — HCL is also the largest

| document | HCL | JSON | CBOR | HCL vs JSON |
|---|---:|---:|---:|---:|
| small | 34 B | 32 B | 25 B | +6.3 % |
| medium | 225 B | 196 B | 137 B | +14.8 % |
| large | 7 851 B | 6 391 B | 5 275 B | **+22.8 %** |

Which is the expected shape for a human-facing language — the newlines, the
indentation, the `=` alignment and the block braces are the feature — but it
removes the last axis on which HCL could have won.

## The choice table

> Read the row that matches your requirement, not the row with the biggest
> number.

| If your constraint is… | Use | Because |
|---|---|---|
| **the file must be HCL** — Terraform-adjacent config, an operator edits it by hand, a tool downstream parses it | `hcl` | a format is a contract, not a performance choice; this is the only row this package should ever be chosen for |
| configuration the SDK owns both ends of | `json` (in-tree) | 29–78× faster, 2 allocations, no dependency, and `config` already decodes it |
| the same, minimising bytes and allocations | `cbor` (in-tree) | fastest and smallest on every row here, and its `Append` is allocation-free |
| anything on a request path | **not this** | 11.6 µs per attribute is a per-request cost nobody budgets for |

**HCL is a configuration language that this SDK can read and write. It is not a
codec to reach for.** The dependency argument for the quarantine (ADR 0022/0034)
is now joined by a performance argument that points the same way, and the
package's own doc comment — "HCL is decode-oriented" — is measured true: decoding
is 35× JSON's cost where encoding is 78×.

## Refused

- **Caching `gohcl.getFieldTags` per type.** It is 5.01 % of the allocated
  objects and the obvious fix, and it is not this package's code to change: the
  cache would have to live inside `hashicorp/hcl/v2`. Vendoring or shadowing a
  third-party reflection path to save 5 % of a cost that is 78× the alternative
  is not a trade worth making — the caller who cares should use `json`.
- **Bypassing `hclwrite` and emitting bytes directly.** It would remove the
  token tree, the grapheme scan and most of the 14 215 allocations. Refused: a
  hand-written HCL emitter is a second implementation of a third-party language's
  syntax, it would drift from the parser that has to read it back, and this SDK's
  doctrine puts a third-party *format* behind that party's own library (ADR 0064
  draws the same line for SMTP). The whole reason this package exists is to not
  reimplement HCL.

Nothing in this package was changed to produce this report.

## Rows thrown away, and what was wrong with them

One row-set, and the correction is instructive rather than dramatic. In a single
early run `Append/hcl/large` measured **7.0 %** above `Marshal/hcl/large`, which
is far too much for a 7 851-byte copy. Re-measured as an isolated pair with
`-count=5` the gap was 3.2 % with overlapping ranges; across the nine samples
published above it is **4.7 %**. The 7 % was the row's position in a 27-row run,
not the copy.

The claim that survives is the one that never moved: `Append` and `Marshal`
allocate **identically** — 14 215 allocations both, at every fixture size, in
every run. An allocation column does not drift, which is why it carries the
conclusion here and the nanoseconds do not.

## Reproducibility envelope

> **Numbers vary across machines.** Every figure quoted above is the **median of
> nine samples over three separate processes**; one of the three is reproduced
> verbatim below. This file is the most stable in the four-package set — the
> observed spread is **under 8 % on every row and under 3 % on most** — and the
> allocation columns did not vary at all. What this page claims is the
> **ratios**; the absolute nanoseconds are not portable.
>
> Three processes rather than one because a sibling package in this same campaign
> (`third-party/x-crypto/xchacha`) had a row that was 20 % wrong from a
> single-process run with a 0.7 % internal spread. Within one process the samples
> are correlated, so a tight `-count=3` reports a precision it does not have.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| RAM                | 15.6 GiB (ballooned VM; balloon floor 8 GiB) |
| Load during the runs | 0.03–0.55 (one-minute average, machine otherwise idle) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Library            | `github.com/hashicorp/hcl/v2` v2.24.0 |
| Reference arms     | `internal/service/codec/json`, `internal/service/codec/cbor` (same repo, same run) |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `c411206` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, × 3 processes |

## Results

One of the three processes, verbatim.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/third-party/codec/hcl
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkMarshal/hcl/small-8         	   63590	     18939 ns/op	   21224 B/op	     126 allocs/op
BenchmarkMarshal/hcl/small-8         	   63296	     18985 ns/op	   21224 B/op	     126 allocs/op
BenchmarkMarshal/hcl/small-8         	   63068	     19454 ns/op	   21224 B/op	     126 allocs/op
BenchmarkMarshal/hcl/medium-8        	   10000	    108737 ns/op	  124456 B/op	     671 allocs/op
BenchmarkMarshal/hcl/medium-8        	    9920	    106077 ns/op	  124456 B/op	     671 allocs/op
BenchmarkMarshal/hcl/medium-8        	   10000	    105091 ns/op	  124456 B/op	     671 allocs/op
BenchmarkMarshal/hcl/large-8         	     409	   2970149 ns/op	 2672517 B/op	   14215 allocs/op
BenchmarkMarshal/hcl/large-8         	     400	   2937903 ns/op	 2672503 B/op	   14215 allocs/op
BenchmarkMarshal/hcl/large-8         	     393	   3002111 ns/op	 2672505 B/op	   14215 allocs/op
BenchmarkMarshal/json/small-8        	 1874762	       639.9 ns/op	      56 B/op	       2 allocs/op
BenchmarkMarshal/json/small-8        	 1852320	       648.3 ns/op	      56 B/op	       2 allocs/op
BenchmarkMarshal/json/small-8        	 1875008	       640.8 ns/op	      56 B/op	       2 allocs/op
BenchmarkMarshal/json/medium-8       	  521350	      2190 ns/op	     304 B/op	       2 allocs/op
BenchmarkMarshal/json/medium-8       	  542378	      2180 ns/op	     304 B/op	       2 allocs/op
BenchmarkMarshal/json/medium-8       	  538018	      2186 ns/op	     304 B/op	       2 allocs/op
BenchmarkMarshal/json/large-8        	   31664	     38357 ns/op	    6580 B/op	       2 allocs/op
BenchmarkMarshal/json/large-8        	   31596	     37813 ns/op	    6579 B/op	       2 allocs/op
BenchmarkMarshal/json/large-8        	   31866	     37731 ns/op	    6579 B/op	       2 allocs/op
BenchmarkMarshal/cbor/small-8        	 5700162	       208.6 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/cbor/small-8        	 5736824	       208.1 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/cbor/small-8        	 5730712	       209.2 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/cbor/medium-8       	 1306897	       921.4 ns/op	     144 B/op	       1 allocs/op
BenchmarkMarshal/cbor/medium-8       	 1303958	       919.1 ns/op	     144 B/op	       1 allocs/op
BenchmarkMarshal/cbor/medium-8       	 1309694	       918.7 ns/op	     144 B/op	       1 allocs/op
BenchmarkMarshal/cbor/large-8        	   71865	     16504 ns/op	    5378 B/op	       1 allocs/op
BenchmarkMarshal/cbor/large-8        	   72424	     16573 ns/op	    5378 B/op	       1 allocs/op
BenchmarkMarshal/cbor/large-8        	   71892	     16503 ns/op	    5378 B/op	       1 allocs/op
BenchmarkUnmarshal/hcl/small-8       	   66231	     17827 ns/op	    8952 B/op	      80 allocs/op
BenchmarkUnmarshal/hcl/small-8       	   66886	     17759 ns/op	    8952 B/op	      80 allocs/op
BenchmarkUnmarshal/hcl/small-8       	   67616	     17776 ns/op	    8952 B/op	      80 allocs/op
BenchmarkUnmarshal/hcl/medium-8      	   12114	     98727 ns/op	   44163 B/op	     372 allocs/op
BenchmarkUnmarshal/hcl/medium-8      	   12210	     98226 ns/op	   44163 B/op	     372 allocs/op
BenchmarkUnmarshal/hcl/medium-8      	   12241	     98084 ns/op	   44163 B/op	     372 allocs/op
BenchmarkUnmarshal/hcl/large-8       	     448	   2621144 ns/op	 1027249 B/op	    7917 allocs/op
BenchmarkUnmarshal/hcl/large-8       	     456	   2629697 ns/op	 1027248 B/op	    7917 allocs/op
BenchmarkUnmarshal/hcl/large-8       	     453	   2646574 ns/op	 1027247 B/op	    7917 allocs/op
BenchmarkUnmarshal/json/small-8      	 1477261	       811.8 ns/op	      24 B/op	       1 allocs/op
BenchmarkUnmarshal/json/small-8      	 1464662	       817.7 ns/op	      24 B/op	       1 allocs/op
BenchmarkUnmarshal/json/small-8      	 1466530	       815.3 ns/op	      24 B/op	       1 allocs/op
BenchmarkUnmarshal/json/medium-8     	  310686	      3789 ns/op	     192 B/op	       3 allocs/op
BenchmarkUnmarshal/json/medium-8     	  317271	      3776 ns/op	     192 B/op	       3 allocs/op
BenchmarkUnmarshal/json/medium-8     	  309014	      3779 ns/op	     192 B/op	       3 allocs/op
BenchmarkUnmarshal/json/large-8      	   15339	     77801 ns/op	   11653 B/op	     136 allocs/op
BenchmarkUnmarshal/json/large-8      	   15595	     76945 ns/op	   11653 B/op	     136 allocs/op
BenchmarkUnmarshal/json/large-8      	   15634	     76606 ns/op	   11653 B/op	     136 allocs/op
BenchmarkUnmarshal/cbor/small-8      	 2085912	       571.5 ns/op	      40 B/op	       2 allocs/op
BenchmarkUnmarshal/cbor/small-8      	 2099906	       569.8 ns/op	      40 B/op	       2 allocs/op
BenchmarkUnmarshal/cbor/small-8      	 2086326	       575.9 ns/op	      40 B/op	       2 allocs/op
BenchmarkUnmarshal/cbor/medium-8     	  370134	      3215 ns/op	     224 B/op	       8 allocs/op
BenchmarkUnmarshal/cbor/medium-8     	  331982	      3226 ns/op	     224 B/op	       8 allocs/op
BenchmarkUnmarshal/cbor/medium-8     	  375320	      3201 ns/op	     224 B/op	       8 allocs/op
BenchmarkUnmarshal/cbor/large-8      	   22155	     54390 ns/op	    7376 B/op	     132 allocs/op
BenchmarkUnmarshal/cbor/large-8      	   21903	     54502 ns/op	    7376 B/op	     132 allocs/op
BenchmarkUnmarshal/cbor/large-8      	   22173	     54252 ns/op	    7376 B/op	     132 allocs/op
BenchmarkAppend/hcl/small-8          	   59847	     20088 ns/op	   21224 B/op	     126 allocs/op
BenchmarkAppend/hcl/small-8          	   58484	     20256 ns/op	   21224 B/op	     126 allocs/op
BenchmarkAppend/hcl/small-8          	   59734	     19859 ns/op	   21224 B/op	     126 allocs/op
BenchmarkAppend/hcl/medium-8         	   10000	    110525 ns/op	  124456 B/op	     671 allocs/op
BenchmarkAppend/hcl/medium-8         	   10000	    111003 ns/op	  124456 B/op	     671 allocs/op
BenchmarkAppend/hcl/medium-8         	   10000	    113787 ns/op	  124456 B/op	     671 allocs/op
BenchmarkAppend/hcl/large-8          	     380	   3082901 ns/op	 2672511 B/op	   14215 allocs/op
BenchmarkAppend/hcl/large-8          	     397	   3082182 ns/op	 2672510 B/op	   14215 allocs/op
BenchmarkAppend/hcl/large-8          	     385	   3069594 ns/op	 2672510 B/op	   14215 allocs/op
BenchmarkAppend/json/small-8         	 1870702	       642.9 ns/op	      24 B/op	       1 allocs/op
BenchmarkAppend/json/small-8         	 1867815	       641.8 ns/op	      24 B/op	       1 allocs/op
BenchmarkAppend/json/small-8         	 1862506	       644.8 ns/op	      24 B/op	       1 allocs/op
BenchmarkAppend/json/medium-8        	  562980	      2100 ns/op	      96 B/op	       1 allocs/op
BenchmarkAppend/json/medium-8        	  570249	      2102 ns/op	      96 B/op	       1 allocs/op
BenchmarkAppend/json/medium-8        	  563365	      2100 ns/op	      96 B/op	       1 allocs/op
BenchmarkAppend/json/large-8         	   33511	     35597 ns/op	      49 B/op	       1 allocs/op
BenchmarkAppend/json/large-8         	   33555	     35713 ns/op	      49 B/op	       1 allocs/op
BenchmarkAppend/json/large-8         	   33298	     35754 ns/op	      49 B/op	       1 allocs/op
BenchmarkAppend/cbor/small-8         	 6208605	       189.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/small-8         	 6324063	       189.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/small-8         	 6340596	       188.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/medium-8        	 1436166	       833.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/medium-8        	 1447208	       827.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/medium-8        	 1453222	       827.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/large-8         	   81049	     14895 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/large-8         	   79464	     15038 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/large-8         	   80656	     14893 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/kitsunium/sdk/third-party/codec/hcl	96.110s
```
