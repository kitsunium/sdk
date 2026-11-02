# ADR 0206 — an io_uring batch may be bounded in time

- **Status**: Accepted
- **Date**: 2026-11-02
- **Accepted**: 2026-11-02, on the diagnosis of the single-vCPU benchmark's
  softirq cliff at 180,000 connections (S22, kernel 7.3.1) and the
  measurements below. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)).
- **Deciders**: SDK maintainers
- **Amends**: nothing published but a new `LoopValue` field and a new
  `LoopRingStateValue` counter.
- **Related**: [ADR 0199](0199-a-loaded-io-uring-loop-groups-its-wait-and-a-native-loop-yields-to-a-spare-p.md)
  and [ADR 0202](0202-a-loop-batch-pays-its-fixed-cost-once-and-a-grouped-wait-starts-on-the-request-rate.md)
  (the grouped wait, and the fixed cost of a batch);
  [ADR 0203](0203-an-io-uring-loops-rings-fill-in-order-and-a-rings-buffers-follow-its-table.md)
  (rings filled in order)

## Context

**The cliff.** The cliff starts with a wave of delayed ACKs. Once an answer
takes longer than about 40 ms, the peer's request is acknowledged by the
kernel's delayed-ACK timer instead of riding on the answer. Linux bounds
that delay by max(srtt, `TCP_DELACK_MIN`), which is HZ/25: 40 ms on a short
RTT (net/ipv4/tcp_output.c `tcp_send_delayed_ack`, include/net/tcp.h, v7.0).
Each such timer sends a bare ACK, which costs softirq time. That slows the
loop, more answers pass 40 ms, and the effect feeds itself: softirq goes
from 30 % to 70 %. At 180,000 connections the normal p99, 20 to 60 ms,
already sits at the threshold.

**When an answer leaves.** A `Write` only copies the answer into the
connection's output and marks the slot (core.go `Write`, `markDirty`). The
output becomes a send SQE in `endBatch`, after `OnBatchEnd`. That SQE is
submitted at the next `io_uring_enter`, the wait at the head of the next
batch. So an answer waits for its whole batch, and a batch is bounded only
by count: `reapRounds` × `CQBatch` (4 × 512) completions a ring. With three
rings filled in order that is up to about 6,000 completions; at 30 to
60 µs each on one vCPU, 180 to 370 ms.

**Rejected levers.** `TCP_QUICKACK` would send a bare ACK for every
request, slow or not (`__tcp_ack_snd_check`), and it must be re-armed after
every receive. `TCP_DELACK_MAX_US` (6.15) can only lower the delay. Neither
helps.

## Decision

### D1 — `LoopValue.BatchBudget` bounds a batch's reaping in time

With a positive `BatchBudget`:

- **Smaller reaps.** Each ring reaps `budgetChunk` (16) completions at a
  time instead of `CQBatch`, for the same share per batch. Between two
  reaps it reads the monotonic clock once: a few dozen nanoseconds against
  16 completions served.
- **The cut.** Once the batch has run for its budget, the ring stops
  reaping. If it still had completions, the batch is cut: the rings after
  it are not reaped either, and the cut is counted in that ring's
  `LoopRingStateValue.BatchesCut`. If the reap that found the budget spent
  emptied its ring, the batch goes on to the next rings, each reaping at
  most one more lot of 16 before it stops: the overrun is bounded, not a
  hard stop.
- **The next batch.** It starts with the ring the cut stopped at, so ring
  0 is not always served first. It does not wait: it only submits what is
  pending. A secondary's leftover completions would otherwise sleep until
  the next tick, since its eventfd already fired.

The handlers, `OnBatchEnd` (an application's transaction) and the flush
then run on the shorter batch.

Zero or a negative value bounds nothing, and the batch is the one it has
always been. The epoll and portable backends ignore the field. Answers are
still sent only after `OnBatchEnd`: sending mid-batch would answer before
an application's commit, and is not done.

### D2 — A long batch hands its answers over at once

A batch that ran past half its budget submits every ring's pending SQEs
right after `endBatch`, before its buffers go back, instead of at the next
wait. A shorter batch, and every batch without a budget, makes no extra
system call (ADR 0202).

## Consequences

Measured in Docker, linux/arm64, kernel 7.0, two CPUs:

- **Burst latency** (`BenchmarkURingBatchBudget`, 6 runs): a held burst of
  400 lines, each costing 100 µs to serve, on one ring, from the release:

  | budget | first echo | median | last |
  |---|---|---|---|
  | none | 41.5 ms | 42.0 ms | 42.6 ms |
  | 1 ms | 2.3 ms (−94.5 %) | 23.0 ms (−45.2 %) | ~ (p=0.589) |
  | 2 ms | 3.8 ms (−90.9 %) | 24.1 ms (−42.7 %) | ~ (p=0.818) |
  | 5 ms | 6.9 ms (−83.4 %) | 27.0 ms (−35.8 %) | ~ (p=0.699) |

  The burst's total work does not change, so its last answer does not
  either. Every earlier answer leaves within about a budget of its batch.
  For the cliff this is the point: no answer waits behind thousands of
  others.
- **No budget, no change.** `BenchmarkURingRings_fill` against main, 20
  interleaved runs: ~ (p=0.211 one ring, p=0.304 three).
- **The cost.** More batches under a burst means more `OnBatchEnd` calls,
  and so more transactions for an application that commits there. The
  budget trades that for latency; the box measures the setting.
- `BatchesCut` shows the cuts in `State`.

## Breaking changes

None. The new field defaults to zero, which is the former behaviour.

## Alternatives considered

- **Sending answers as the batch goes** — refused for now: an answer would
  leave before `OnBatchEnd`, so before an application's transaction. It
  would need the application's explicit opt-in.
- **Lowering `CQBatch`** — available without code, and the benchmark's
  application tries it first. It bounds the count, not the time: a batch
  of slow requests still grows with their cost.
- **`TCP_QUICKACK`, `TCP_DELACK_MAX_US`** — refused, as the Context says.

## References

- net/ipv4/tcp_output.c `tcp_send_delayed_ack`, `tcp_delack_max`;
  net/ipv4/tcp_input.c `__tcp_ack_snd_check`, `tcp_event_data_recv`;
  net/ipv4/tcp.c `__tcp_sock_set_quickack`, `TCP_DELACK_MAX_US` — tag v7.0.
- `internal/service/net/loop/budget_uring_linux_test.go`
  (`TestURingBudget_reapShape`, `TestURingBudget_cutsBatches`,
  `BenchmarkURingBatchBudget`).
