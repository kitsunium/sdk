<!-- updated: 2026-10-25T23:00:00Z -->
# tools/netgate/

## Purpose

Judges the network plan's merge gates (plan P8, R7: a gate is placed at the
merge, never at the release). The `bench` job of
`.github/workflows/net-kernel-gate.yml` runs engine A against engine B on the
loads of `internal/service/net/server` (`BenchmarkLoadL1`, `L2`, `L3`), ten
interleaved rounds on one GitHub-hosted runner; netgate reads that
`go test -bench` output, holds each gate of its table to its threshold on the
bound of benchstat's confidence interval, and writes a report naming the
commit measured.

Stdlib-only, no `go.sum`, outside `go.work` (the `tools/` convention, and an
`auxiliary` module of `design/sdk.yaml`).

## Contents

| File | Role |
|---|---|
| `gates.go` | the table, as data: per step its load, engines A and B, criteria (one metric, one op, one threshold each), `Active`, what it is `Pending` on, P2's allocation budget, and the named 0-alloc exclusions (full queue, setup/Start, error paths) |
| `stats.go` | `summarize`: benchstat's median and distribution-free interval (golang.org/x/perf benchmath, `AssumeNothing`) — order statistics k and n−k+1, k the largest whose binomial(n, ½) tail stays within (1−0.95)/2; the 2nd and 9th of ten. Fewer than 10 samples is refused |
| `parse.go` | `BenchmarkLoad<load>/engine=<label>[-procs]` lines read into samples by load, engine and metric; every other line skipped |
| `eval.go` | the types, and `evaluate`: a ratio criterion bounds B/A by the widest the two intervals allow (`hi(B)/lo(A)` for `<=`, `lo(B)/hi(A)` for `>=`); a missing or short side is `missing`; an active gate that does not pass fails the report; an inactive gate reports what it measured and never fails |
| `main.go` | the CLI: `-in`, `-sha` (required: no report without its commit), `-out`, `-list`; exit 0, 1 (an active gate failed), 2 (usage, unreadable input) |
| `main_test.go` | the interval against benchstat's own output on a fixed set (100.5 ± 9 %), k for n = 10 and 20, the parser, each gate of the table turned on and held to its threshold — passing, failing on the bound with the median on the threshold, missing a side, nine samples —, the shipped table — P2 active, passing on like samples and failing without them or 10 % dearer, every other row inactive, and the CLI |

## The table

| Step | Load | A → B | Criterion (bound) | State |
|---|---|---|---|---|
| P2 | L2 | handwritten → chain | `cpu-ns/req` ratio ≤ 1.05 (upper); 0 allocations as a kit budget, not a bench metric (the four http1 links' `ServeHTTP1`) | **active** (ADR 0189): `chainL2` is `http1.Routes` then `http1.Bearer` |
| P4a | L3 | recvmmsg → gro | `datagrams/syscall` ratio ≥ 4 (lower); `cpu-ns/datagram` ratio ≤ 0.75 (upper) | inactive until P4a |
| P6 | L1 | goroutine → loop | `cpu-ns/req` ratio ≤ 0.70 (upper) | inactive until P6 |
| P5 | L1 | published → loop | `cpu-ns/req` ratio ≤ 1.0 (upper) | inactive: the published http1's samples are not stored |

P0, P1 and P3 have functional gates only: the lanes of the same workflow.

## Why-this-shape

- **The bound, not the median.** A gate passes when the worst the interval
  allows still meets the threshold, so a step whose median merely touches it
  fails; the tests pin that case for every row.
- **The interval is computed here, not by running benchstat.** It is
  `golang.org/x/perf`, a dependency this tree cannot take; the order-statistic
  interval is a dozen lines, and the test holds it to benchstat's printed
  output.
- **Allocations are not a bench metric.** The bench's server is a child
  process, and a process-wide `Mallocs` delta counts the runtime's own
  allocations (measured: about 0.00035 a request) — which
  `scripts/pre-commit/check-alloc-lane-coverage.sh` refuses in any
  hand-written test. P2's "allocs 0" is therefore a kit budget of the chain,
  counted on its own stack by the race-off alloc lane, named in the table.
- **Inactive, not absent.** A step that is not built has its row, its
  harness running in CI and its numbers in every report, so turning a gate on
  is setting `Active` in the step's own pull request, not writing the
  mechanism then.

## Turning a gate on

In the pull request that builds engine B: give the bench its `engine=<B>`
sub-benchmark, set `Active: true` on the row, and keep the threshold the
plan's. P5 also needs engine A: the published http1's L1 samples, measured on
the same runner in the same job (a checkout of the last release's tag built
beside the head) — not built here.

## Verify

```sh
cd tools/netgate && GOWORK=off go test ./...
GOWORK=off go run . -list
GOWORK=off go run . -in bench.txt -sha "$(git rev-parse HEAD)"
bazel test //tools/netgate:all
```
