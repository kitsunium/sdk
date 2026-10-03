<!-- updated: 2026-10-03T13:06:04Z -->
# e2e/checks/

## Purpose

The conformance groups of the e2e binary: one exported function per domain,
each returning a `harness.CheckGroup` whose checks call the PUBLIC `pkg/v1` API
on the host and read the effect back. `e2e/main.go`'s `conformanceGroups` lists
them, in this order: `Codec`, `Crypto`, `Logger`, `Errs`, `Process`, `Rlimit`,
`Signal`, `Cgroup`, `Reaper`, `SDNotify`, `SDListen`. The binary's rules —
platform-neutral Go, prove the effect, never hang — are in `e2e/CLAUDE.md`;
the runner is `../harness/`.

## Contents

| File | Group(s) | What a check proves on the host |
|---|---|---|
| `codec.go` | `Codec` | a Marshal→Unmarshal round trip for eighteen Formats through `pkg/v1/data/codec` — the structured and binary ones over a string-keyed map, XML and ASN.1 DER over a tagged struct, the base-N family over a scalar — and that the registry was populated by the import |
| `crypto.go` | `Crypto` | the `pkg/v1/crypto` facades: AEAD seal/open (and a wrong AAD refused), a deterministic hash, sign/verify, a deterministic KDF, a password hash round trip, a MAC, a key agreement both sides share |
| `logger.go` | `Logger` | `pkg/v1/observe/logger` writing to a buffer: the text output, attributes, `framework_version`, the level filter, the builder, the default logger |
| `errs.go` | `Errs` | `pkg/v1/errs`: code and reason introspection, the Public/Private split, `HasCode`/`HasReason`, the code's octets, the exit and HTTP mappings, every accessor degrading on a non-SDK error |
| `proc_spawn.go` | `Process`, `Rlimit`, `Signal` | a real shell spawned through `pkg/v1/proc/process`: the exit status, stdio capture, a group-aware stop; `pkg/v1/proc/rlimit`'s trampoline read back with `ulimit` (open files, core, the umask); `pkg/v1/proc/signal`'s `Parse`/`String` round trip and `Relay` |
| `cgroup.go` | `Cgroup` | `pkg/v1/proc/cgroup`: a group created, its limit read back from `/sys/fs/cgroup`, a child placed before exec, freeze/thaw/kill |
| `reaper.go` | `Reaper` | `pkg/v1/proc/reaper`: subreaper acquisition classified by GOOS, a reparented grandchild adopted and reaped |
| `proc_systemd.go` | `SDNotify`, `SDListen` | `pkg/v1/proc/systemd/notify`'s Listen/Ready/Recv round trip and its no-op contract; `pkg/v1/proc/systemd/listen`'s activator-side `Prepare` and the empty-set service contract |
| `*_external_test.go` | — | each group's registration (`assertGroup`): its domain label, at least the checks it must hold, no nil check — a check dropped from a group is a behaviour nobody proves |
| `*_internal_test.go` | — | the checks and their helpers driven directly, asserting only what holds on every host: a check runs to completion and returns a row the runner can tally |

## Rules

- **The verdict follows the platform contract.** A `coreproc.CodeUnsupportedPlatform`
  answer (matched with `perrs.HasCode`) is `harness.NotSupported`, a success; an
  environmental gap (no `/bin/sh`, no cgroup delegation, no permission) is
  `harness.Skipped`; wrong behaviour is `harness.Failed` with the observed value
  in its detail.
- **Imports are `pkg/v1/*` facades**, plus `internal/core/proc` for the sentinel
  CODES a check compares against — nothing else of `internal/`.
- **A new domain is a new exported function here and a line in
  `conformanceGroups`**, with an external test pinning its registration: a group
  built and not listed contributes no rows, and the table shows nothing missing.
- **No build tags.** A check calls the API on every OS and branches on the
  result.

## Verify

```sh
cd e2e && GOWORK=off go test -race ./checks/
cd e2e && GOWORK=off go run .        # the conformance table for this host
```
