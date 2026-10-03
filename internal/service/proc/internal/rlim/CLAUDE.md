<!-- updated: 2026-10-03T12:00:00Z -->
# internal/service/proc/internal/rlim/

## Purpose

The **one constructor of the kernel's resource-limit struct** the proc family
uses: `Make(soft, hard uint64) syscall.Rlimit`, the place a
`coreproc.LimitValue` becomes what `setrlimit(2)` and `prlimit64(2)` read.
`proc/exec` calls it in the trampoline, applying a `Spec`'s limits in the child
before exec; `proc/rlimit` calls it applying limits to a running process —
`setrlimit` on every Unix, and `prlimit64` for a foreign pid on Linux. Both
carried a copy of it, two build-tagged files each, until it moved here; Linux's
`rlimit` built the struct inline.

The struct is not one type across the Unix kernels. FreeBSD and DragonFly
declare `Rlimit.Cur` and `Max` as `int64` (their `rlim_t` is `__int64_t`); every
other Unix declares them `uint64`, the width of `coreproc.LimitValue`. `Make` is
split by build tag so no caller ever names the field type, and `LimitInfinity`
(`^uint64(0)`) lands as `RLIM_INFINITY` on both: verbatim where the fields are
`uint64`, wrapped to `int64(-1)` where they are `int64`.

Code range: **none** — it builds a value and returns no error.

Internal to `internal/service/proc` (Go's `internal/` rule): its two importers
both sit in the proc family, so the rule admits exactly the family's engines.

## Contents

| File | Build tag | Surface |
|---|---|---|
| `doc.go` | all | the package doc — off Unix there is no rlimit struct and the package holds nothing else |
| `value_default.go` | `unix && !freebsd && !dragonfly` | `Make`: the pair assigned verbatim |
| `value_signed.go` | `freebsd \|\| dragonfly` | `Make`: the pair converted to `int64`, `^uint64(0)` → `-1` |
| `value_default_external_test.go` | as `value_default.go` | the pair unchanged, infinity included |
| `value_signed_external_test.go` | as `value_signed.go` | the conversion, and infinity as `RLIM_INFINITY` |

## Verification

```sh
bazel test --config=race //internal/service/proc/internal/rlim:rlim_test
cd internal/service && GOWORK=off go test -race ./proc/internal/rlim/
```

The signed variant runs only on FreeBSD and DragonFly, which is why the package
is listed in `e2e-cross.yml`'s `SERVICE_PKGS`: that lane runs it on both
kernels, as it ran the copies inside `proc/exec` and `proc/rlimit` before.

## Do NOT

- **Name the field type at a call site.** `syscall.Rlimit{Cur: x}` compiles on
  one kernel family and not the other; `Make` is the one spelling.
- **Clamp `LimitInfinity`.** A clamp turns "no limit" into the largest finite
  one, a ceiling the caller never asked for.
