<!-- generated from internal/service/proc/cgroup/cgroup_linux_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/cgroup/` to refresh -->
# Benchmarks — `internal/service/proc/cgroup`

## What could not be measured, and why — read this first

**Every verb this package exists for is unmeasurable in the container these
numbers were taken in, and this report does not pretend otherwise.**

`Create`, `Add`, `SetMemoryMax`, `SetCPUMax`, `SetPidsMax`, `SetIOMax`, `Kill`,
`Freeze`, `Thaw` and `Delete` are all a `mkdir(2)` or a `write(2)` against
cgroupfs, and cgroupfs is where the cost lives — the kernel parses each value in a
controller-specific handler and applies it to a live hierarchy. Measuring that
needs a **delegated** cgroup v2 subtree.

What this host actually has, verified rather than assumed:

| | |
|---|---|
| cgroup v2 mounted | **yes** — `cgroup2 on /sys/fs/cgroup (rw,nosuid,nodev,noexec,relatime,nsdelegate)` |
| controllers enabled | **all** — `cpuset cpu io memory hugetlb pids rdma misc` |
| `mkdir /sys/fs/cgroup/<name>` | **EACCES** |
| `mkdir` inside this process's own login-session scope (`…/session-NNNN.scope`, owned by root) | **EACCES** |
| `mkdir` inside a scope the user manager made (`systemd-run --user --scope`, under `user@UID.service`) | not tried on that box; **succeeds** on the 2026-11-08 box of §4 |
| `cgroup.Available()` | **false** |

So: the hierarchy is right there, fully equipped, and delegated to nobody. This is
the ordinary unprivileged-container case the package is explicitly designed to
degrade on, and it is the case in which the live paths cannot be timed. **That
was true of that container, not of every host the package runs on**: a login
session's scope is root's, but a scope started with `systemd-run --user --scope`
sits under `user@UID.service`, which the manager delegates to the user, so the
caller can `mkdir` in it while `cgroup.Available()` — which asks about the mount
root — still answers false. §4 measures the live verbs there.

**No live row was faked.** `BenchmarkCreate_Unavailable` refuses to run at all if
the mount root *is* delegated, so it can never be mistaken for a success-path
number, and the live rows of §4 skip, with their reason, where no root is. §§1–3
measure three real things on that container: the probe, the pure path and value
construction, and the SDK's share of a controller write isolated against a plain
filesystem.

**The missing numbers are §4's**: the live `Create`/`Add`/`Set*Max`/`Kill`/`Delete`,
measured in a delegated scope and set against §3's floor. The difference is
cgroupfs and nothing else.

## 1. `Available()` is two syscalls and 5.9 µs, and it is honest on purpose

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Available` — `stat` the marker, then really try `MkdirTemp` | **5 868** | 511 | 8 |
| `Create_Unavailable` — `stat`, `mkdir` → EACCES, typed wrap | 5 460 | 640 | 9 |

The probe does not read a config file or trust a mount table: it creates a
throwaway directory under the root, because that is the only test of *delegation*
that cannot false-positive. On this host that `mkdir` is refused, and the refusal
costs ~5 µs — **cgroupfs is roughly 2.5× the cost of the same failing operation on
tmpfs**, because the kernel takes the cgroup mutex before it can tell you no.

`Create_Unavailable` at 5 460 ns is the same shape, and the two agree, which is
the cross-check that the rows are measuring the filesystem rather than the
benchmark.

**The actionable consequence: `Available()` is not a predicate to call per
spawn.** It is 5.9 µs, eight allocations, and two syscalls to be told the same
thing it said last time. Call it once at wiring time and remember the answer. (For
scale, a whole process launch is ~626 µs — so a per-spawn `Available()` is 0.9 %
of a launch to answer a question whose answer cannot change without a remount.)

