<!-- generated from internal/service/app/consensus/multi/multi_bench_test.go — run `cd internal && go test -c -o /tmp/m.test ./service/app/consensus/multi/ && for i in 1 2 3 4 5 6 7 8; do /tmp/m.test -test.run '^$' -test.bench BenchmarkProposeCallers -test.benchtime=200000x; done` to refresh -->
# Benchmarks — `internal/service/app/consensus/multi`

The gate of #926 (ADR 0152 D6, D19): 1 000 concurrent callers propose
commands of 64 bytes to the leader of a three-node cluster over the memory
transport and the memory log. `unpacked` proposes each command as its own
entry through `Node.Propose`, the engine already taking every waiting
proposal of a turn into one Append; `packed` proposes through a `Packer`
(`MaxInFlight` 1, the default) to nodes running a `Machine`. Every
caller gets its own result in both runs
(`TestAPackedEntryAppliesOnEveryNodeWithItsResultsInCallerOrder`).

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. The box was shared
> with other builds while it ran (load average 18 to 50 on 10 cores): read
> the absolute rates as an order of magnitude, the ratio of a pair as the
> figure the gate judges.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | Darwin 25.6.0 |
| Architecture       | arm64 |
| Go toolchain       | go1.27.3 darwin/arm64 |
| Git branch         | `feat/sdk-lot-1-c-loop` |
| Git commit         | `8219ffc28` (pre-commit) |
| Generated (UTC)    | 2026-11-12 |
| Bench wall-clock   | `-test.benchtime=200000x`, 8 rounds, each round `unpacked` then `packed` in one process |
| Fixture            | 3 voters, `DurabilityVolatile`, memory network, 1 000 callers, 64-byte commands |

## Packed against unpacked, paired by round

| Round | unpacked cmds/s | packed cmds/s | ratio | packed entries/s |
|---|---:|---:|---:|---:|
| 1 | 288 051 | 1 270 156 | 4.41 | 1 689 |
| 2 | 318 923 | 1 197 655 | 3.76 | 1 725 |
| 3 | 231 283 | 1 041 961 | 4.51 | 1 693 |
| 4 | 289 763 | 1 124 226 | 3.88 | 1 793 |
| 5 | 227 199 |   810 161 | 3.57 | 1 446 |
| 6 | 321 681 | 1 257 090 | 3.91 | 1 666 |
| 7 | 293 178 | 1 299 295 | 4.43 | 1 754 |
| 8 | 319 269 | 1 147 916 | 3.60 | 1 687 |

The packed run commits 3.57 to 4.51 times the commands per second of the
unpacked run, 3.9 at the median, on the same box in the same round: the
gate's 3x holds in every round. Unpacked, an entry is a command (its
entries/s equal its cmds/s); packed, some 700 commands share an entry. Command
bytes per second follow the commands (64 bytes each): 14.5 to 20.6 MB/s
unpacked, 51.9 to 83.2 MB/s packed.

More in flight did no better on this box: an entry proposed while another
is in flight takes fewer commands. In two rounds of one process each, one in
flight ran at 1.05 and 1.09 million commands per second, two at 0.51 and
1.02, four at 0.82 and 1.26, eight at 0.29 and 1.00, the spread of a shared
box rather than a gain.
