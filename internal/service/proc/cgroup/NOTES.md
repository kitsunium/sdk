# Notes — <!-- updated: 2026-11-08T12:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Performance — see `BENCH.md`

**What a group used: `GroupReader` (#932).** Every backend's group also
satisfies the core sibling port `GroupReader` (ADR 0039), asserted on the
`Group` `Create` returns: `Stat()` and `Members()`. Linux reads `cpu.stat`
(required: a group without it is gone → `CgroupReadFailed`), then
`memory.current`, `memory.peak` (5.19+), `memory.events`, `pids.current` and
`io.stat`, each optional — a missing one (a controller not enabled, an older
kernel) is zero with `Partial` — and `cgroup.procs` for `Members`
(`groupstat_linux.go`, one flat-keyed parser). FreeBSD sums
`rctl_get_racct("process:<pid>")` over the tracked members — `cputime` (whole
seconds) and `memoryuse` — with the member count as pids, always `Partial`
(`groupstat_freebsd.go`). Windows queries the job: basic accounting (user and
kernel time, active processes), the extended limits' `PeakJobMemoryUsed`, and
the process-id list grown on `ERROR_MORE_DATA`, always `Partial`
(`groupstat_windows.go`). The live OOM-kill row of the issue needs the
linux-init lane's delegated cgroup; `TestStatOnADelegatedGroup` covers the CPU
and pid rows there and skips elsewhere. The FreeBSD and Windows readers never
ran here.

**The live S1 suite (#727)** is `s1_linux_external_test.go`: it works in a
group it makes under, in this order, `KITSUNIUM_CGROUP_TEST_ROOT` (a lane's
delegated root whose children may take controllers), the mount when
`Available()`, or its own group when `Probe` says it is delegated — and skips
otherwise. Under `systemd-run --user --scope -p Delegate=yes` it runs as is
(controllers refused typed: the scope holds the test, so it cannot hand a
controller down); with a scope that first moves itself into a leaf, enables
its controllers and exports its directory as `KITSUNIUM_CGROUP_TEST_ROOT`,
every memory and cpu setter is written and read back on the kernel.
`TestLiveSpawnIntoTheTreesPath` joins it to `exec`'s placement (ADR 0217,
#929): the directory `GroupTree.Path` names is the `Spec.CgroupPath` a child
is spawned into, and the group then holds it — `Contains` — and, as the
`CgroupKill` `proc/service` asks for (S0g, #734), `Kill` ends it.

**What S0g asks of the Linux group (#734)** is pinned on the fake filesystem
by `s0g_linux_internal_test.go`: `Members` over the subtree, `DeleteBeneath`
keeping the group and following no link, the watch publishing a change a
burst undid, `Membership`'s table and — on any Linux host — its inverse of
`cgroupfs.OwnGroup`. The live half is `proc/service`'s
`cgroupmodes_live_linux_external_test.go`.

**`Available()` answers for the mount root; `Probe` for any root (#1097).** On
every host that delegates a SUBTREE — a unit with `Delegate=yes`, a rootless
container, a user session — `Available()` is false while
`Create(name, WithRoot(own))` works. `Probe(WithRoot(p.OwnGroup)).Delegated` is
the honest gate there. The conformance suite's live-verb gate is that root
(`liveHost`, shared with the S1 suite's `liveRoot` through `delegatedRoot` in
`host_linux_external_test.go`), no longer `Available()`: on a delegated-subtree
host the live cycle runs under `WithRoot(own)` instead of asserting the
refusal. Controllers cannot be handed down by a scope that holds the test
(cgroup v2's no-internal-process rule), so a memory or pids ceiling there is
asserted as the typed refusal; a scope that first moves itself into a leaf and
enables them (`KITSUNIUM_CGROUP_TEST_ROOT`) writes them for real.

**`Available()` is a wiring-time call, not a per-spawn predicate.** It is 5.9 µs
and eight allocations, because the honest delegation probe really attempts a
`MkdirTemp` under the mount — and a refused `mkdir` on cgroupfs costs ~2.5× the
same refusal on tmpfs (the kernel takes the cgroup mutex before saying no). Call
it once and remember the answer.

The pure half — `validateName` (48 ns, zero allocations), `withinRoot` (180 ns),
`maxOrValue` (2.8 ns for `"max"`, 44 ns for a number), `applyOptions` (24 ns) —
sums to under 500 ns and is not worth attention.

The SDK's own share of a controller write is **~700 ns and two allocations**
(`filepath.Join` + the decimal rendering); everything else is `os.WriteFile` and
the filesystem under it.

**The live verbs are characterised in a delegated scope (#1097).**
`cgroup_live_linux_bench_test.go` times `Create`, `Delete`, `Add`,
`SetMemoryMax`, `SetPidsMax` and `Kill` on a real cgroupfs, under the root
`delegatedRoot` finds — and skips without one — and `BENCH.md` §4 has the rows:
a group costs 69 µs to make and remove (146 µs once memory and pids are enabled
for its parent), a controller write 10.4 µs (the SDK's share 584 ns), an `Add`
27 µs, a `Kill` 11 µs on an empty group. The first container the package was
measured in delegated nothing (`mkdir` was EACCES at the mount root and inside
its login-session scope), so `Available()` was false there and §§1–3 have no live
path; a scope from `systemd-run --user --scope` is delegated to the user, and
`Available()`, which asks about the mount root, still answers false in it.