## 2. The pure half is free

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `MaxOrValue_Unlimited` — a negative writes the literal `"max"` | **2.831** | 0 | 0 |
| `ApplyOptions_None` | 23.84 | 16 | 1 |
| `ApplyOptions_WithRoot` | 27.14 | 16 | 1 |
| `MaxOrValue_Number` — `strconv.FormatInt` | 43.67 | 16 | 1 |
| `ValidateName_Valid` | 47.66 | 0 | 0 |
| `WithinRoot_Inside` | 180.1 | 24 | 1 |
| `ValidateName_Traversal` — the refusal | 279.4 | 216 | 3 |

`validateName` is **47.7 ns and zero allocations** on the accepted path — four
string comparisons, a separator scan, and a `filepath.Clean` round trip. The
refusal costs 279 ns because it builds the typed `INVALID_SPEC` with the offending
name attached; a crafted name can therefore make the call ~6× more expensive and
0 % more successful, which against a ~5 µs cgroupfs operation is not a lever.

`withinRoot` at 180 ns is the defence-in-depth check after the join, and it
allocates 24 B for the `root + PathSeparator` concatenation. It runs once per
`Create`.

The whole pure half sums to under 500 ns against a `Create` that is at minimum
5 460 ns here — and much more on a host that actually creates the directory.

## 3. The SDK's share of a controller write: **≈ 6 %**

