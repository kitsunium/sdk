<!-- generated from internal/service/codec/tlv/tlv_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./codec/tlv/` to refresh -->
# Benchmarks — `internal/service/codec/tlv`

A reflection-driven, self-describing codec — six thousand lines of it, and no
measurement of its own. `pkg/v1/codec/BENCH.md` compares it against the other
formats on a scalar payload; nothing said what a **field** costs, what a
**nesting level** costs, or whether the type-info cache the package built for
itself is worth what it claims.

Four questions, four answers, and one of them removed 10 000 allocations from a
thousand-row decode.

## 1. A field costs 18 ns and a nesting level costs seven fields

`Append` into a buffer that already has capacity, so the number is the encoder
and not the allocator.

| shape | ns/op | allocs |
|---|---:|---:|
| `Append` 1 field | 69.78 | **0** |
| `Append` 4 fields | 133.0 | **0** |
| `Append` 16 fields | 344.7 | **0** |

The slope across the sweep is **18.3 ns per scalar field**, and the intercept
is ~51 ns of fixed per-call cost (the struct header, the type-info lookup, the
`reflect.Value` for the root). Nothing on this path allocates at all.

Nesting is a different price:

| shape | ns/op | ns per level |
|---|---:|---:|
| `Append` depth 1 | 122.6 | — |
| `Append` depth 4 | 443.4 | 107 |
| `Append` depth 16 | 2 062 | **129** |

Each level of the chain carries two scalar fields, so ~37 ns of that is field
cost and the remaining **~92 ns is the recursion itself** — the pointer
dereference, the depth check, the `encodeReflectValue` → `encodeByKind`
dispatch, and a second `cachedStructTypeInfo` lookup.

> **One nesting level costs about as much as seven flat fields.** A payload
> with a choice between `{a, b, c, d}` and `{a, b, inner:{c, d}}` should take
> the flat one, and the difference is 92 ns per record, not a rounding error.

At scale the fixed cost disappears: `BenchmarkAppendLarge` encodes 1 000
five-field records into 63 890 wire bytes in 161 285 ns — **161 ns per record**
against 146.8 ns for one `benchMixed` on its own, and **one** allocation for
the whole message.

## 2. `Marshal` is `Append` plus the allocator, and that is the whole difference

| fields | `Append` (sized dst) | `Marshal` (nil dst) | delta | `Marshal` allocs |
|---:|---:|---:|---:|---:|
| 1 | 69.78 ns | 158.2 ns | +88 ns | 2 · 24 B |
| 4 | 133.0 ns | 362.3 ns | +229 ns | 4 · 120 B |
| 16 | 344.7 ns | 842.8 ns | +498 ns | 6 · 504 B |

`Marshal` delegates to `Append(nil, v)`, so every doubling of the output is a
`growslice`. At 16 fields that is **six allocations and 2.4× the wall time**,
all of it the growth cascade.

The package `CLAUDE.md` claimed `Append` "benches at 1 alloc/op". That number
came from the cross-format harness, where `dst` is a recycled buffer that is
sometimes too small. With a destination that is genuinely pre-sized, **`Append`
is zero allocations**, and the doc now says so.

## 3. What a field NAME cost, and what removing it bought

`BenchmarkUnmarshalTyped` — 1 000 records straight into `[]benchMixed`, the
`tryDecodeRootInto` fast path — reported **16 003 allocations**. A memory
profile attributed them:

| source | share of allocated objects |
|---|---:|
| `decodeString` | **74.79 %** |
| — of which, reached via `decodeFieldName` | **61.80 %** |
| `reflect.unsafe_New` | 9.55 % |
| `reflect.Value.extendSlice` | 8.55 % |

Every field name on the wire was being turned into a Go `string` — **twice
over**, because `decodeString` returns `any`, so the bytes were copied to the
heap and then the string header was boxed into an interface. Five fields per
record, two allocations each, ten per record, for a value that is used to look
up a field index and then dropped.

`parseFieldName` now returns the name as a `[]byte` aliasing the input, and
`resolveFieldIndex` compares it against a `nameBytes` built once per type at
cache-build time. Nothing on the decode path converts.

| | before | after |
|---|---:|---:|
| `UnmarshalTyped` (1 000 records) | 1 350 030 ns · 321 587 B · 16 003 allocs | **1 022 308 ns · 210 360 B · 6 003 allocs** |
| `UnmarshalMixed` (one 5-field struct) | 1 194 ns · 240 B · 15 allocs | **874.1 ns · 136 B · 5 allocs** |
| `UnmarshalAny` (1 000 records, untyped) | 1 090 471 ns · 503 650 B · 16 001 allocs | **910 377 ns · 423 625 B · 11 001 allocs** |

**A five-field struct now decodes in 5 allocations instead of 15** — one per
field value, which is the boxing a `map`-free reflective decoder cannot avoid,
and zero for the names.

The untyped path improved too, by exactly one allocation per field name: it
still needs a real `string` for its `map[string]any` key, but it no longer pays
`decodeString`'s interface box on top. That was a defect in the shared helper,
not in either caller.

Every test stayed green, including the round-trip and malformed-name suites.

## 4. The `singleflight` question, answered: no

The repository carries an open note that `cachedStructTypeInfo` (the function
`typeInfoFor` in that note) is a candidate for `internal/kernel/singleflight`.
Three measurements settle it, and none of them is close.

**What the protection would cost.** `internal/kernel/singleflight/BENCH.md`
measures a leading `Do` at **2 007 ns** uncontended and **2 879 ns** per caller
on a contended shared key. That is a goroutine park/unpark round trip and it is
not negotiable — it is what the primitive *is*.

**What the work costs.** The miss it would protect:

| struct | `buildStructTypeInfo` | vs a 2 007 ns leading `Do` |
|---|---:|---:|
| 1 field | **234.7 ns** | 8.6× cheaper than the protection |
| 5 fields | **896.1 ns** | 2.2× cheaper than the protection |
| 16 fields | **2 356 ns** | 1.17× more expensive |

**How often it happens.** Once per Go type, per process, for the life of the
process — `reflect.Type` identities are stable and the cache never evicts.

**Whether there is contention to relieve.** No:

| | ns/op | allocs |
|---|---:|---:|
| `TypeInfoHit` (serial) | 26.23 | 0 |
| `TypeInfoHitParallel` (8 P) | **3.712** | 0 |

`sync.Map`'s read-mostly path scales at **7.1×** across 8 cores. The hot path
is not a bottleneck and adding a `Group` in front of it could only slow it.

> **Verdict.** Take the worst case a `Group` could improve: eight goroutines
> racing the very first encode of a 16-field type. Today they duplicate
> 2 356 ns of work **in parallel** — 2 356 ns of wall time, 18.8 µs of CPU,
> once. With a `Group`, one leader runs 2 356 ns on a goroutine of its own
> while seven callers park, and the contended figure says each of them waits
> ~2 879 ns. **Wall time gets worse, and 16 µs of CPU is saved once in the
> life of the process.** For a 1-field type the protection costs 8.6× the work
> it protects. `sync.Map.LoadOrStore` already makes the *store* idempotent;
> only the work can duplicate, at most once per racing goroutine, once ever.
>
> This is exactly the rule `singleflight/BENCH.md` states — "a `Group` pays for
> itself when `fn` costs materially more than ~2 µs **and** callers actually
> collide" — and `buildStructTypeInfo` fails both halves. The note should be
> closed, not implemented.

Note that §3 made `buildStructTypeInfo` **more** expensive, by one allocation
per field: the 5-field build went from 496 B / 7 allocs to 632 B / 12, and the
16-field one from 1 560 B / 18 to 2 200 B / 34. (The wall-clock before/after is
not quoted because the two were not measured under the same machine load; the
allocation counts are exact.) That is paid **once per type, ever**, against 10
allocations saved on **every record decoded** — and it does not disturb the
verdict, which already rested on the two smaller shapes being an order of
magnitude below the threshold and on there being no contention to relieve.

## 5. The typed root path is not faster than the untyped one, and that is fine

| 1 000 records | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `UnmarshalTyped` → `[]benchMixed` | 1 022 308 | 210 360 | 6 003 |
| `UnmarshalAny` → `[]any` of `map[string]any` | 910 377 | 423 625 | 11 001 |

The typed path is **12 % slower and allocates 50 % less**. That reads as a
contradiction of the package doc, and it is not one: the two benchmarks do not
produce the same thing. `UnmarshalAny` stops at an intermediate the caller must
still project into structs; `UnmarshalTyped` hands back finished Go values. The
typed path's extra time is `reflect.Value.Set` per field, which is the work the
untyped path has simply not done yet.

What it does say is that the typed path has headroom, and a profile names
where: after §3, **20 % of its remaining allocations are `decodeInt` and
`decodeFloat` boxing a scalar into `any`** on a path that already holds a typed
destination. Removing that means a typed scalar decoder that never touches
`any`, which is a real change to `decodeScalarByTag`'s signature and its
callers. Recorded, not done — and it is the next thing to do here.

## 6. Left alone, with the reason

- **`decodeString` for a string VALUE** (34.32 % of the remaining allocated
  objects) stays a copy. The decoded value outlives `Unmarshal` while the
  caller may reuse the input buffer, so an `unsafe.String` view would be a
  use-after-free. The package `CLAUDE.md` already says this; the profile now
  quantifies what it costs.
- **`reflect.unsafe_New` + `reflect.Value.extendSlice`** (45 % of the
  remaining objects) are `reflect` building the caller's slice. They are the
  price of a reflective decoder and there is nothing above them to remove.
- **The encode scratch pool** is left exactly as documented: `Append` into a
  sized buffer is already 0 allocations, so there is nothing for a pool to
  save on that path.

## Reproducibility envelope

> **Numbers vary across machines, and this run shared the box.** Two other
> agent jobs were active; load average ranged 2.7 to 6.1 on 8 cores. Every
> figure is the **median of three `-benchtime=1s` runs**; the run-to-run spread
> was under 4 % on fifteen of the nineteen rows, 10.6 % on
> `AppendFieldSweep/16fields`, 10.7 % on `AppendNestSweep/depth4`, 14.8 % on
> `TypeInfoHitParallel` and 23.4 % on `TypeInfoBuild/1fields`. **The allocation
> columns are invariant** and did not vary at all — every before/after claim in
> §3 rests on them, not on the nanoseconds.
>
> The §4 verdict compares against `internal/kernel/singleflight/BENCH.md`,
> taken on this same machine and toolchain.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| CPU                | AMD EPYC 7351P 16-Core Processor |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `agent-ae8135b496985ac91` (off `jaimerias-que-tu-te-connect`) |
| Git commit         | `c30e2ad` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-count=3`, medians, machine under load |

