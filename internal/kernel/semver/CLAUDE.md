# internal/kernel/semver/

## Purpose

**SemVer 2.0.0 precedence and Go pseudo-versions**, with Go's leading `v`: the
kernel primitive that replaces `golang.org/x/mod` in the SDK (ADR 0156 §4 —
on the reorganisation series). Stdlib-only AND generic: every function takes a
version string and answers about it — no release, no update, no product in any
signature — so it passes SDK rule 1 like `heap` and `pathchain` did.

Its consumers are `internal/service/proc/self` (pseudo-versions, in place of
`x/mod/module`) and, through the public alias `pkg/v1/data/semver`, the distribution
mechanisms (`selfupdate`, `entitlement`), which live in the framework (ADR 0158)
and reach the SDK only through `pkg/v1` (ADR 0147). With them switched, no
`go.mod` of the SDK or the framework requires `golang.org/x/mod` any more.

Emits **no error codes**: every function answers with a value or a `bool`, as
x/mod's do, so the package needs no `PP` range, no `codeRangeOwners` row and no
`//:audit_sources` label.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `semver.go` | `IsValid`, `Compare`, `Prerelease` |
| `pseudo.go` | `IsPseudoVersion`, `PseudoVersionRev`, `PseudoVersionTime`; the stamp and where it may sit |
| `parse.go` | the one-pass parser (`components`, `parse`, `identifiers`) and the precedence helpers |

## The surface is the six functions the tree calls

`IsValid`, `Compare` and `Prerelease` are what `selfupdate` and `entitlement`
call on `x/mod/semver`; `IsPseudoVersion`, `PseudoVersionRev` and
`PseudoVersionTime` are what `proc/self` called on `x/mod/module`. Names and
semantics are x/mod's, so a call site moves by changing its import — with one
deliberate difference: the two pseudo-version readings answer `(value, bool)`
where x/mod answered `(value, error)`, because the kernel has no error budget
for "this string is not a pseudo-version" and nobody branched on x/mod's
reasons.

**Not provided, on purpose:** `Canonical`, `Major`, `MajorMinor`, `Build`,
`Sort`, `Max`. Nothing calls them; `pkg/v1/data/semver` publishes this surface, and a
published function is frozen at v1 while an added one breaks nobody.
`Canonical`, `Major` and `MajorMinor` would also need a `(string, bool)` shape
here — x/mod's `""`-for-invalid is exactly what `KTN-FUNC-EMPTYSENTINEL`
refuses. Sorting is `slices.SortFunc(list, semver.Compare)`.

## Why-this-shape

- **Invalid input is an answer.** `Compare` orders every invalid string below
  every version and equal to every other invalid string — x/mod's contract,
  which keeps `Compare` a total order a sort can use. `Prerelease` of an invalid
  string is `""`, as it is of a release: `""` is the value "no pre-release",
  not a sentinel, and `IsValid` is the question that tells the two apart.
- **Numbers are never converted.** They are compared as decimal strings — the
  longer is the larger, then digit by digit — so `v1.2.18446744073709551616`
  orders above `…551615` where a `uint64` would overflow.
- **A pseudo-version is recognised by where its stamp sits**, in the
  pre-release `parse` has already cut: the LAST identifier is
  `yyyymmddhhmmss-REVISION`, and it is either the whole pre-release of a
  `vX.0.0` or preceded by an identifier `0`. That is the toolchain's own pattern
  (`x/mod/module`'s `pseudoVersionRE`) restated over identifiers, and the suite
  proves the two agree on every near miss. It recognises the SHAPE: a stamp
  whose digits are no instant (a thirteenth month) is still a pseudo-version,
  and `PseudoVersionTime` is what says the time does not read — as x/mod did.
- **One pass, no copy.** Every reading is a substring of its input; the parser
  classifies each byte once. `strings.Cut` per identifier cost 2–3× x/mod's time
  in call overhead alone (`BENCH.md`).
- **Independent of x/mod's source.** Written from SemVer 2.0.0 and the Go
  modules reference, not translated from x/mod; what was taken from x/mod is
  its TEST VECTORS, written out and attributed in the suite, so the two are
  shown to agree rather than assumed to.

## Tests

| File | What it pins |
|---|---|
| `semver_external_test.go` | x/mod's own `tests` table (validity, pre-release, the 1 024 pairwise comparisons, its golden sort order), SemVer §11's example chains, and the grammar's edges — leading zeros, empty identifiers, bytes outside `[0-9A-Za-z-]`, numbers past `uint64` |
| `pseudo_external_test.go` | x/mod's own `pseudoTests` table both ways, the shape boundary (where the stamp may sit, fourteen digits, a hyphen-free revision), a time that is no instant, and the ordering that puts a pseudo-version between its tags |
| `oracle_external_test.go` | differential tests against three oracles written from the documents — the regular expression SemVer 2.0.0 publishes, the toolchain's pseudo-version pattern, and §11 over `math/big` — on ~23 000 one-edit mutants of every seed, plus three fuzz targets |
| `parse_internal_test.go` | the one-comparison `isDigit` / `isLetter` against their plain ranges, over all 256 bytes |
| `perf_fixtures_test.go` | `//go:build !race` — the fixtures of the design's budgets (`allocs: 0` on every function, ADR 0165): one call each, over every branch that could allocate |
| `perf_gen_test.go` | written by kit gen from those budgets: each fixture's call counted over 30 000 calls after as many warm-up calls; run by the alloc lane only (rule 12, kit's section of `tools/alloc-lane-targets.txt`) |
| `semver_bench_test.go` | the figures in `BENCH.md` |

## Do NOT

- Add `Canonical`, `Major`, `MajorMinor` or `Build` in x/mod's
  `""`-for-invalid shape. When something needs one, it is `(string, bool)`, and
  `pkg/v1/data/semver` forwards it in the same change.
- Import `golang.org/x/mod` here or in this suite. The vectors are written out
  so the comparison holds without it; the out-of-tree harness `BENCH.md`
  describes is where x/mod is measured.
- Read a pseudo-version with a regular expression, or re-split the version
  string. The stamp is an identifier `parse` already found.
- Return an error. The kernel has no error budget for "not a version"; a `bool`
  says it and costs no code range.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=1 ./semver/
go test -count=1 -run TestPerfAllocs ./internal/kernel/semver/   # the alloc gate, race off
cd internal/kernel && GOWORK=off go test -run '^$' -fuzz '^FuzzCompare$' -fuzztime 60s ./semver/
bazel test --config=race //internal/kernel/semver:semver_test
bazel test --config=alloc //internal/kernel/semver:semver_test
```
