<!-- generated from third-party/codec/protobuf/codec_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./third-party/codec/protobuf/` from the repo root; every figure below is the median of NINE samples over THREE such processes. Wire sizes come from `go test -run TestReportWireSize -v ./third-party/codec/protobuf/` -->
# Benchmarks — `third-party/codec/protobuf`

ADR 0023 quarantines this codec for a **contract** reason, not a dependency one:
Protobuf is schema-bound, so it cannot honour the universal "every registered
codec round-trips any Go struct" guarantee the default registry enforces.
Nothing measured what it costs.

The answer turns out to depend entirely on **which shape of message you hand
it**, by a factor of 3.6 — and the shape most callers reach for is the slow one.
This page measures both, because measuring only the first would be a libel and
measuring only the second would be a sales brochure.

> **These numbers are not comparable with `third-party/codec/hcl/BENCH.md`.**
> That page's `json` and `cbor` rows encode a Go *struct*; these encode a
> `map[string]any`, which is a different and much more expensive job. Compare
> within a file, never across.

## Two shapes, and the gap between them

Protobuf encodes `proto.Message` values. A caller who has run `protoc` has a
**generated message**. A caller who has not — and the SDK's own tests are in this
position, as is anyone holding decoded JSON — reaches for
`structpb.Struct`, the well-known type that wraps arbitrary data.

Same library, same machine, same run, comparable field counts (200 vs 192):

| Marshal | ns/op | B/op | allocs | wire bytes |
|---|---:|---:|---:|---:|
| `structpb.Struct`, 200 fields | 140 603 | 11 264 | **401** | 4 324 |
| generated message, 192 fields | **38 768** | 5 376 | **1** | 5 116 |

**3.63× the time and 401× the allocations, for the shape and nothing else.**
`structpb` models every value as a `Value` message carrying a oneof, and every
object as a `map<string, Value>` — so each field becomes a nested submessage with
its own length prefix, and each one is materialised through reflection. The
allocation profile of the 200-field marshal is one line:

```
      flat  flat%   sum%        cum   cum%
  10322077 99.59% 99.59%   10322077 99.59%  reflect.unsafe_New
     25989  0.25% 99.84%   10348066 99.84%  google.golang.org/protobuf/proto.MarshalOptions.marshal
