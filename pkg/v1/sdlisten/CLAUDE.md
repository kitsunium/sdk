# pkg/v1/sdlisten

Public facade for systemd-style **socket activation** (`sd_listen_fds(3)`) — the
companion to `pkg/v1/sdnotify` (ADR 0016). A service recovers already-bound
listening sockets handed to it by an activator; an activator hands sockets to a
child it spawns. Both halves ship here, so the protocol is testable without
systemd.

## Why this shape

- **Thin facade, delegation only.** `Files` / `Listeners` / `WithNames` /
  `Prepare` delegate straight to `internal/service/proc/sdlisten`; the facade adds
  zero behaviour. `Spec` is a type alias of the core port (so the same value drives
  `process.Start`).
- **Symmetric, self-contained.** `Prepare` (activator) is the mirror of
  `Files`/`Listeners` (service): it appends each listener's socket to the child
  `Spec.ExtraFiles` and sets `LISTEN_FDS` / `LISTEN_FDNAMES` in `Spec.Env`. This is
  what makes the family testable end-to-end without a real init system.
- **LISTEN_PID convention.** A pre-fork activator cannot know the child's pid, so
  `Prepare` omits `LISTEN_PID`. The receiving side accepts an absent `LISTEN_PID`
  from a trusted parent while still validating a present one (the systemd case): a
  set-but-mismatched `LISTEN_PID` yields no fds.

## Do / Do-not

- **Do** pass `unsetEnv: true` to `Listeners`/`Files` so a grandchild does not
  re-inherit the activation environment.
- **Do** use `WithNames` when several sockets share a unit and you key on
  `FileDescriptorName=` (duplicate names group several fds).
- **Do not** expect activation off Unix — fd inheritance is a Unix mechanism;
  every function returns `UnsupportedPlatform` elsewhere.
- **Do not** add new exported types — the surface is the four functions + the
  `Spec` alias.

## Errors

Every error is a central `internal/core/proc` sentinel: `LISTEN_FAILED` (a bad
fd / unsupported listener kind / malformed `LISTEN_FDS`) or `UnsupportedPlatform`
(non-Unix). An empty or foreign activation set is **not** an error — it returns
nil.

## Platform

Unix only. The package compiles on every GOOS; off Unix the functions return
`UnsupportedPlatform`.
