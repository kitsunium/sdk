# internal/kernel/sdkwarn/

## Purpose

The SDK's construction-time warning primitive. Stdlib-only, domain-neutral.
`Emit(format, args...)` formats a message and routes it to the installed
audit hook, or to `os.Stderr` when no hook is set. `SetHook(fn)` installs an
audit captor used by tests to assert that sinks/middlewares surfaced the
right composition warnings at `New` time — without piping `os.Stderr`.

`sdkwarn` is the channel a middleware uses when it wants to say
"this composition is dubious but not wrong" — e.g. `async` wrapping a
`LocalClass` sink, `CircuitBreaker` wrapping a sink that can't be down,
`Retry` wrapping a `Sample` whose drop is permanent.

## Contents

| Symbol | Surface | Use case |
|---|---|---|
| `HookFn` | `type HookFn = func(string)` | shared name for the hook signature |
| `Emit` | `func Emit(format string, args ...any)` | one-shot construction-time warning |
| `SetHook` | `func SetHook(fn HookFn) (previous HookFn)` | install / restore audit captor |

## Conventions

- **Construction time only.** `Emit` MUST NOT be called on the hot path
  (`Write` / `Flush` / `Close`). There is no rate limit, no buffering, no
  batching — a per-record caller will saturate `stderr` and starve the GC.
  The convention is "once per anomaly per process".
- **Hot-path budget is zero.** `Emit` is never invoked on the `Write` path.
  Sinks/middlewares evaluate `downstream.Class()` at `New` time and call
  `Emit` once; the resulting decoration sink writes Records without re-checking.
- **Hook deadlock contract.** The hook runs in the caller goroutine. A
  blocking hook deadlocks the caller. Hooks MUST return promptly. `sdkwarn`
  does NOT `recover` from a panicking hook — a panic propagates as documented
  Go semantics.
- **Stderr-path allocation budget is NOT gated.** When no hook is installed,
  `Emit` allocates 3+ times (Sprintf result + concat + stderr buffering). The
  hook-installed path allocates only the Sprintf result (1 alloc/op). Tests
  gate the hook-installed path at `<= 1.0 allocs/op` (`AllocsPerRun(1000)`);
  the stderr path is exercised but not measured.
- **No `init()`.** The zero value of `atomic.Pointer[HookFn]` is "no hook"
  — exactly the documented default. `KTN-FUNC-NOINIT` enforced globally.
- **Concurrent-safe.** `atomic.Pointer.Load` on `Emit`, `atomic.Pointer.Swap`
  on `SetHook`. No mutex. Race tests verified under `bazel test --config=race`.

## Do NOT

- Call `Emit` on the hot path. The warning channel has no rate limit.
- Install a blocking hook in production. Hooks are a test-time captor;
  production hooks (for log forwarding) should be non-blocking channel sends.
- Use `sdkwarn` for error reporting — errors go through `errs.Define` /
  `errs.Wrap`. `Emit` carries a string, not a typed `*errs.Error`.
- Replace the package-level slot in tests — call `SetHook` instead. The slot
  is `atomic.Pointer[HookFn]` and `SetHook` is the only sanctioned mutator.

## Verification

```sh
bazel test --config=race //internal/kernel/sdkwarn:sdkwarn_test
# allocation gate runs without race instrumentation:
bazel test //internal/kernel/sdkwarn:sdkwarn_test
```