This is the one number this container *can* establish, and it is the one a future
delegated run needs.

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RawWriteFile_Baseline` — `os.WriteFile`, no SDK code in frame | 8 551 | 152 | 3 |
| `WriteController_TmpfsNotCgroupfs` — `SetMemoryMax(1<<30)` | 9 257 | 216 | 5 |
| `Add_TmpfsNotCgroupfs` — `Add(pid)` | 9 231 | 224 | 5 |

**Read the row names.** These are writes to a private directory on `/dev/shm`.
They are **not** cgroup writes and 9.3 µs is **not** what `SetMemoryMax` costs
against cgroupfs — the memory controller's handler does real work that a tmpfs
write does not, and none of it is here.

What the subtraction does establish:

```
9 257 − 8 551 = 706 ns  →  the SDK's entire share of SetMemoryMax  (7.6 %)
9 231 − 8 551 = 680 ns  →  the SDK's entire share of Add           (7.4 %)
5 allocs − 3 allocs = 2  →  exactly filepath.Join and the decimal rendering
```

**~700 ns and two allocations is everything this package contributes to a
controller write.** Everything else belongs to `os.WriteFile` and the filesystem
under it. On a delegated host the denominator will be larger (cgroupfs is slower
than tmpfs — §1 measured the same refusal at 2.5×), so the SDK's share will be
*smaller* than 7.6 %, not larger.

**Verdict: nothing to optimise.** The two allocations are a path join and a number
rendered as text, both required to name a file and write a value.

### A methodology note that cost 22× to learn

The plain-filesystem rows first ran under `TMPDIR` and measured **201 898 ns** —
22× the figure above. `TMPDIR` here is tmpfs, so that was not a disk. A CPU
profile showed only 33 % of wall time on-CPU: the rest was **contention from the
other jobs sharing this build machine's `/tmp`**. Re-pointing the rows at
`/dev/shm` — also tmpfs, but uncontended — gave 9.3 µs, stable to 2.2 % across five
runs. The benchmark now prefers `/dev/shm` and logs which directory it used.

That is worth recording because the failure mode is silent: the number was
plausible, repeatable within a run, and completely wrong.

## 4. The live verbs, on a real cgroupfs (#1097)

`cgroup_live_linux_bench_test.go` (package `cgroup_test`) times `Create`, `Delete`,
`Add`, `SetMemoryMax`, `SetPidsMax` and `Kill` against a delegated cgroup v2 root:
the one the conformance suite finds (`delegatedRoot`: `KITSUNIUM_CGROUP_TEST_ROOT`,
the mount for root, or this process's own group when `Probe` says it is
delegated). With none, every row skips with its reason. A group's name is the
benchmark's own and each is removed before it returns; `Create` and `Delete`
make and remove their groups in batches of 512 with the timer stopped, `Add`
moves one sleeping child of the benchmark between two groups (each write a real
migration), `SetMemoryMax`/`SetPidsMax` alternate two values, and
`Kill_Populated` starts, moves in and reaps a child with the timer stopped so
only the `cgroup.kill` write is timed (it spawns per iteration, hence the fixed
`-benchtime 1000x`).

Measured 2026-11-08, inside `systemd-run --user --scope -p Delegate=yes -p
TasksMax=500 -p MemoryMax=4G -p RuntimeMaxSec=300`, `-benchmem -count 8`,
median (min–max). Two scopes, because what the root hands its children changes
the price of a group:

- **no controllers**: the scope as the manager makes it — the benchmark runs in
  it, so it cannot hand `memory` or `pids` down (cgroup v2's no-internal-process
  rule) and the setters skip;
- **memory and pids enabled**: a scope whose shell first moves into a leaf and
  writes `+memory +pids` to the scope's `cgroup.subtree_control`, exporting the
  scope as `KITSUNIUM_CGROUP_TEST_ROOT`.

| workload | no controllers: ns/op | B/op | allocs/op | memory + pids enabled: ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| `Create_Live` — `mkdir` of a group | 46 246 (44 503–50 878) | 1 020 | 15 | 106 451 (105 727–112 145) | 1 019 | 15 |
| `Delete_Live` — `rmdir` of an empty group | 24 675 (23 586–26 185) | 256 | 2 | 42 192 (40 561–43 538) | 256 | 2 |
| `Create_Delete_Live` — a group's whole life | 69 098 (68 314–70 577) | 1 288 | 18 | 146 451 (143 227–149 347) | 1 288 | 18 |
| `Add_Live` — a migration through `cgroup.procs` | 27 001 (20 862–29 066) | 384 | 5 | 26 753 (25 198–28 494) | 384 | 5 |
| `SetMemoryMax_Live` | skipped | | | 10 474 (10 322–10 573) | 372 | 5 |
| `SetPidsMax_Live` | skipped | | | 10 308 (10 183–10 935) | 361 | 4 |
| `Kill_Empty_Live` — `cgroup.kill` of an empty group | 10 614 (10 122–11 052) | 360 | 4 | 10 594 (10 115–11 436) | 360 | 4 |
| `Kill_Populated_Live` — one sleeping member | 24 206 (23 458–26 273) | 360 | 4 | 23 327 (21 900–27 464) | 360 | 4 |

The same box and session, re-measured for the comparison (`-count 8`, median):
`RawWriteFile_Baseline` 8 648 ns, `WriteController_TmpfsNotCgroupfs` 9 232 ns,
`Add_TmpfsNotCgroupfs` 10 372 ns, `Create_Unavailable` 10 538 ns.

What the rows say:

- **A controller write on cgroupfs is 10.3–10.5 µs, 1.2× the plain write**
  (8.6 µs): `memory.max` and `pids.max` cost the kernel about 1.1–1.2 µs more
  than the tmpfs write of `WriteController_TmpfsNotCgroupfs` (9.2 µs). The SDK's share is the 584 ns between `RawWriteFile_Baseline`
  and `WriteController_TmpfsNotCgroupfs`, **5.6 % of `SetMemoryMax`** — smaller
  than §3's 7.6 %, as §3 predicted a delegated host would show.
- **A group is made and removed in 69 µs, and 146 µs once memory and pids are
  enabled for its parent**: the kernel instantiates each enabled controller's
  state per group, 2.3× on `Create` and 1.7× on `Delete`. A caller that makes a
  group per job pays that, not the SDK's 15 allocations and 1 KiB.
- **`Add` is a migration: 27 µs**, 2.6× a write to a plain file (`Add_Tmpfs`,
  10.4 µs), the cost of moving a task between groups. It does not depend on the
  controllers.
- **`Kill` is 10.6 µs on an empty group and 24 µs with one member**: the
  `cgroup.kill` write itself, the target's death not waited for.
- `Create_Unavailable`, the refusal this host used to have, is 10.5 µs here — a
  `mkdir` refused by cgroupfs costs less than a quarter of a delegated one
  (46 µs); §1's 5.5 µs was another box and kernel, and is not comparable.
- Spread is under 14 % everywhere but `Add` without controllers (30 %, one low
  round) and the populated `Kill` (24 % with, 12 % without): the rows that
  involve a task and the scheduler. The medians are the claim, not the extremes.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible), `GOMAXPROCS=4` |
| RAM | 11 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Git base | `091dcdc3e` (branch `wip/sdk-lot-4-fin`), plus the benchmark file of the commit that carries this section |
| Generated (UTC) | 2026-11-08 |
| Load average | 2.3 / 3.2 / 4.6 at the end of the runs, other jobs sharing the box |

Reproduce, from a scope that is delegated to the user:
`systemd-run --user --scope -p Delegate=yes -- go test -run '^$' -bench 'Live$' -benchmem -count 8 ./internal/service/proc/cgroup/`,
the populated kill with `-bench Kill_Populated_Live -benchtime 1000x`; for the
second column, from a shell that first moves into a leaf of the scope and
enables the controllers (`CLAUDE.md`, the live S1 suite).

## Platform note

`//go:build linux`, because `validateName`, `withinRoot`, `maxOrValue` and
`controlGroup` live in `cgroup_linux.go`. The Windows (Job Objects) and FreeBSD
(rctl) backends are separate implementations with their own syscalls and are **not
characterised here** — their numbers would have nothing in common with these, and
inventing a shared table would be the dishonest kind of portability.

