<!-- updated: 2026-11-02T18:00:00Z -->
# internal/service/net/loop/

The reference half of this file moved, unchanged and under the same section titles, to
`internal/service/net/loop/NOTES.md` (ADR 0253): read the section a change touches.
It holds:
§Contents; §Why-this-shape; §Conformance (one suite, every backend present).

## Purpose

The network domain's event-loop engine (ADR 0186): the native backends a
plaintext stream group runs on when a `corenet.LoopHandler` serves it:
io_uring on `linux && (amd64 || arm64 || riscv64 || loong64)` after a staged
probe of the running kernel, and epoll on every Linux architecture and after
any probe failure; kqueue on darwin, FreeBSD, OpenBSD, NetBSD and DragonFly
after its own staged probe (ADR 0222) — `server` hands it kqueue only when the
code names it, its `auto` staying portable there until the paired gate holds
(ADR 0222, amended 2026-11-11). The
portable backend is not here: it needs the netpoller and `net.Conn`, so it lives
in `server` (`loop_portable.go`). `server.Group.HandleLoop` is the one way in
(§D9 step 11); nothing of this package is published itself. Beside the
stream groups, `StartPacket` runs one io_uring lane of a datagram group a
`corenet.PacketLoopHandler` serves (#852, `server.PacketGroup.HandleLoop`).

## Benchmarks and BENCH.md (#981, ADR 0165)

`BENCH.md` beside this file is the package's report: the envelope (CPU, kernel,
Go, commit, `-benchtime`, `-count`), one row per Benchmark function — the `Core_*`
rows on a fake driver, `Post`, `Wheel`, `Slab` and `Write` (allocs 0 for `Write` and
`Post`), the eight `URing*` rows on io_uring — and the class B rows of `caps.go`,
each with the benchmark its ADR will read or "no row yet" (ADR 0189 §D1). It is
hand-run, as `net/http1`'s is, and `make bench` does not write it:

```
cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=6 -benchtime=300ms ./service/net/loop/
```

`scripts/pre-commit/check-bench-md.sh` finds a benchmarked package by its
`*_bench_test.go` file names, so every Benchmark function lives in a file of that
name (`loop_bench_test.go`, `batch_cost_bench_test.go`, `uring_*_bench_test.go`,
the io_uring ones with their explicit `//go:build linux && (amd64 || ...)` line,
which a `_linux_bench_test.go` name would lose): a benchmark put in an
`_internal_test.go` or `_linux_test.go` file hides from the check again.
`TestBackendsRequired` and `TestCapsRequired` still need their `-ldflags` (above).

## Cost

- **The slab follows its memory limit and gives a burst back (#809, ADR 0186
  §D7 amended).** A loop's budget was 128 MiB whatever the cgroup, and an arena
  stayed mapped until the loop stopped. Now server passes `memlimit.Derive()` (the
  allowance, nothing installed) as `Config.Memory`; 512 MiB and four loops is
  32 MiB a loop. The alloc and free path keeps no count: `shrink` counts each
  arena's chunks on the free stack once a tick, and only in a class holding two
  arenas' worth of free chunks, so `BenchmarkSlab`'s alloc+free is the base's
  code (benchstat geomean +2.7 %, n=10, the noise of a shared VM). A 64 MiB burst
  freed is back to one arena a class within 32 ticks
  (`TestSlab_shrinkAfterBurst`, the mapper double, the ticks driven by hand: the
  loop reads no clock port). An epoll or kqueue loop with no connection slept
  until the next event, so its shrink never ran once a burst's clients had all
  left — the usual end of a burst: such a loop now waits on the tick while
  `slab.reclaimable` (a class with two wholly free arenas) holds, and sleeps
  again once it does not, so it wakes for nothing; io_uring waits on the tick
  anyway (`TestSlab_idleLoopGivesBack`, every native backend: 8 MiB of spills,
  every client gone, back to one arena a class within a second with no event,
  then no batch at all on epoll and kqueue). Under `GOMEMLIMIT` set by the
  operator memlimit reads no allowance, and the budget is the default.
- The LoopIO budget is allocs 0 for `Write`, `SetDeadline`, `Pressure` and a
  refused `Write` with a caller-owned `p` (`TestCore_allocs`).
- An idle connection costs at most **256 B of Go heap** and no mapped byte
  (`TestMemory_idleConnection`, 10 000 connections from a re-executed child,
  dialed `SOMAXCONN` at a time, each wave accepted before the next — FreeBSD
  clamps the 4096 backlog to 128 and resets a handshake past its queue
  (#1407) —, race off, its own Bazel target in `tools/alloc-lane-targets.txt`). Measured
  under Docker on Linux 7.0 arm64: 136.4 B per connection with epoll, the test
  handler's own map entry included, and 137.9 B with io_uring (145.8 B each
  before the table grew by fixed steps, ADR 0201 §D2). The kernel's socket memory is not counted
  here; the 50k/150k/250k curve on a real host is measured outside CI.

## Verification

```
# Linux (CI, and Docker on a Mac with --security-opt seccomp=unconfined, which
# io_uring needs; under Docker's default profile auto falls back to epoll):
cd internal && GOWORK=off go test -race -tags loopdebug \
  -ldflags='-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=iouring,epoll -X=github.com/kitsunium/sdk/internal/service/net/loop.requiredCaps=none' \
  ./service/net/loop/
# without either -X the suite fails closed on Linux, by design; `none` requires nothing.
bazel test //internal/service/net/loop:all
# darwin and the BSDs (the macos-15 lane, the BSD guests): kqueue is required
# by default; -X=…requiredBackends=none lifts it.
cd internal && GOWORK=off go test ./service/net/loop/
```

## Do NOT

- Import `server`, or any member of the family.
- Put a pointer in `connCtl`, or a payload byte in the Go heap on a native
  backend.
- Compact, reallocate or recycle a buffer handed to `OnData` before
  `OnBatchEnd` returns.
- Walk every open slot at each batch: list what the batch touched instead.
- Call `OnClose` from inside another callback.
- Call `setrlimit` or `GOMAXPROCS`: `State` reports both, and the consumer
  decides. `LoopsAuto` leaves a P over; it never adds one.
