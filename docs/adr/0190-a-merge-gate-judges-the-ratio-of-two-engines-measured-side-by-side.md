# ADR 0190 — a merge gate judges the ratio of two engines measured side by side

- **Status**: Accepted
- **Date**: 2026-10-26
- **Accepted**: 2026-10-26, on the owner's decision of 2026-10-26: the
  thresholds of the network plan stay as decided, P2's 5 % included, and
  what changes is how they are measured — engines A and B in one child,
  alternating, the gate judging each round's ratio with its interval. It can
  be revisited while the SDK is v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0187](0187-the-handler-type-chooses-the-engine.md) §D10
  (a gate at the merge: how a ratio gate is measured and judged)
- **Related**: [ADR 0186](0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md),
  [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md)

## Context

The network plan's ratio gates (P2, P4a, P5, P6, `tools/netgate`) ran each
engine in its own child, ten rounds interleaved, and bounded B/A by the widest
the two intervals allow, `hi(B)/lo(A)`. On a GitHub-hosted runner one engine's
CPU a request moves by 5 to 8 % from one round to the next. Two series of the
**same** engine then bound their ratio at 1.077 (run 40191639461), and P2's
chain, whose medians matched the hand-written handler's within 1 to 3 %,
failed its 5 % twice (1.089, 1.107). The gate judged the runner, not the
engines: with that method a 5 % threshold could not be met by any code.

## Decision

### D1 — A round is one paired sample

Engines A and B run in **one** child, side by side, each on its own listener
or socket. A round sends its load in `2 × 8` windows, A, B, B, A, A, B, …, the
child's CPU read at each window's ends: a drift of the runner during the round
lands on both engines, and its linear part cancels in the order. The round
reports each engine's metric and their ratio, B over A, as
`BenchmarkLoad<load>/pair=<A>:<B>` with units `a:<metric>`, `b:<metric>` and
`ratio:<metric>`. The workflow still runs ten rounds; a pair is 400 000
requests a round (L1, L2), or datagrams (L3), so a window is 25 000.

### D2 — The gate judges the interval of the ratios

A ratio criterion is held on benchstat's distribution-free interval of the ten
ratios themselves (the 2nd and 9th order statistics at n = 10, 95 %), its
upper bound for `<=`, its lower bound for `>=`. The engines' own values are
reported beside it and never decide: a runner that moves both by 25 % while
their ratio stays within 1 % passes a 5 % gate (`TestEvaluate_pairedCancelsTheRunner`).
The thresholds are the plan's, unchanged.

### D3 — The calibration is a gate of its own

Each load carries an active calibration row, one engine against itself —
`cal-L1` (`loop:loop`), `cal-L2` (`handwritten:handwritten`), `cal-L3`
(`recvmmsg:recvmmsg`) — whose ratios must stay within the tightest threshold
that load is judged at: 5 % either way on L1 and L2 (P2's; P5's 1.0 is a
non-regression met only below 1), 25 % on L3 (P4a's). A runner on which one
engine does not match itself fails the run, rather than letting a step pass or
fail on the runner. Every step's row stays inactive until its engine B lands,
as before.

## Consequences

- `tools/netgate` reads only `pair=` lines; the `engine=` shape is gone.
- `internal/service/net/server`'s loads serve two engines in one child
  (`TestLoadChild` takes `L2:a,b`, `TestLoadL3Child` `L3:a,b`); an engine B is
  added by its step to the load's handler switch (`serveLoad`) or to
  `l3Sources`, and to its pair's sub-benchmark.
- The bench job runs about four times the requests it did; still within its
  30 minutes.
- An idle engine's background cost lands in the other's windows, alike for
  both, so it moves the ratio of neither.

## Alternatives considered

- **Keep separate children and widen n to 20.** Rejected by the owner: the
  runner's drift between children stays in the bound; more samples narrow the
  interval of each engine, not the gap the drift opens between them.
- **Raise the thresholds.** Rejected by the owner: the plan's 5 % is the
  property P2 promises.

## References

- Plan P8 §Gates; `tools/netgate/`, `.github/workflows/net-kernel-gate.yml`,
  `internal/service/net/server/load_gate_linux_test.go`,
  `internal/service/net/server/load_l3_linux_internal_test.go`.
