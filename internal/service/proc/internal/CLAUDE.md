<!-- updated: 2026-11-08T00:00:00Z -->
# internal/service/proc/internal/

## Purpose

The proc family's private helpers. This directory holds no Go code: it is a
prefix, not a package. Go's `internal/` rule lets only a package under
`internal/service/proc` import what sits below it, so a helper placed here is
shared by the family's engines and reachable by nothing else — not by another
service domain, and not by `pkg/v1`.

## Members

| Package | What it is | Importers |
|---|---|---|
| `bsdabi/` | the records the BSD kernels and darwin hand the family, decoded from their bytes: a process's `kinfo_proc` (`kinfo_proc2` on NetBSD) for its start time and users, every layout the header's under the ABI and proven by size and pid before a field is read (#729); FreeBSD's `sockcred2`, a datagram's sender (#730) — no system call, no build constraint, so every layout is tested on Linux | `proc/exec` (adoption off Linux), `proc/systemd/notify` (the FreeBSD listener) |
| `cgroupfs/` | the one reading of where this process sits in the control-group filesystem: every mount of the unified and v1-memory hierarchies with what each exposes, the membership of `/proc/self/cgroup`, the translation between them, and the own unified cgroup's directory (`OwnGroup`) — moved from `memlimit` (#1097, #938) | `proc/memlimit` (the ancestor walk), `proc/cgroup` (`Probe`), `proc/self` (the container id) |
| `rlim/` | the one constructor of the kernel's resource-limit struct: `Make(soft, hard)` → `syscall.Rlimit`, split by build tag because FreeBSD and DragonFly declare its fields `int64` where every other Unix declares `uint64`, so `LimitInfinity` lands as `RLIM_INFINITY` on both | `proc/exec` (the trampoline's pre-exec `setrlimit`), `proc/rlimit` (`setrlimit` and `prlimit64` on a running process) |

It sits beside its importers, at the deepest directory that holds them both,
so the `internal/` rule says who may use it — as the observe family's own
`internal/` does for the OTLP emitter (ADR 0155 §1). It is a leaf rather than
an export of `rlimit` that `exec` imports: one constructor is not worth an
edge from one engine to another, the reason ADR 0159 gives for moving the
backoff curve out of `resilience`.

## Do NOT

- Put Go code in this directory.
- Add a helper only one member uses: it belongs in that member.
- Add a helper a member of another family needs: it is not this family's, and
  the `internal/` rule would refuse the import anyway.
