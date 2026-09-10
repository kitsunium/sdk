<!-- generated from internal/service/proc/sdlisten/sdlisten_unix_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/sdlisten/` to refresh -->
# Benchmarks — `internal/service/proc/sdlisten`

**"Socket activation happens once at startup. Is any of it accidentally on a hot
path, or accidentally quadratic?"**

**Neither.** The activator side is **4.5 µs for one socket and 14.0 µs for four**
— linear, and **63 % of it is the `dup(2)` the protocol requires**. The service
side is **52 ns** for a process that was not activated, which is what almost every
binary linking this package will be.

## 1. A non-activated binary pays 52 ns, once

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `FdCount_Absent` — `LISTEN_FDS` unset | **52.09** | 0 | 0 |
| `FdCount_Present` — parse the count, then verify `LISTEN_PID` | 245.4 | 0 | 0 |
| `PidMatches_Absent` — trusted parent, no `getpid` | 49.03 | 0 | 0 |
| `PidMatches_Present` — `Atoi` + `getpid(2)` | 188.2 | 0 | 0 |

Every entry point (`Files`, `Listeners`, `WithNames`) starts with `fdCount`, so a
binary that links this package and is never socket-activated pays **one
environment lookup, 52 ns, zero allocations** and stops. There is no reason to
guard the call.

The rows decompose cleanly, which is why they are believed:

```
FdCount_Present − FdCount_Absent  = 193 ns   (Atoi + the LISTEN_PID check)
PidMatches_Present − _Absent      = 139 ns   (Atoi + getpid(2))
reaper/BENCH.md: getpid(2)        = 119 ns
                            → Atoi ≈ 20 ns, and the two rows agree
```

**The `LISTEN_PID`-absent shortcut is worth 139 ns**, and it exists for
correctness, not speed: a pre-fork activator cannot know the child's pid, so this
SDK's own `Prepare` omits the variable and `Files` accepts its absence from a
trusted parent. Real systemd always sets it and the check runs.

## 2. Name splitting is linear in the descriptor count, and allocates twice

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `SplitNames_Unset` — no `LISTEN_FDNAMES`, 4 fds | 120.7 | 64 | 1 |
| `SplitNames_Four` — 4 names, 4 fds | 218.1 | 128 | 2 |
| `SplitNames_Padded` — 2 names, 8 fds | 246.2 | 160 | 2 |

One allocation for the output slice, plus one for `strings.Split` when names are
supplied. `_Padded` costs more than `_Four` despite splitting *fewer* names,
because it emits eight entries rather than four — the cost tracks the descriptor
count, which is the right thing for it to track. Nothing here is quadratic and
nothing here runs more than once per process.

## 3. `Prepare` is linear, and 63 % of it is a syscall the protocol mandates

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Prepare_Empty` — no listeners, spec untouched | **3.915** | 0 | 0 |
| `ListenerFile_Dup` — one `File()` + `Close()`, no SDK code | 2 855 | 160 | 6 |
| `Prepare_One` | 4 487 | 1 064 | 15 |
| `Prepare_Four` | 13 980 | 1 688 | 36 |
| `EnvWithout_64` — the environment rewrite, 64 entries | 1 546 | 1 152 | 1 |

The per-listener slope confirms the mechanism:

```
(13 980 − 4 487) / 3 = 3 164 ns per additional listener
ListenerFile_Dup     = 2 855 ns
                     → 309 ns of SDK bookkeeping per listener, 90 % is the dup
```

And the fixed part:

```
4 487 − 2 855 (the dup) − ~390 (envWithout over 16 entries) ≈ 1 240 ns
```

— the sorted-name pass (`slices.Sorted(maps.Keys(...))`, which allocates), the
`ExtraFiles` prepend, the `strconv.Itoa`, and the `strings.Join`. Against 4.5 µs
paid once per spawn, none of it is worth touching; and `Prepare` is always
followed by an `exec.Start`, which is **626 µs** (`exec/BENCH.md` §1). **`Prepare`
with four sockets is 2.2 % of the launch it prepares.**

`Prepare_Empty` at **3.9 ns** matters more than it looks: an activator that always
calls `Prepare` and sometimes has nothing to pass pays nothing for the call.

`envWithout` is `O(len(env) × len(keys))` with `slices.Contains` over three keys.
At 64 entries that is 1.5 µs and one allocation — linear in the environment,
constant in the keys, and run once. The obvious rewrite (a switch on the key) is
not worth 1.5 µs of a 626 µs spawn.

## 4. Nothing to optimise, and the one thing that could be is not ours

The only line that could show up in a profile of this package is
`net.TCPListener.File()` at **2 855 ns and six allocations** — a `dup(2)`, a
blocking-mode change, and an `os.NewFile`. It is stdlib, it is what
`sd_listen_fds(3)` requires (the child must inherit a descriptor, and the parent
must keep its listener), and the SDK adds 309 ns on top of it.

No production code was changed in this package.

## What was NOT measured, and why

**`Files` / `Listeners` / `WithNames` against a real activation set.** Recovering
descriptors requires fds 3.. to *be* inherited listening sockets, and `Files`
returns `*os.File` values that alias those descriptors without duplicating them —
so a benchmark loop would either leak wrappers with finalizers that close fd 3
underneath the next iteration, or close the socket it is measuring. The honest
alternatives are a subprocess harness (which would measure the spawn, at 626 µs
per iteration) or a fake, and a fake here would be measuring `os.NewFile`.

What those functions actually do is `fdCount` + `splitNames` + one `fcntl` and one
`os.NewFile` **per descriptor** — every component of which is measured above or in
the stdlib. The package's own contribution is bounded by §1 and §2, and it is
nanoseconds. The end-to-end path is covered functionally by
`sdlisten_unix_external_test.go`, which does spawn a real child.

## Platform note

`//go:build unix`, matching the package: descriptor inheritance is a Unix
mechanism and the `!unix` sibling returns `UnsupportedPlatform` from every entry
point (ADR 0018). There is nothing to measure off Unix, and measuring the stub
would report the cost of a sentinel return dressed as a protocol.

The listeners are loopback **TCP** rather than Unix-domain, so no private
directory is needed and `listenerFile` takes the `*net.TCPListener` branch a real
activator takes.

## Reproducibility envelope

> Every figure is the **median of 5 full runs**, each a separate process (5 rather
> than 3 because `EnvWithout_64` and `FdCount_Present` were noisy at 3). Spread
> was under 10 % on every row but those two (24 % and 14 %), where the median is
> stable and the maximum is a single outlier; min/max are in the results block.
> Load average was 1.06–2.42 across the runs on this 8-core box.
>
> **The ratios are what this report asserts**: 63 % of `Prepare` is the dup, the
> per-listener slope is linear, and a non-activated binary pays 52 ns.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 532a984 |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 5 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/sdlisten
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                median ns/op   spread            B/op  allocs/op
FdCount_Absent-8                52.09     5.6%               0      0
FdCount_Present-8               245.4    14.4%  (244.2–279.5)   0      0
PidMatches_Absent-8             49.03     2.2%               0      0
PidMatches_Present-8            188.2     2.8%               0      0
SplitNames_Unset-8              120.7     4.9%              64      1
SplitNames_Four-8               218.1     6.6%             128      2
SplitNames_Padded-8             246.2     7.0%             160      2
EnvWithout_64-8                 1 546    24.4%  (1482–1859)   1152      1
ListenerFile_Dup-8              2 855     5.3%             160      6
Prepare_One-8                   4 487     4.1%            1064     15
Prepare_Four-8                 13 980     9.2%            1688     36
Prepare_Empty-8                 3.915     2.8%               0      0
```
