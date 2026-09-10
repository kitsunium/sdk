# internal/service/proc/sdlisten

Socket activation (`sd_listen_fds(3)`) for the OS process-supervision domain
(ADR 0016) — the service half that recovers inherited listening sockets, plus the
activator half (`Prepare`) that hands sockets to a spawned child. Companion to
`sdnotify`. **Stdlib-only** (`net`, `os`, `strconv`, `strings`, `syscall`, plus
`slices`/`maps`) + `internal/kernel/errs`.

## File map

| File | Build tag | Role |
|---|---|---|
| `sdlisten_unix.go` | `unix` | `Files` / `Listeners` / `WithNames` (service side, fd 3..) + `Prepare` (activator side); `LISTEN_FDS`/`LISTEN_PID`/`LISTEN_FDNAMES` parsing |
| `sdlisten_other.go` | `!unix` | stub: every entry point returns `UnsupportedPlatform` |

No `codes.go` / `errors.go` — every error is a `core/proc` sentinel
(`ListenFailed`, `UnsupportedPlatform`); this package mints none.

## Behaviour

- **Service side.** `Files(unsetEnv)` returns fds `3..3+LISTEN_FDS` as named
  `*os.File` (CLOEXEC cleared); `Listeners` wraps the stream sockets as
  `net.Listener` (closing each `*os.File` after `net.FileListener` dups it);
  `WithNames` groups by `LISTEN_FDNAMES` (duplicate names allowed). All honour
  `LISTEN_PID`: an absent value is accepted (trusted parent), a present value must
  equal `getpid()`, else no fds are returned (not an error). `unsetEnv` clears the
  three variables so a grandchild does not re-inherit them.
- **Activator side.** `Prepare(child, named)` **prepends** each listener's dup'd
  socket to `child.ExtraFiles` so the activation sockets occupy fd `3..3+N` (the
  protocol fixes them there); any `ExtraFiles` the caller already set shift to
  after them. It sets `LISTEN_FDS` + `LISTEN_FDNAMES` in `child.Env`, in
  sorted-name order so the fd↔name pairing is deterministic, and omits
  `LISTEN_PID` (unknowable pre-fork). `Files` clears `FD_CLOEXEC` on each
  recovered fd (via `fcntl(F_SETFD, 0)`) so the socket survives a re-exec.

## Platform

Fd inheritance is a Unix mechanism, so this is `//go:build unix` (Linux, darwin,
the BSDs). Off Unix the stub returns `UnsupportedPlatform`; every GOOS compiles.

## Performance — see `BENCH.md`

A binary that links this package and is **not** socket-activated pays **52 ns and
zero allocations**, once: every entry point starts with `fdCount`, which stops at
an unset `LISTEN_FDS`. There is no reason to guard the call.

`Prepare` is linear and syscall-bound: **4.5 µs for one socket, 14.0 µs for four**,
of which **63 % is the `dup(2)`** the protocol requires (`net.TCPListener.File()`,
2 855 ns) and 309 ns per listener is this package's bookkeeping. `Prepare` with no
listeners is 3.9 ns. Against the ~626 µs `exec.Start` that always follows it, a
four-socket `Prepare` is 2.2 % of the launch it prepares.

The `LISTEN_PID`-absent shortcut saves the 139 ns of `Atoi` + `getpid(2)`, but it
exists for correctness (a pre-fork activator cannot know the child pid), not
speed.

## Do NOT

- Call `errs.Define` — all codes live in `core/proc`. Restate the sentinel fields
  in `errs.Wrap`.
- Set `LISTEN_PID` in `Prepare` — a pre-fork activator cannot know the child pid.

## Verification

```sh
bazel test --config=race //internal/service/proc/sdlisten:sdlisten_test
# Fallback
go test -race ./internal/service/proc/sdlisten/...
```
