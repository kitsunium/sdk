<!-- updated: 2026-11-13T12:00:00Z -->
# tools/netgate/

## Purpose

Judges the network plan's merge gates (plan P8, R7: a gate is placed at the
merge, never at the release). The `bench` job of
`.github/workflows/net-kernel-gate.yml` runs engine A against engine B on the
loads of `internal/service/net/server` (`BenchmarkLoadL1`, `L1big`, `L1h2`, `L2`,
`L3`, then `Sparse` once GRO is on on the veth pair's far end), paired
(ADR 0190): one child serves both engines, a round alternates windows A, B, B,
A, … and reports their ratio, ten rounds on one GitHub-hosted runner. netgate
reads that `go test -bench` output, holds each gate of its table to its
threshold on the bound of benchstat's confidence interval of the rounds'
ratios, and writes a report naming the commit measured.

Stdlib-only, no `go.sum`, outside `go.work` (the `tools/` convention, and an
`auxiliary` module of `design/sdk.yaml`).

## Contents

| File | Role |
|---|---|
| `gates.go` | the table, as data: per step its load, engines A and B, criteria (one metric, one op, one threshold each, `Reported` for one judged and written but never settling the gate), `Active`, what it is `Pending` on, P2's allocation budget, and the named 0-alloc exclusions (full queue, setup/Start, error paths) |
| `stats.go` | `summarize`: benchstat's median and distribution-free interval (golang.org/x/perf benchmath, `AssumeNothing`) — order statistics k and n−k+1, k the largest whose binomial(n, ½) tail stays within (1−0.95)/2; the 2nd and 9th of ten. Fewer than 10 samples is refused |
| `parse.go` | `BenchmarkLoad<load>/pair=<A>:<B>[-procs]` lines read into samples by load, pair and unit — `a:<metric>`, `b:<metric>`, `ratio:<metric>`; every other line skipped, the former `engine=` shape included |
| `eval.go` | the types, and `evaluate`: a ratio criterion holds the bound of the interval of the rounds' `ratio:` samples (`hi` for `<=`, `lo` for `>=`), A's and B's own values summarised beside it, never deciding; a criterion of B alone reads `b:`; a missing or short pair is `missing`, a ratio at or below zero too; a `Reported` criterion is judged and written, never settling its gate; an active gate that does not pass fails the report; an inactive one reports what it measured and never fails |
| `main.go` | the CLI: `-in`, `-sha` (required: no report without its commit), `-out`, `-list`, `-only step,step` (`selectGates`: the rows named alone, in the table's order — the darwin job judges `kqueue,cal-L1-kqueue` on its own samples, where the Linux rows are absent by design; a name the table lacks is bad usage); exit 0, 1 (an active gate failed), 2 (usage, unreadable input, an unknown step) |
| `main_test.go` | the interval against benchstat's own output on a fixed set (100.5 ± 9 %), k for n = 10 and 20, the parser, each gate turned on and held to its threshold on its ratios — passing, failing on the bound with the median on the threshold, a noisy pair, nine rounds, a missing pair —, `TestEvaluate_pairedCancelsTheRunner` (engines moving 25 % together, their ratio within 1 %, pass 5 %), the shipped table — calibration, P2, P4a, P6 and uring-recvmsg active, P6 failing at −25 %, a run without P4a's rounds failing, every other step inactive —, and the CLI, and `-only` (`TestRun_only`: the darwin rows judged alone, the whole table failing on them) |

## The table

| Step | Load | A → B | Criterion (bound) | State |
|---|---|---|---|---|
| P2 | L2 | handwritten → chain | `cpu-ns/req` ratio ≤ 1.05 (upper); 0 allocations as a kit budget, not a bench metric | **active** (ADR 0191): `chainL2` is `http1.Routes` then `http1.Bearer` |
| P4a | L3 | recvmmsg → gro | `datagrams/syscall` ratio ≥ 4 (lower), **reported, not gated** (ADR 0192 §D7); `cpu-ns/datagram` — the receiver's and the sender's rusage plus /proc/stat's irq and softirq time over the window (ADR 0192 §D6) — ratio ≤ 0.75 (upper) | **active** |
| P6 | L1 | goroutine → loop | `cpu-ns/req` ratio ≤ 0.70 (upper) | **active** (ADR 0196 §D5): L1's loop engine runs `LoopsAuto`, one loop a P but one (ADR 0199) |
| P5 | L1 | published → loop | `cpu-ns/req` ratio ≤ 1.0 (upper) | inactive: the published http1's samples are not stored |
| cal-L1 | L1 | loop → loop | `cpu-ns/req` ratio within 5 % of 1, both bounds | **active**: the runner can be judged at P2's 5 % |
| cal-L2 | L2 | handwritten → handwritten | the same | **active** |
| cal-L3 | L3 | recvmmsg → recvmmsg | `cpu-ns/datagram` and `datagrams/syscall` within 25 % of 1 (P4a's) | **active** |
| P9 | L1h2 | goroutine-h2 → loop-h2 | `cpu-ns/req` ratio ≤ 0.70 (upper), P6's | inactive (#963): measured on the runner first |
| cal-L1h2 | L1h2 | loop-h2 → loop-h2 | `cpu-ns/req` ratio within 5 % of 1, both bounds | inactive (#963): three runner rounds within 0.952–1.05 first |
| h1-h2 | L1h2 | loop → loop-h2 | `cpu-ns/req` ratio ≤ 1.0, reported | inactive (#963): HTTP/2 against HTTP/1.1 on the loop, no threshold until measured |
| uring-recvmsg | L3 | recvmmsg → uring-recvmsg | P4a's: `cpu-ns/datagram` ratio ≤ 0.75 (upper); `datagrams/syscall` (a batch's, one `io_uring_enter` each) ≥ 4, reported | **active** (#852, 2026-11-13): upper bound 0.347, 0.280 and 0.299 on three runs of the runner before it was turned on |
| shards | L3 | recvmmsg → recvmmsg+shards | `cpu-ns/datagram` ratio ≤ 1.25, reported | inactive (#977): `Shards(0)` becomes the default only on 1.8× the datagrams/s at saturation, which L3's fixed rate does not measure |
| send-zc | L1big | loop → loop-sendzc | `cpu-ns/req` ratio ≤ 0.9, reported | inactive (#969): `uring-send-zc` leaves class A-code only through a `net:default-change` pull request citing it |
| files | L1big | goroutine-static → loop-files | `cpu-ns/req` ratio ≤ 0.70, reported | inactive (#982) |
| busy-poll | Sparse | loop → loop-busypoll | `p99-ns` and `cpu-ns/req` ratios ≤ 1.0, reported | inactive (#979): its ADR sets the thresholds from the veth lane's rows |
| kqueue | L1 (macos-15) | goroutine → loop-kqueue | P6's: `cpu-ns/req` ratio ≤ 0.70 | inactive (#976): the `bench-darwin` job, judged with `-only`; `auto` prefers kqueue only once it holds |
| cal-L1-kqueue | L1 (macos-15) | loop-kqueue → loop-kqueue | `cpu-ns/req` within 5 % of 1, both bounds | inactive (#976): the darwin runner held within 1.05 of itself first |

P0, P1 and P3 have functional gates only: the lanes of the same workflow.

## Why-this-shape

- **A round is paired** (ADR 0190). Two children measured one after the other
  moved by 5 to 8 % on the runner, so two series of the same engine bounded
  their ratio at 1.077 and a 5 % gate judged the runner. One child serving
  both, windows A, B, B, A, …, puts every drift on both engines; the gate
  reads the interval of the ten ratios, not two intervals divided.
- **The calibration is a gate.** One engine against itself, active on every
  load: if the runner cannot hold one engine within the tightest threshold of
  its load, the run fails as such, rather than a step on its account.

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

In the pull request that builds engine B: serve it in the load's child
(`serveLoad` in `load_gate_test.go`, or `l3Sources` for L3), give the
load its `pair=<A>:<B>` sub-benchmark, set `Active: true` on the row, and keep
the threshold the plan's. P5 also needs engine A, the published http1, served
in the same child as the head's — not built here.

## Verify

```sh
cd tools/netgate && GOWORK=off go test ./...
GOWORK=off go run . -list
GOWORK=off go run . -in bench.txt -sha "$(git rev-parse HEAD)"
GOWORK=off go run . -in bench-darwin.txt -sha "$(git rev-parse HEAD)" -only kqueue,cal-L1-kqueue
bazel test //tools/netgate:all
```