```

**99.59 % of the objects are `reflect.unsafe_New`** — two per field, exactly the
401 in the column.

## Against the in-tree codecs, through `structpb`: it loses on every axis

| Marshal | Protobuf | JSON | CBOR | pb vs JSON | pb vs CBOR |
|---|---:|---:|---:|---:|---:|
| small (3 fields) | 2 539 ns | 2 370 ns | 773.1 ns | 1.07× slower | 3.28× slower |
| medium (26 fields, nested + list) | 24 329 ns | 16 580 ns | 6 296 ns | 1.47× slower | 3.86× slower |
| large (200 fields) | 140 603 ns | 120 732 ns | 33 758 ns | 1.16× slower | **4.17× slower** |

| Unmarshal | Protobuf | JSON | CBOR | pb vs JSON | pb vs CBOR |
|---|---:|---:|---:|---:|---:|
| small | 3 231 ns | 2 881 ns | 1 878 ns | 1.12× slower | 1.72× slower |
| medium | 34 295 ns | 30 240 ns | 17 103 ns | 1.13× slower | 2.01× slower |
| large | 196 021 ns | 165 677 ns | 105 482 ns | 1.18× slower | 1.86× slower |

And the axis a binary format is supposed to win by default:

| wire bytes | Protobuf | JSON | CBOR |
|---|---:|---:|---:|
| small | 45 | 35 | **29** |
| medium | 678 | 564 | **485** |
| large | 4 324 | 3 652 | **3 193** |

**Protobuf-through-`structpb` is 18–29 % LARGER than JSON and 35–55 % larger than
CBOR.** The framing is the reason: a `structpb` field costs a map-entry
submessage header, a key tag with length, a `Value` submessage header and a oneof
tag before any payload — where JSON costs `"key":` and CBOR costs a one-byte
header. The compact binary format is only compact when the schema removes the
field names, and `structpb` puts them back.

## The cost nobody benchmarks: getting into a message at all

A caller holding a `map[string]any` cannot call `Marshal`. They must convert
first, and `Marshal` benchmarks never show it:

| | `structpb.NewStruct` | then `Marshal` | total | CBOR `Marshal` alone |
|---|---:|---:|---:|---:|
| small | 724.6 ns | 2 539 ns | 3 264 ns | 773.1 ns |
| medium | 6 616 ns | 24 329 ns | 30 945 ns | 6 296 ns |
| large | 33 811 ns | 140 603 ns | **174 414 ns** | 33 758 ns |

> **Converting the large document into a message costs 33 811 ns. CBOR encodes
> the whole document in 33 758 ns.** The two are within 0.2 % of each other: the
> conversion alone is, to measurement precision, the entire cost of the
> alternative.

End to end, a caller with a map pays **1.45× JSON and 5.17× CBOR** to produce the
largest output of the three. There is no reading of these rows on which
`structpb` is the right choice for data that started life as a map.

## Where the time goes: Protobuf encodes everything twice

```
      flat  flat%   sum%        cum   cum%
     210ms  5.61%  5.61%      210ms  5.61%  unicode/utf8.ValidString
     160ms  4.28%  9.89%      190ms  5.08%  internal/runtime/maps.(*Iter).Next
     150ms  4.01% 13.90%      150ms  4.01%  google.golang.org/protobuf/encoding/protowire.AppendVarint
     130ms  3.48% 17.38%     1590ms 42.51%  google.golang.org/protobuf/internal/impl.(*MessageInfo).sizePointerSlow
     120ms  3.21% 20.59%      140ms  3.74%  reflect.cvtDirect
     110ms  2.94% 23.53%     2010ms 53.74%  google.golang.org/protobuf/internal/impl.(*MessageInfo).marshalAppendPointer
     110ms  2.94% 26.47%      190ms  5.08%  runtime.interhash
      90ms  2.41% 34.76%     1590ms 42.51%  google.golang.org/protobuf/internal/impl.sizeMap