## Results

Median run of three per row, verbatim.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/codec/tlv
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkAppendFieldSweep/1fields-8         	16894378	        69.78 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendFieldSweep/4fields-8         	 9137284	       133.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendFieldSweep/16fields-8        	 3568329	       344.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkMarshalFieldSweep/1fields-8        	 7807783	       158.2 ns/op	      24 B/op	       2 allocs/op
BenchmarkMarshalFieldSweep/4fields-8        	 3430090	       362.3 ns/op	     120 B/op	       4 allocs/op
BenchmarkMarshalFieldSweep/16fields-8       	 1452766	       842.8 ns/op	     504 B/op	       6 allocs/op
BenchmarkAppendNestSweep/depth1-8           	10182522	       122.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendNestSweep/depth4-8           	 2755682	       443.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendNestSweep/depth16-8          	  593962	      2062 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendMixed-8                      	 7961734	       146.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkAppendLarge-8                      	    7482	    161285 ns/op	     63890 wire-B	      24 B/op	       1 allocs/op
BenchmarkUnmarshalTyped-8                   	    1137	   1022308 ns/op	  210360 B/op	    6003 allocs/op
BenchmarkUnmarshalAny-8                     	    1214	    910377 ns/op	  423625 B/op	   11001 allocs/op
BenchmarkUnmarshalMixed-8                   	 1358960	       874.1 ns/op	     136 B/op	       5 allocs/op
BenchmarkTypeInfoHit-8                      	45750835	        26.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkTypeInfoHitParallel-8              	270113268	         3.712 ns/op	       0 B/op	       0 allocs/op
BenchmarkTypeInfoBuild/1fields-8            	 4777681	       234.7 ns/op	     144 B/op	       4 allocs/op
BenchmarkTypeInfoBuild/5fields-8            	 1345388	       896.1 ns/op	     632 B/op	      12 allocs/op
BenchmarkTypeInfoBuild/16fields-8           	  486727	      2356 ns/op	    2200 B/op	      34 allocs/op
```
