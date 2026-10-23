# internal/kernel/swar/

## Purpose

**First-match byte search, a word at a time**: SIMD within a register (SWAR) on
the standard library alone, with an AVX2 form behind `GOEXPERIMENT=simd` on
amd64. Stdlib-only AND generic — a slice, two bytes, a bound; no header, no JSON
escape and no token in a signature — so it passes SDK rule 1. It exists for the
two loops the network path runs per byte today: the HTTP/1 codec looking for a
line end, and the JSON string encoder looking for a byte it must escape.

**Not published** (ADR 0186 §D10, which amends ADR 0159): a program reaches it through the
domains that search, not through `pkg/v1`. Emits **no error codes** — every
function answers with an index or `-1`, and bad input has no meaning to refuse
(a bound above 128 is answered byte by byte) — so the package needs no `PP`
range, no `codeRangeOwners` row and no `//:audit_sources` label.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `gen_decls.go` | `IndexLess`, one call of its body `indexLess`, measured to inline (ADR 0168) |
| `swar.go` | the lane tests (`zeroLanes`, `lessLanes`), `IndexByte2` whole (its body does not inline once `twoScanIndexByte2` is in it, so no `impl:`), the SWAR forms, `twoScanIndexByte2`, and `Set` with `NewSet` and `Set.Index` |
| `simd_other.go` | `//go:build !(goexperiment.simd && amd64)` — the dispatchers for every other build: SWAR, and two `bytes.IndexByte` scans past 64 bytes for `IndexByte2` |
| `simd_amd64.go` | `//go:build goexperiment.simd && amd64` — the AVX2 forms, `hasVector` (`archsimd.X86.AVX2()`, read once) and the same dispatchers choosing between AVX2, the two scans and SWAR |

## The surface

| Function | Finds | Competitor, and what the numbers say (`BENCH.md`) |
|---|---|---|
| `IndexByte2(b, c1, c2)` | the first `c1` or `c2` (pass a byte twice for one) | `bytes.IndexAny` (always slower); two `bytes.IndexByte` scans (the word loop wins below 64 bytes, so past it the function IS two scans) |
| `IndexLess(b, n)` | the first byte below `n` | none in the standard library; the byte loop is 2 to 4 times slower from 16 bytes up, and ahead below 8 |
| `NewSet(first, second, below, upper).Index(b)` | the first byte that is `first`, `second`, below `below`, or — with `upper` — from `0x80` up | none; the table loop `encoding/json` uses is 2 to 2.6 times slower from 64 bytes up on the M1 (1.5 to 2.4 on the noisy x86 core; 2 to 9 with AVX2 from 32), and ahead below 8 |

**Dropped, measured on an M1 and an EPYC: `IndexByte`.** `bytes.IndexByte` is an assembly
loop over 32 bytes at a time and beat the word loop at every length but 8 bytes
on the M1 (NEON), by up to 5 times at 4096. A function that does not win is not
kept. **The M1's answer, confirmed on x86-64**: on an EPYC 7351P (Zen 1, a
noisy shared core, `BENCH.md`) `bytes.IndexByte` is ahead (a tie at 16 in one build) from 16 bytes up in
both builds and the word loop leads only at 8 bytes, by a few ns; under Rosetta
the word loop led the translated assembly between 8 and 64 bytes, which
`BENCH.md` reads as the translator's cost. Its forms live
on, test-only, in `dropped_bench_test.go` and
`dropped_simd_amd64_bench_test.go`: `go test -bench DroppedIndexByte` re-asks the
question on real x86-64 or another Go release, and if the word loop wins there
the function comes back. Until then use `bytes.IndexByte` for one byte.

## Why-this-shape