No production code was changed in this package.

## Reproducibility envelope

> Every figure is the **median of 5 full runs**, each a separate process. Spread
> was under 8 % on every row but `Available` (14.8 %, a single high outlier over
> five runs — cgroupfs contends with the rest of the machine). Load average was
> 1.25–1.71 across the runs on this 8-core box.
>
> **The claims this report asserts** are: the pure half is under 500 ns, the SDK's
> share of a controller write is ~700 ns and two allocations, and `Available()` is
> a wiring-time call. The absolute plain-filesystem figures are `/dev/shm` on this
> machine and will move elsewhere; the *difference* between the two rows is what
> transfers.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| cgroup v2 | mounted rw, all controllers, **no delegation to the caller** |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 1da8e4c |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 5 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/cgroup
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                              median ns/op   spread   B/op  allocs/op
Available-8                                   5 868    14.8%    511      8
ValidateName_Valid-8                          47.66     5.5%      0      0
ValidateName_Traversal-8                      279.4     2.8%    216      3
WithinRoot_Inside-8                           180.1     1.1%     24      1
MaxOrValue_Number-8                           43.67     4.0%     16      1
MaxOrValue_Unlimited-8                        2.831     0.7%      0      0
ApplyOptions_None-8                           23.84     3.3%     16      1
ApplyOptions_WithRoot-8                       27.14     1.3%     16      1
Create_Unavailable-8                          5 460     2.1%    640      9
WriteController_TmpfsNotCgroupfs-8            9 257     2.2%    216      5
Add_TmpfsNotCgroupfs-8                        9 231     2.3%    224      5
RawWriteFile_Baseline-8                       8 551     7.5%    152      3
```

## The S1 verbs (#727) — not benchmarked

`GroupTuner`'s setters are `writeController` with another file name: the
SDK's share is the ~700 ns floor above, the rest cgroupfs. `Create` of a
nested name adds one `openat2` per element (`os.Root`) and, with
`WithControllers`, one `cgroup.subtree_control` write per group on the way.
`Watch` costs a goroutine and an inotify descriptor per call and one read of
`cgroup.events` (and `memory.events`) per batch of records. None of these
has a benchmark: the live verbs of §4 are measured, these still are not, and
the suite that exercises them (`s1_linux_external_test.go`) is a conformance
test, not a measurement.
