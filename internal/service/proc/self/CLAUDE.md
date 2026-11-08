# internal/service/proc/self/

## Purpose

What the running process is DOING (ADR 0100): its runtime state (`stats.go`,
`cputime_*.go`). Every other `proc` package acts on another process — spawns,
signals, reaps, limits a child; this one only reads, and only the process it
runs in. Public facade: `pkg/v1/proc/process` (`Self`, and the `Stats` /
`Distribution` aliases).

What the binary was BUILT from was here too and is now
`internal/service/proc/buildinfo` (ADR 0172): reading a build must not import
`runtime/metrics`, whose initialisation every process importing it pays at
start — about 0.03 ms of a status line's every render, for a figure it never
reads.

Stdlib, plus `internal/core/proc` and `internal/kernel/errs` for the typed refusal of `SetOOMScoreAdj`, the one writer, and `internal/cgroupfs` for the container id (#938).

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `stats.go` | `StatsValue`, `DistributionValue` + `Count` / `Quantile`, `ReadStats`, the runtime/metrics names, `lastGC`, `runtimeCPU` |
| `cputime_unix.go` | `cpuTime` from `getrusage(RUSAGE_SELF)` — user plus system, every thread |
| `cputime_other.go` | `cpuTime` as the runtime's estimate, flagged — Plan 9, wasm (Windows reads `GetProcessTimes`) |
| `oomscore_linux.go` / `oomscore_other.go` | `SetOOMScoreAdj` (#940): the one writer here — the process's own `/proc/self/oom_score_adj`, `RlimitFailed` when the kernel refuses (out of range, lowering without `CAP_SYS_RESOURCE`), `UnsupportedPlatform` off Linux. Added by L4-exec; the read side (#938/#939) is L4-misc's |
| `kernelmem_linux.go` / `kernelmem_unix.go` (+ `kernelmem_unit_darwin.go` / `kernelmem_unit_kib.go`) / `kernelmem_windows.go` / `kernelmem_other.go` | the kernel's view in `StatsValue` (#939): `RSSBytes`, `PeakRSSBytes`, `Threads`, `OpenFiles`, `KernelMemory` — `/proc/self/statm`, `status` (VmHWM, Threads) and `fd` on Linux; `ru_maxrss` for the peak on the other Unixes (bytes on darwin, KiB elsewhere); `K32GetProcessMemoryInfo` and `GetProcessHandleCount` on Windows |
| `identity.go` / `identity_linux.go` / `identity_windows.go` / `identity_other.go` | `ReadIdentity`, `IdentityValue`, `StartSource` (#938): the kernel's start time (Linux `/proc/self/stat` field 22 in USER_HZ ticks after `/proc/stat` `btime`; Windows `GetProcessTimes`; the package init elsewhere, said by `StartedAtSource` — darwin/BSD `kinfo_proc` layouts are not bound), the binary's SHA-256 and Go build id (ELF `.note.go.buildid`, else the linker's raw mark in the first 32 KiB — `go tool buildid`'s two readings), the container id from the cgroup path through `internal/cgroupfs` (docker, cri-containerd, crio, libpod, kubepods), the pid namespace inode and NSpid 1, the boot id |
| `cputime_windows.go` | `cpuTime` from `GetProcessTimes` (#938): `CPUEstimated` is false on Windows now |

## Why-this-shape

- **The values live here, not in `core/proc`** (ADR 0074). No port of the proc
  domain speaks them and one mechanism produces them; hoisting them would put
  one reading of `runtime/debug` into the contract layer.
- **One writer, one error.** `SetOOMScoreAdj` lets a supervisor that must
  outlive its children lower its own OOM bias; a refused write is a fault the
  caller acts on, so it alone returns an error. Its test runs in a helper
  process: a raised bias cannot be lowered again without privilege.
- **No error anywhere else.** Not being built with module support, a metric the
  toolchain does not export, a platform without `getrusage` — none is a fault a
  caller can act on. Each is a zero field, a `false`, or `CPUEstimated`, and
  each field's comment says which.
- **Everything cumulative, on purpose.** The runtime's histograms count since
  the process started, so a p99 read here is over the process's life. A window
  is two snapshots subtracted; a snapshot that reset the runtime's counters
  would break every other reader.
- **`Quantile` reads a bucket's UPPER bound** — the honest reading of a bucketed
  histogram — and `q = 0` lands on the first bucket that holds an observation,
  not the first bucket; the downstream copy answered the empty first bucket.
- **`started` is this package's initialisation**, not the kernel's process
  start: the gap is the runtime's own bring-up. Reading `/proc/self/stat` would
  be exact on Linux and nothing elsewhere; a field that means different things
  per platform is worse than a documented approximation.
- **CPU time is the kernel's where it exists.** The runtime's estimate counts
  only threads running Go code, so it misses cgo and time blocked in the kernel;
  `CPUEstimated` says when that is what the caller got.

## Do NOT

- Add an error return. A caller has nothing to do with "this platform has no
  getrusage" except read the flag.
- Reset or window the runtime's counters here. Subtract two snapshots.
- Read the build here. It is `internal/service/proc/buildinfo`'s, apart from
  `runtime/metrics` on purpose (ADR 0172).

## Verification

```sh
bazel test --config=race //internal/service/proc/self:self_test
cd internal/service && GOWORK=off go test -race ./proc/self/
```
