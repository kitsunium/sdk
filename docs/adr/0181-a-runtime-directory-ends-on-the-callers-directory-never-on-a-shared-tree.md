# ADR 0181 — a runtime directory ends on the caller's directory, never on a shared tree

- **Status**: Accepted
- **Date**: 2026-10-20
- **Accepted**: 2026-10-20
- **Deciders**: SDK maintainers, the owner (kodflow/ktn-linter#785)
- **Extends**: [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (where a private socket lives), by a sibling function per [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
- **Related**: kodflow/ktn-linter#785, #757 (its per-user daemon)

## Context

`ipc.RuntimeDir(app)` names where an application's private sockets live:
`$RUNTIME_DIRECTORY` (systemd), then `$XDG_RUNTIME_DIR/<app>`, then
`%LOCALAPPDATA%\<app>` on Windows, else `<tmp>/<app>-<uid>`.

The last step is a directory in a tree every account can write to. ADR 0148's
gate holds there: `Listen` creates the directory 0700 and refuses one another
account created first (`CodeDirectoryUnsafe`). The cost is that the gate is
all that stands between the daemon and a denial of service. An account that
creates `<tmp>/<app>-<uid>` first does not get in, but the daemon no longer
starts. ktn-linter's per-user daemon (#757) wrote the same rule locally and
kept a guard (`guardRuntimeDir`) for exactly this case. Its plan (P1) asks for
the fallback to be the application's own state directory, never `/tmp`.

A host without `$XDG_RUNTIME_DIR` is not rare: `su`, `sudo -u`, cron, a
container without a session manager, a CI runner.

## Decision

1. `ipc.RuntimeDirOr(app, fallback string) string` runs `RuntimeDir`'s chain
   and ends on `fallback` instead of `<tmp>/<app>-<uid>`, returned as given.
   The caller names a directory under one it already keeps private, typically
   `<state>/<app>/run`.
2. `RuntimeDir(app)` is `RuntimeDirOr(app, <tmp>/<app>-<uid>)`. Its behaviour
   does not change, and no caller has to move.
3. Nothing new is created and nothing new is checked. `Listen` already
   creates the directory 0700 and refuses a relative, foreign or open one.
   A fallback is held to the same gate as every other step.
4. A sibling, not a parameter: `RuntimeDir`'s signature is published
   (ADR 0039).

## Alternatives

- **Drop the `/tmp` step from `RuntimeDir`.** Rejected: it changes where
  every existing caller's sockets live on a host without
  `$XDG_RUNTIME_DIR`. A daemon and its clients upgraded at different times
  would look in two places.
- **An error when no runtime directory exists.** Rejected: the caller has a
  private directory to offer, and refusing to start is worse than using it.
- **`/run/user/$UID` as one more step.** Left to the caller. It belongs to
  logind and exists only for a session logind opened. Where it exists without
  `$XDG_RUNTIME_DIR`, a caller may name it as its fallback.

## Consequences

- ktn-linter's per-user daemon places its socket with `RuntimeDirOr` and
  removes its local `/tmp` fallback and the guard that came with it. A
  second consumer exists, the criterion #785 set for a mechanism to come back
  to the SDK.
- `<tmp>/<app>-<uid>` stays reachable through `RuntimeDir` for the callers
  that never name a directory.