```

**`sizePointerSlow` is 42.51 % cumulative.** `proto.Marshal` walks the whole
message to compute its exact encoded size, allocates one buffer of precisely that
size, then walks it again to fill it. With a map field that means iterating every
entry twice — `sizeMap` carries the same 42.51 %.

That is a deliberate trade and it pays off in one place, visibly: **the generated
message marshals in exactly 1 allocation**, because a perfectly sized buffer needs
no growth. On `structpb` the trade is a loss — the second pass is paid, and 401
allocations happen anyway, because the reflection walk allocates as it goes.

`unicode/utf8.ValidString` at 5.61 % is the other half of the schema's promise:
protobuf validates that every `string` field is well-formed UTF-8. JSON and CBOR
do not, on this path. It is a correctness feature with a price tag, and this is
the price tag.

## The generated message, measured fairly

| generated `FileDescriptorProto` (16 messages × 12 fields) | ns/op | B/op | allocs | wire |
|---|---:|---:|---:|---:|
| `protobuf.Marshal` | **38 768** | 5 376 | **1** | **5 116 B** |
| `json.Marshal` (same Go value) | 206 585 | 14 351 | 1 | 14 123 B |
| `protobuf.Unmarshal` | 109 288 | 45 936 | 1 676 | — |

**5.33× faster than JSON and 2.76× smaller on the wire, in one allocation.** This
is protobuf being what it is sold as, and it is the row that justifies the
package existing at all.

Note the asymmetry the encode row hides: `Unmarshal` costs **2.82× its own
`Marshal`** and 1 676 allocations, because decoding must materialise every
submessage, every pointer-to-scalar and every repeated slice. Protobuf's
one-allocation encode is not matched by a one-allocation decode, and a service
that decodes more than it encodes should size on the second number.

## `Append` saves nothing, exactly as documented

| Append, large (`structpb`) | ns/op | B/op | allocs |
|---|---:|---:|---:|
| Protobuf | 140 940 | **11 264** | **401** |
| JSON | 118 901 | 5 335 | 403 |
| CBOR | 32 916 | **0** | **0** |

Protobuf's `Append` is within 0.3 % of its `Marshal` and allocates identically,
because — as `codec.go` says — `proto` exposes no public append API, so `Append`
is `Marshal` plus a copy. JSON drops the output allocation; CBOR appends in
**zero**. A caller reaching for `Appender` to avoid an allocation gets nothing
from this codec, and now that is measured rather than implied.

## The choice table

| If your constraint is… | Use | Because |
|---|---|---|
| **the wire must be Protobuf** — a gRPC peer, a schema registry, a `.proto` contract | `protobuf` **with generated types** | 5.3× JSON's speed, 2.8× smaller, one allocation; this is the only row this package should be chosen for |
| you have a `map[string]any` and want it small and fast | `cbor` (in-tree) | 5.2× faster end to end and 26 % smaller than Protobuf-via-`structpb`, with no dependency |
| you have a `map[string]any` and want it readable | `json` (in-tree) | 1.45× faster end to end and smaller, and every tool can read it |
| **Protobuf, but via `structpb`, for data that is not schema-bound** | **not this** | slower and larger than both alternatives on every row, plus a conversion that costs a whole CBOR encode |

The last row is the finding. `structpb.Struct` exists to let protobuf carry
schemaless data across a schema-bound wire — it is an **interoperability** tool,
and using it as a serialisation strategy inverts every property protobuf is
chosen for.

One structural aside that explains the shape of this whole page: JSON's own rows
here cost 404 allocations at `large`, against **2** for a comparable Go struct in
`third-party/codec/hcl/BENCH.md`. A `map[string]any` forces per-key interface
boxing and key sorting on every encoder that touches it. `structpb.Struct` is a
map underneath, so it inherits that penalty and then adds protobuf's framing on
top.

## Refused

- **Caching a `proto.MarshalOptions` with `UseCachedSize`.** It would let the
  size pass be skipped on repeated marshals of an unchanged message and could
  remove a large share of the 42.51 %. Refused: it is only sound when the caller
  guarantees the message has not been mutated since the last size computation,
  and the `codec.Codec` port — `Marshal(v any)` — cannot know that. Turning a
  correctness precondition into an invisible default is how a wire format starts
  emitting truncated messages.
- **Special-casing `structpb.Struct` to encode it directly.** Refused for the
  same reason ADR 0023 quarantined the codec: this package is a thin adapter over
  a third-party library's own encoder, and a hand-written fast path for one
  message type would be a second protobuf implementation that must stay
  bit-compatible with the first.

Nothing in this package was changed to produce this report.

## Reproducibility envelope

> **Numbers vary across machines.** Every figure quoted above is the **median of
> nine samples over three separate processes**; one of the three is reproduced
> verbatim below. The observed spread is **under 5 % on every row** and the
> allocation columns did not vary at all. What this page claims is the
> **ratios**; the absolute nanoseconds are not portable.
>
> Three processes rather than one because several of this page's ratios sit
> between 1.07× and 1.47×, close enough to a single-process measurement error to
> be reversed by one. A sibling package in this same campaign
> (`third-party/x-crypto/xchacha`) had exactly that happen: a row 20 % wrong from
> a single-process run whose internal spread was 0.7 %.
>
> The `structpb` and generated-message families measure comparable field counts
> (200 and 192) but not identical documents — no such pair exists, because the
> whole difference between them is that one has a schema and the other does not.
> The 3.63× is therefore a like-for-like *scale* comparison, not a like-for-like
> *document* comparison, and the allocation columns (401 against 1) carry the
> claim more safely than the nanoseconds do.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| RAM                | 15.6 GiB (ballooned VM; balloon floor 8 GiB) |
| Load during the runs | 0.03–0.55 (one-minute average, machine otherwise idle) |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Library            | `google.golang.org/protobuf` v1.36.12 |
| Reference arms     | `internal/service/codec/json`, `internal/service/codec/cbor` (same repo, same run) |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `f6082f7` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, × 3 processes |

## Results

One of the three processes, verbatim.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/third-party/codec/protobuf
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkMarshal/protobuf/small-8         	  456457	      2539 ns/op	     144 B/op	       7 allocs/op
BenchmarkMarshal/protobuf/small-8         	  472948	      2528 ns/op	     144 B/op	       7 allocs/op
BenchmarkMarshal/protobuf/small-8         	  457398	      2548 ns/op	     144 B/op	       7 allocs/op
BenchmarkMarshal/json/small-8             	  504033	      2363 ns/op	     168 B/op	      10 allocs/op
BenchmarkMarshal/json/small-8             	  483445	      2370 ns/op	     168 B/op	      10 allocs/op
BenchmarkMarshal/json/small-8             	  479998	      2364 ns/op	     168 B/op	      10 allocs/op
BenchmarkMarshal/cbor/small-8             	 1543683	       775.5 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/cbor/small-8             	 1558156	       771.7 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/cbor/small-8             	 1550804	       774.4 ns/op	      32 B/op	       1 allocs/op
BenchmarkMarshal/protobuf/medium-8        	   49802	     24418 ns/op	    1664 B/op	      61 allocs/op
BenchmarkMarshal/protobuf/medium-8        	   49933	     24036 ns/op	    1664 B/op	      61 allocs/op
BenchmarkMarshal/protobuf/medium-8        	   49936	     24047 ns/op	    1664 B/op	      61 allocs/op
BenchmarkMarshal/json/medium-8            	   72334	     16580 ns/op	    1313 B/op	      56 allocs/op
BenchmarkMarshal/json/medium-8            	   71666	     16640 ns/op	    1313 B/op	      56 allocs/op
BenchmarkMarshal/json/medium-8            	   72339	     16572 ns/op	    1313 B/op	      56 allocs/op
BenchmarkMarshal/cbor/medium-8            	  189132	      6214 ns/op	     512 B/op	       1 allocs/op
BenchmarkMarshal/cbor/medium-8            	  191356	      6222 ns/op	     512 B/op	       1 allocs/op
BenchmarkMarshal/cbor/medium-8            	  195294	      6231 ns/op	     512 B/op	       1 allocs/op
BenchmarkMarshal/protobuf/large-8         	    8569	    138549 ns/op	   11264 B/op	     401 allocs/op
BenchmarkMarshal/protobuf/large-8         	    8228	    139971 ns/op	   11264 B/op	     401 allocs/op
BenchmarkMarshal/protobuf/large-8         	    8586	    140942 ns/op	   11264 B/op	     401 allocs/op
BenchmarkMarshal/json/large-8             	    9906	    120407 ns/op	    9432 B/op	     404 allocs/op
BenchmarkMarshal/json/large-8             	    9802	    121762 ns/op	    9433 B/op	     404 allocs/op
BenchmarkMarshal/json/large-8             	    9709	    120805 ns/op	    9430 B/op	     404 allocs/op
BenchmarkMarshal/cbor/large-8             	   35462	     33738 ns/op	    3203 B/op	       1 allocs/op
BenchmarkMarshal/cbor/large-8             	   35590	     33758 ns/op	    3203 B/op	       1 allocs/op
BenchmarkMarshal/cbor/large-8             	   35457	     33728 ns/op	    3203 B/op	       1 allocs/op
BenchmarkUnmarshal/protobuf/small-8       	  354327	      3225 ns/op	     640 B/op	      18 allocs/op
BenchmarkUnmarshal/protobuf/small-8       	  358983	      3231 ns/op	     640 B/op	      18 allocs/op
BenchmarkUnmarshal/protobuf/small-8       	  352016	      3268 ns/op	     640 B/op	      18 allocs/op
BenchmarkUnmarshal/json/small-8           	  408478	      2878 ns/op	     432 B/op	      11 allocs/op
BenchmarkUnmarshal/json/small-8           	  395706	      2857 ns/op	     432 B/op	      11 allocs/op
BenchmarkUnmarshal/json/small-8           	  409359	      2869 ns/op	     432 B/op	      11 allocs/op
BenchmarkUnmarshal/cbor/small-8           	  599356	      1873 ns/op	     424 B/op	      10 allocs/op
BenchmarkUnmarshal/cbor/small-8           	  633980	      1896 ns/op	     424 B/op	      10 allocs/op
BenchmarkUnmarshal/cbor/small-8           	  602089	      1890 ns/op	     424 B/op	      10 allocs/op
BenchmarkUnmarshal/protobuf/medium-8      	   35138	     34102 ns/op	    6336 B/op	     200 allocs/op
BenchmarkUnmarshal/protobuf/medium-8      	   34869	     34506 ns/op	    6336 B/op	     200 allocs/op
BenchmarkUnmarshal/protobuf/medium-8      	   35176	     34295 ns/op	    6336 B/op	     200 allocs/op
BenchmarkUnmarshal/json/medium-8          	   39685	     30104 ns/op	    3978 B/op	     139 allocs/op
BenchmarkUnmarshal/json/medium-8          	   39404	     30102 ns/op	    3978 B/op	     139 allocs/op
BenchmarkUnmarshal/json/medium-8          	   39864	     29992 ns/op	    3977 B/op	     139 allocs/op
BenchmarkUnmarshal/cbor/medium-8          	   70911	     16952 ns/op	    3472 B/op	      93 allocs/op
BenchmarkUnmarshal/cbor/medium-8          	   69528	     17309 ns/op	    3472 B/op	      93 allocs/op
BenchmarkUnmarshal/cbor/medium-8          	   70106	     17172 ns/op	    3472 B/op	      93 allocs/op
BenchmarkUnmarshal/protobuf/large-8       	    5774	    194357 ns/op	   37848 B/op	    1081 allocs/op
BenchmarkUnmarshal/protobuf/large-8       	    5766	    196082 ns/op	   37848 B/op	    1081 allocs/op
BenchmarkUnmarshal/protobuf/large-8       	    5547	    196203 ns/op	   37848 B/op	    1081 allocs/op
BenchmarkUnmarshal/json/large-8           	    7452	    166759 ns/op	   25611 B/op	     683 allocs/op
BenchmarkUnmarshal/json/large-8           	    7080	    165359 ns/op	   25610 B/op	     683 allocs/op
BenchmarkUnmarshal/json/large-8           	    7479	    165076 ns/op	   25611 B/op	     683 allocs/op
BenchmarkUnmarshal/cbor/large-8           	   10000	    105926 ns/op	   23696 B/op	     417 allocs/op
BenchmarkUnmarshal/cbor/large-8           	   10000	    105989 ns/op	   23696 B/op	     417 allocs/op
BenchmarkUnmarshal/cbor/large-8           	   10000	    106303 ns/op	   23696 B/op	     417 allocs/op
BenchmarkNewStruct/small-8                	 1649779	       725.8 ns/op	     527 B/op	       9 allocs/op
BenchmarkNewStruct/small-8                	 1683495	       713.9 ns/op	     527 B/op	       9 allocs/op
BenchmarkNewStruct/small-8                	 1687957	       716.4 ns/op	     527 B/op	       9 allocs/op
BenchmarkNewStruct/medium-8               	  186082	      6610 ns/op	    4312 B/op	      86 allocs/op
BenchmarkNewStruct/medium-8               	  180802	      6652 ns/op	    4312 B/op	      86 allocs/op
BenchmarkNewStruct/medium-8               	  179467	      6614 ns/op	    4312 B/op	      86 allocs/op
BenchmarkNewStruct/large-8                	   35126	     34693 ns/op	   21367 B/op	     405 allocs/op
BenchmarkNewStruct/large-8                	   33256	     34415 ns/op	   21367 B/op	     405 allocs/op
BenchmarkNewStruct/large-8                	   35918	     33495 ns/op	   21367 B/op	     405 allocs/op
BenchmarkAppend/protobuf/small-8          	  452858	      2577 ns/op	     144 B/op	       7 allocs/op
BenchmarkAppend/protobuf/small-8          	  453938	      2566 ns/op	     144 B/op	       7 allocs/op
BenchmarkAppend/protobuf/small-8          	  451912	      2562 ns/op	     144 B/op	       7 allocs/op
BenchmarkAppend/json/small-8              	  487298	      2373 ns/op	     120 B/op	       9 allocs/op
BenchmarkAppend/json/small-8              	  509425	      2356 ns/op	     120 B/op	       9 allocs/op
BenchmarkAppend/json/small-8              	  496106	      2356 ns/op	     120 B/op	       9 allocs/op
BenchmarkAppend/cbor/small-8              	 1618143	       745.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/small-8              	 1625637	       739.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/small-8              	 1571653	       762.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/protobuf/medium-8         	   48912	     24413 ns/op	    1664 B/op	      61 allocs/op
BenchmarkAppend/protobuf/medium-8         	   49227	     24215 ns/op	    1664 B/op	      61 allocs/op
BenchmarkAppend/protobuf/medium-8         	   49479	     24405 ns/op	    1664 B/op	      61 allocs/op
BenchmarkAppend/json/medium-8             	   71881	     16521 ns/op	     737 B/op	      55 allocs/op
BenchmarkAppend/json/medium-8             	   72249	     16597 ns/op	     736 B/op	      55 allocs/op
BenchmarkAppend/json/medium-8             	   73171	     16550 ns/op	     737 B/op	      55 allocs/op
BenchmarkAppend/cbor/medium-8             	  201054	      5955 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/medium-8             	  201037	      5988 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/medium-8             	  201062	      6054 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/protobuf/large-8          	    8026	    140768 ns/op	   11264 B/op	     401 allocs/op
BenchmarkAppend/protobuf/large-8          	    8656	    140668 ns/op	   11264 B/op	     401 allocs/op
BenchmarkAppend/protobuf/large-8          	    8425	    141973 ns/op	   11264 B/op	     401 allocs/op
BenchmarkAppend/json/large-8              	    9804	    119468 ns/op	    5336 B/op	     403 allocs/op
BenchmarkAppend/json/large-8              	    9796	    118009 ns/op	    5336 B/op	     403 allocs/op
BenchmarkAppend/json/large-8              	    9943	    120714 ns/op	    5336 B/op	     403 allocs/op
BenchmarkAppend/cbor/large-8              	   36844	     33277 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/large-8              	   37141	     32530 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppend/cbor/large-8              	   36763	     32782 ns/op	       0 B/op	       0 allocs/op
BenchmarkGeneratedMarshal/protobuf-8      	   30265	     38821 ns/op	    5382 B/op	       1 allocs/op
BenchmarkGeneratedMarshal/protobuf-8      	   30412	     38867 ns/op	    5376 B/op	       1 allocs/op
BenchmarkGeneratedMarshal/protobuf-8      	   31021	     39354 ns/op	    5376 B/op	       1 allocs/op
BenchmarkGeneratedMarshal/json-8          	    5715	    207778 ns/op	   14358 B/op	       1 allocs/op
BenchmarkGeneratedMarshal/json-8          	    5848	    204670 ns/op	   14352 B/op	       1 allocs/op
BenchmarkGeneratedMarshal/json-8          	    5677	    207575 ns/op	   14352 B/op	       1 allocs/op
BenchmarkGeneratedUnmarshal-8             	    9440	    110446 ns/op	   45936 B/op	    1676 allocs/op
BenchmarkGeneratedUnmarshal-8             	   10000	    108770 ns/op	   45936 B/op	    1676 allocs/op
BenchmarkGeneratedUnmarshal-8             	   10000	    108993 ns/op	   45936 B/op	    1676 allocs/op
PASS
ok  	github.com/kitsunium/sdk/third-party/codec/protobuf
```
