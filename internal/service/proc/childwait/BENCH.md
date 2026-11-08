<!-- updated: 2026-11-03T23:30:00Z -->
# childwait — benchmarks

What the ledger costs a spawn and a sweep, and whether the spawn gate the two
share serialises a supervisor that spawns while it reaps (#943). Measured with
`go test -run '^$' -bench . -benchtime=1s ./service/proc/childwait/` from
`internal/`, on the host of the envelope below.

## 1. The ledger's own cost: nanoseconds, and flat in its size

| workload | ns/op | B/op | allocs/op |
|---|---|---|---|
| `Deliver_NoClaims` — one hand-off: the gate taken exclusively, the lookup | 62.39 | 0 | 0 |
| `Deliver_10kClaims` — the same with 10 000 claims in the ledger | 68.38 | 0 | 0 |
| `Spawn_ClaimOverhead` — the shared gate + one claim around a start that forks nothing, released | 222.9 | 176 | 1 |
| `ReapAny_NoClaims` — one `wait4(-1, WNOHANG)` with nothing to collect | 332.0 | 0 | 0 |

A claim is one map entry: 10 000 of them add 6 ns to a hand-off. The one
allocation of a spawn is the `Claim` itself.

## 2. Spawn and sweep contend for the gate — and it does not show

`SpawnSweep_Contention` forks `/bin/true` through the ledger, reporting the
latency of the spawn (the gate, `os.StartProcess`, the claim) without a reaper
and with one draining `wait4(-1)` in a loop beside it, as `reaper.Start` does
on SIGCHLD. Every child the sweep collects takes the gate exclusively and waits
out the fork in flight.

| row | ns/op (spawn + collect) | p50 spawn | p99 spawn |
|---|---|---|---|
| `no_reaper` | 741 477 | 230 870 ns | 499 630 ns |
| `reaper_running` | 762 267 | 232 583 ns | 517 063 ns |

The p99 with the reaper running is 1.03× the p99 without it — far inside the
2× #943 set as the bound for narrowing the gate. The proposed narrowing (a
spawn generation counter read at the sweep's start, so a delivery waits only
for the forks begun before its `wait4`) is therefore not made: it would trade
ADR 0093's one-lock invariant for a gain this host cannot measure. A host
forking a much larger supervisor (a fork of a large address space holds the
gate longer) should re-run the row before revisiting that.

## Reproducibility envelope

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (4 of 8 visible cores: `GOMAXPROCS=4`) |
| RAM | 15 GiB (balloon 8 GiB) |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Generated | 2026-11-03, one run of `-benchtime=1s`, shared VM under another build |
