<!-- generated from internal/service/i18n/i18n_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./i18n/` -->
# Benchmarks — `internal/service/i18n`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `agent-afbd5167737bfca6d` |
| Git commit        | `04a3fe3` (the tree this domain was added to) |
| Generated (UTC)   | 2026-09-10 |
| Bench wall-clock  | `-benchtime=1s -count=5`, median quoted |

## What is being measured

Rendering a message is on the path of **every string on every page** of a
translated application, and negotiating a language is on the path of every
request. A domain that answers in nanoseconds is one nobody routes around; one
that allocates per string is one whose cost shows up as GC pressure attributed
to something else entirely.

Four things are measured because ADR 0063 turns on all four: **key lookup**,
**plural selection**, **interpolation** and **language negotiation** — plus the
two construction paths (`NewPrinter`, `NewStore`) that exist precisely so the
request path does not pay for them.

## Results (median of 5)

### The render path

| Benchmark | ns/op | B/op | allocs/op | What it is |
|---|---:|---:|---:|---|
| `RenderLiteral`         | 105.4 | 0 | **0** | a message with no placeholder — the single-literal fast path |
| `RenderOneArgument`     | 183.7 | 32 | 1 | `"Welcome back, {name}"` |
| `RenderThreeArguments`  | 281.4 | 96 | 1 | three substitutions, still one allocation |
| `RenderCountEnglish`    | 188.5 | 24 | 1 | plural select (2 categories) + interpolate |
| `RenderCountPolish`     | 208.0 | 32 | 1 | plural select (4 categories) + interpolate |
| `RenderCountArabic`     | 213.9 | 32 | 1 | plural select (6 categories) + interpolate |
| `RenderThroughFallback` | 279.8 | 24 | 1 | the key is absent from the requested language; the chain walks `fr → en` |
| `RenderMissingKey`      | 604.3 | 280 | 3 | no language holds the key: the key is returned AND a refusal is built |

### Plural selection alone

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PluralSelectPolish` | 4.16 | 0 | **0** |
| `PluralSelectArabic` | 3.10 | 0 | **0** |

### Lookup and negotiation

| Benchmark | ns/op | B/op | allocs/op | Header |
|---|---:|---:|---:|---|
| `StoreLookup`              | 54.4 | 0 | **0** | one exact `(tag, key)` lookup |
| `NegotiateAbsentHeader`    | 3.63 | 0 | **0** | `""` |
| `NegotiateSingleRange`     | 99.3 | 0 | **0** | `fr` |
| `NegotiateBrowserHeader`   | 527.2 | 0 | **0** | `fr-CH,fr;q=0.9,en;q=0.8,de;q=0.7,*;q=0.5` |
| `NegotiateNoMatch`         | 801.7 | 0 | **0** | five weighted ranges, none supported — the worst well-formed case |

### Construction (startup, not per request)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ParseTag` (`zh-Hant-TW`) | 186.9 | 64 | 2 |
| `NewPrinter`              | 296.8 | 240 | 3 |
| `NewStore` (4 languages, 9 entries, 2 counted) | 18 813 | 8 360 | 129 |

## What pprof showed, and what was done about it

The profiles are the reason two of the numbers above look the way they do.
Both were taken with `-cpuprofile`/`-memprofile` and read with
`go tool pprof -top`.

### 1. Negotiation allocated, and does not any more

`strings.Split(header, ",")` built a `[]string` the parse loop read once and
dropped. `pprof` put it at **31.6 % of allocated objects** (`strings.genSplit`,
`-sample_index=alloc_objects`) and **12.5 % of CPU** on that path. Replacing it
with a `strings.Cut` walk over the header in place:

| Benchmark | Before | After |
|---|---|---|
| `NegotiateSingleRange`   | 249.1 ns, 16 B, 1 alloc | **99.3 ns, 0 B, 0 allocs** |
| `NegotiateBrowserHeader` | 855.6 ns, 80 B, 1 alloc | **527.2 ns, 0 B, 0 allocs** |

