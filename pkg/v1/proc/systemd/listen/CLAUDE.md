<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/proc/systemd/listen

Public facade for systemd-style **socket activation** (`sd_listen_fds(3)`) — the
companion to `pkg/v1/proc/systemd/notify` (ADR 0016). A service recovers already-bound
listening sockets handed to it by an activator; an activator hands sockets to a
child it spawns. Both halves ship here, so the protocol is testable without
systemd.

The package is `listen`. It was `sdlisten`, at the root of `pkg/v1`, until
ADR 0155 put the proc facades under `pkg/v1/proc` and the two systemd protocols
under `systemd/`: the import path's last element and the package name agree,
so a caller writes `listen.Listeners(true)`. The source file keeps the
protocol's name, `sdlisten.go`.

## Why this shape

- **Thin facade, delegation only.** `Files` / `Listeners` / `WithNames` /
  `Prepare` delegate straight to `internal/service/proc/systemd/listen`; the facade adds
  zero behaviour. `Spec` is a type alias of the core port (so the same value drives
  `process.Start`).
- **Symmetric, self-contained.** `Prepare` (activator) is the mirror of
  `Files`/`Listeners` (service): it puts each listener's socket, in sorted-name
  order, at the FRONT of the child `Spec.ExtraFiles` — the protocol fixes them at
  fd 3 onward, so any `ExtraFiles` already set shift after them — and sets
  `LISTEN_FDS` / `LISTEN_FDNAMES` in `Spec.Env`. This is
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

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/proc.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.
