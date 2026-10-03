<!-- updated: 2026-10-03T12:00:00Z -->
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