- **Exact lane tests, never the borrowing form.** `^(((x&0x7F..)+0x7F..)|x|0x7F..)`
  cannot carry out of a lane, so every flag is a real match and no flag depends
  on a neighbour. `(x-0x01..)&^x&0x80..` flags the byte above a true match too;
  only its lowest bit is right, and a loop over its bits reads false hits (the
  fault this package's exhaustive pair test exists to catch). The first match is
  `TrailingZeros64(mask)>>3`: the scan stops at the first hit and never walks the
  bits of a mask.
- **`byte < n` is exact for `n` up to 128 only.** The addend `0x80-n` keeps
  `(x&0x7F)+0x80-n` within one lane, and the lane is below `n` exactly when its
  high bit stays clear and `x` has none. Above 128 no such sum exists; the
  forms read byte by byte instead of returning a wrong answer, and `NewSet`
  records it (`wide`) so a `Set` with such a bound is still correct.
- **Bounds-checked loads only.** `binary.LittleEndian.Uint64(b[i:i+8])` under
  `i+8 <= len(b)`, then the tail one byte at a time. Slicing is checked
  against the CAPACITY, so the loop guard on `len` is what keeps a recycled
  receive buffer's old `"\r\n"` out of the answer — `TestNothingPastTheLength`
  gives every form such a buffer. No `unsafe`. The AVX2 forms read the last
  register at `len(b)-32`, overlapping the one before it, rather than past the
  end.
- **The zero `Set` is the byte `0x00`.** Its `first` and `second` are both 0 and
  its bound is 0; the bound's addend is computed per search from `below`, not
  stored, so the zero value is not accidentally "everything under 128" (a test
  caught that).
- **The vector forms are a bonus, never a requirement.** The AVX2 form is chosen
  at run time (`hasVector`), for slices of 32 bytes or more, and the SWAR form
  is in the same binary. The SDK's submission builds WITHOUT the experiment, so
  the SWAR forms are what ships; the vector forms are held to the same oracle
  and cost nothing when absent. `simd/archsimd` is outside the Go 1 promise and
  may break on a Go upgrade — a lane that builds with `GOEXPERIMENT=simd` shows
  it first.
- **`ClearAVXUpperBits` before every return of an AVX2 form.** In Go 1.27.3 the
  compiler does not emit the `VZEROUPPER` that stops the SSE code which follows
  (the runtime's, the caller's) paying for dirty upper halves; `archsimd` says
  it may one day, and the call is then redundant.

## Tests

| File | What it pins |
|---|---|
| `oracle_internal_test.go` | the scalar per-byte oracle, and the `spec` table that runs the public function, the SWAR form and the AVX2 form (when built and the CPU has it) against it |
| `swar_internal_test.go` | every one of the 256 values at every position of 12 lengths in front of two backgrounds; every ordered pair of adjacent bytes at the lane and register edges (the borrow); the first of many matches; two random differentials; a capacity beyond `len` filled with `"\r\n\r\n"`; empty and nil; the zero `Set`; the lane tests on their own; `FuzzSearch` (`go test -fuzz FuzzSearch`) |
| `forms_*_test.go` | the per-build hooks that give a `spec` its vector form; `TestVectorFormsRunHere` logs whether the run exercised it |
| `swar_external_test.go` | the examples, the bound's edges through the public function, a `Set` shared between goroutines |
| `perf_fixtures_test.go`, `gen_perf_test.go` | the design's budgets: `allocs: 0` for every entry point, race off (the alloc lane) |
| `swar_bench_test.go`, `dropped_bench_test.go` | the figures `BENCH.md` records |

**MUTATIONS** (each failed the suite when applied): the borrowing lane test in
`zeroLanes` failed `TestLaneTestsAreExact`; loading and looping to `cap(b)`
instead of `len(b)` failed `TestNothingPastTheLength`; dropping the AVX2 forms'
last register failed `TestEveryValueAtEveryPosition` at length 32.

## Do NOT

- Add a search for one byte. Measure it against `bytes.IndexByte` first
  (`go test -bench DroppedIndexByte`); the standard library's assembly wins.
- Use a borrowing lane test (`(x-0x01..)&^x&0x80..`) and walk its bits.
- Read through `b[:cap(b)]`, or loop on `cap`: the capacity of a recycled buffer
  holds the last request's bytes.
- Reach for `unsafe`, or for a word load that is not a bounds-checked slice.
- Import `simd/archsimd` from a file without `goexperiment.simd && amd64`, or
  call an AVX2 form without `hasVector` and a length of at least `vectorMin`.
- Put a JSON, HTTP or token word in a signature: the consumers wrap this; it
  does not know them.