2.5× and 1.6×, and — the part that matters for a request path — the header
parse now allocates **nothing at all**, so a request carrying an
`Accept-Language` costs the same garbage as one that does not. The parse works
out of a fixed 32-element stack array and sorts it with an insertion sort
rather than `sort.SliceStable`, because handing that array to `sort.Interface`
would move it to the heap to order six elements.

### 2. A render allocates exactly one object, and that one is NOT corrected

`pprof` on `RenderOneArgument` attributes the single allocation to
`strings.Builder.Grow` → `internal/bytealg.MakeNoZero` — that is the returned
string, and there is no version of this API that does not produce one.

The CPU profile of the render path is dominated by map traffic:

```
17.0%  internal/runtime/maps.memHashAES
26.4%  runtime.mapaccess2 (cumulative)
40.3%  core/i18n.MessageValue.Format (cumulative)
46.1%  service/i18n.(*Printer).resolve (cumulative)
```

That is two map lookups per render — `byTag`, then the key — plus one `Args`
lookup per placeholder. **It is left alone, deliberately, and the reason is
written here rather than left for someone to rediscover in a profile of their
own:**

- Flattening `byTag[tag][key]` into one map needs a composite key, which hashes
  the same two things, or a string concatenation, which allocates.
- Caching the per-language message map on the `Printer` would skip the outer
  lookup — but a `Printer` holds the `Catalog` PORT, not the concrete `*Store`,
  and the port is what `pkg/v1/i18n` publishes. Binding the renderer to the
  concrete type to save ~25 ns would trade an ADR 0039 property for a
  micro-optimisation.

A page rendering 200 strings pays roughly 40 µs and 200 small allocations for
its text. That is the number to argue with if it ever matters.

### 3. What the shape of the table says

- **`RenderLiteral` is free of allocation** because a pattern with no
  placeholder compiles to a single literal span whose string IS the catalogue's
  string; `Format` returns it without a builder.
- **Plural selection is 3–4 ns and never allocates**, in every language. Arabic
  — six categories — is *faster* than Polish, because its first three clauses
  are an exact `switch` while Polish computes two moduli. The cost of getting
  plural rules right is not the reason anyone ships English rules everywhere.
- **`RenderThroughFallback` costs ~91 ns more than a direct render**: that is
  one extra `Lookup` that misses. A partially translated catalogue is not a
  performance problem; `Store.Missing` is there because it is a CORRECTNESS
  problem.
- **`RenderMissingKey` is the slowest path by design** — 604 ns and 3
  allocations, because it builds a typed refusal carrying the key and the whole
  chain. It is measured so that a catalogue with a systematic gap has a known
  price rather than a mysterious one.
- **`NewPrinter` is 297 ns and `NewStore` is 19 µs.** Both are startup costs.
  The documented shape is one `Printer` per language, built once and indexed by
  `Tag`; a request then pays a map lookup instead of 297 ns and three
  allocations. The two numbers are here so that advice is a measurement.

## Reproducing

```sh
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./i18n/

# the profiles quoted above
cd internal/service && GOWORK=off go test -run '^$' \
  -bench 'BenchmarkNegotiateBrowserHeader|BenchmarkRenderOneArgument' \
  -benchmem -cpuprofile=/tmp/cpu.prof -memprofile=/tmp/mem.prof -o /tmp/i18n.test ./i18n/
go tool pprof -top /tmp/i18n.test /tmp/cpu.prof
go tool pprof -top -sample_index=alloc_objects /tmp/i18n.test /tmp/mem.prof
```

Bazel runs the same targets; the alloc-lane (`tools/alloc-lane-targets.txt`) is
NOT involved, because this package ships no `//go:build !race` test — the
allocation claims above are ordinary `-benchmem` numbers and carry no rule-12
exclusion of any kind.
