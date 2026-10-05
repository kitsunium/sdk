<!-- updated: 2026-10-05T00:00:00Z -->
# internal/kernel/fs/flock/

## Purpose

The SDK's file lock. Stdlib-only, domain-neutral. `TryLock` takes an
exclusive lock on the whole of a file WITHOUT waiting — `flock(2)` on the Unix
kernels, `LockFileEx` over every byte on Windows — and `Unlock` gives it back
(ADR 0081, ADR 0159 §3). `Native` says whether this GOOS has either.

It exists because the same non-blocking `flock` was written twice — in
`internal/service/app/lock` (the file locker, ADR 0052, with the `LockFileEx`
half ADR 0081 added) and in `internal/service/security/session` (the file
store's store-wide lock, ADR 0073) — line for line, the `EWOULDBLOCK`/`EAGAIN`
pair and all. Both now call this package and keep everything that is theirs:
lock its in-process gate, its fencing ledger and the ADR 0082–0086 rules;
session its gate, its abandonable waits and its own platform gate.

## Contents

| File | Holds |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `flock.go` | `TryLock` and `Unlock` — the contract, stated once for every platform |
| `flock_unix.go` | `Native = true` and `flock(LOCK_EX\|LOCK_NB)` / `flock(LOCK_UN)` (linux, darwin, the four BSDs; android and ios through their tags) |
| `flock_windows.go` | `Native = true` and `LockFileEx` / `UnlockFileEx` over offset 0, 2^64-1 bytes, with `LOCKFILE_FAIL_IMMEDIATELY`, bound from `kernel32` with `syscall.NewLazyDLL` (no `x/sys`, ADR 0018) — and the measured table of how it differs from `flock(2)` |
| `flock_other.go` | `Native = false` and `errors.ErrUnsupported` from both calls (js, wasip1, plan9, aix, solaris, illumos) |
| `flock_external_test.go` | the platform matrix, two-description exclusion, the same-description row where the kernels are opposites, and a failed call that must not read as contention — on every GOOS, nothing skipped |

## The two kernels, measured (ADR 0081)

Taken on `windows-latest` and on linux/amd64 before the first backend over
either was written; `internal/service/app/lock`'s suite still carries the raw
`LockFileEx` measurements, with bindings of its own.

| Case | `flock(2)` | `LockFileEx` |
|---|---|---|
| Two separate opens, same process | excludes | excludes |
| **The same description locked again** | **succeeds — a conversion** | **refused — `ERROR_LOCK_VIOLATION`** |
| Another process while held | excludes | excludes |
| Enforced against unrelated I/O | no — advisory | yes — mandatory; the holder's own handle is exempt |
| Scope | the whole file | a byte range: this package takes all 2^64-1 bytes |
| Released by | the last close on the description, which a process's death is | the handle's close, which a process's death is — asynchronously, so a caller unlocks explicitly |

Row 2 is why the package promises nothing about goroutines sharing one
descriptor: on Unix they would all be inside at once, on Windows the second
would be told the lock is busy. Both consumers put a gate in front of
`TryLock` — lock's `nameGate`, session's one-slot channel — for exactly that
reason, and `TestTheSameDescriptionIsConvertedOnUnixAndRefusedOnWindows` pins
the row through this package's own calls.

## Conventions

- **Never a blocking call.** A blocking `LOCK_EX`, or `LockFileEx` without
  `LOCKFILE_FAIL_IMMEDIATELY`, parks the thread inside a syscall no
  cancellation reaches; a caller with a deadline polls `TryLock` on its own
  clock (lock: `FileConfig.Poll` on `clock.Timed`; session: the same).
- **Contention is an answer, not an error.** `(false, nil)` means held
  elsewhere — `EWOULDBLOCK`/`EAGAIN` on Unix (both read, because the BSDs need
  not agree), `ERROR_LOCK_VIOLATION` on Windows and nothing else.
  `ERROR_IO_PENDING` is deliberately a failure: it means a request still
  outstanding, which "held elsewhere" would contradict.
- **The kernel's own error, unwrapped.** Any other failure is the
  `syscall.Errno`; a caller wraps it once in its own code (lock:
  `LOCK_BACKEND_FAILED`; session: `LOCK_FAILED`). The package emits no code
  and owns no dotted-quad range — pathchain's convention for this family.
- **`Native` is read where a caller is wired.** Where it is false every call
  returns `errors.ErrUnsupported` (allowed by rule 2: it writes no message of
  its own), and both consumers refuse at construction under their own
  sentinel, `proc.UnsupportedPlatform`, so neither ever makes the call.
- **The range is the whole file**, on Windows. Locking less is a decision about
  the file's contents and so the caller's; the one consumer that keeps data in
  its lock file (lock's fencing ledger, at offset 0) needs every byte covered.

## Consumers, and what each keeps

| Consumer | Calls | Keeps for itself |
|---|---|---|
| `internal/service/app/lock` | `Native`, `TryLock`, `Unlock` — one description per acquisition | the in-process `nameGate` (taken first, released last), the fencing ledger inside the locked range, the `hardenedOpen` pairing (`platformNative = flock.Native && hardenedOpen`, pinned by `TestTheLockAndItsHardeningShareAPlatform`), `LOCK_BACKEND_FAILED` |
| `internal/service/security/session` | `TryLock`, `Unlock` — one description for the store's lifetime | its own `platformNative` (Unix only: the store also needs owner-only modes and a directory flush, which Windows lacks — `TestTheStoreIsBuiltOnlyWhereTheKernelCanLock` pins that it never exceeds `Native`), the one-slot gate, both abandonable waits (ADR 0073), `LOCK_FAILED` |

`internal/service/security/secret` locks through the lock domain's file
locker, not through this package.

## Do NOT

- **Add a blocking variant**, or drop `LOCKFILE_FAIL_IMMEDIATELY` — the same
  mistake spelled for the other kernel. Every caller's context would stop
  meaning anything.
- **Promise goroutine exclusion.** Row 2 above: the answer is opposite on the
  two kernels, and a caller that relies on either is wrong on the other.
- **Widen the Unix tag set to a GOOS because it has some lock.** solaris and
  illumos have `fcntl` record locks, which are per PROCESS rather than per
  description and released by ANY close of the file — a different contract.
  A new platform arrives with its own measured row, as Windows did (ADR 0081),
  and `internal/service/app/lock`'s `nofollow_*.go` must gain it in the same
  change (ADR 0082; the pairing test fails otherwise).
- **Read `ERROR_IO_PENDING` as contention**, or any errno but the documented
  ones.
- **Return a coded error.** The kernel answers with the kernel's errno; the
  code is the caller's.

## Verification

```sh
bazel test --config=race //internal/kernel/fs/flock:flock_test
# or: cd internal/kernel && GOWORK=off go test -race ./fs/flock/...
GOWORK=off GOOS=windows go -C internal/kernel vet ./fs/flock/
```

The suite carries no build constraint and asserts on every GOOS: the lock on
the six Unix kernels and Windows, `errors.ErrUnsupported` where `Native` is
false. `.github/workflows/e2e-cross.yml` lists `./fs/flock` in `KERNEL_PKGS`,
so it runs on real Linux, macOS and Windows kernels, inside FreeBSD, OpenBSD
and NetBSD VMs, and on illumos and Solaris — where what it proves is the
refusal.
