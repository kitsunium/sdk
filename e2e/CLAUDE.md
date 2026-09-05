# e2e/

## Purpose

The SDK conformance binary: a runtime test harness that calls the **public
`pkg/v1` API** and asserts the observable effect on the host, so we know the SDK
*works* on a platform, not merely that it *compiles* (the build bar is the
cross-platform matrix; this is the runtime bar — see ADR 0018). Built per-GOOS
and run on each real OS VM by `.github/workflows/e2e-vm.yml`.

## Shape

```
e2e/
├── go.mod              own module (github.com/kitsunium/sdk/e2e); replace → ../pkg + ../internal/*
├── main.go            orchestrator: conformanceGroups() lists every domain, exit = Fail count
├── harness/           the runner
│   ├── harness.go      Status, Result, Run, safeRun, report (table + summary)
│   └── checkgroup.go   Check, CheckGroup — a domain's named group of checks
└── checks/            one file per domain group; each exports func <Domain>() harness.CheckGroup
    ├── codec.go        Marshal/Unmarshal round-trip per Format
    ├── crypto.go       AEAD seal/open, hash, sign, kdf, password, mac, agree
    ├── observability.go logger (buffer capture + level filter) + errs (introspection)
    ├── proc_spawn.go   process (exit/stdio/group-stop) + rlimit (trampoline readback) + signal
    ├── proc_resource.go cgroup (limit readback from /sys/fs/cgroup, freeze/thaw/kill) + reaper (orphan adoption)
    └── proc_systemd.go  sdnotify (readiness round-trip) + sdlisten (activation env)
```

## Rules

- **Platform-neutral Go, no build tags.** The public API compiles on every GOOS
  and returns a typed `UnsupportedPlatform` off its native platform, so each
  check calls it on any OS and branches: `perrs.HasCode(err,
  coreproc.CodeUnsupportedPlatform)` → `harness.NotSupported` (a *success*, the
  expected off-platform contract). Environmental gaps (no `/bin/sh`, no cgroup
  delegation) → `harness.Skipped`. Genuine wrong behaviour → `harness.Failed`.
- **Prove the effect, not the return.** A cgroup check reads `memory.max` back
  from `/sys/fs/cgroup`; an rlimit check reads `ulimit -n` from a spawned shell.
  A nil error is not proof.
- **Never hang, never panic.** Bounded deadlines on any blocking receive;
  `harness.Run` wraps each check in a panic-recover.
- **The tree is linted like the rest of the SDK** (`ktn-linter`, phases 1-8). It
  used to be excluded as "a runtime test harness, not library code"; the rules
  turned out to apply, and the exemption was mostly hiding missing tests. The
  unit tests here pin what holds on EVERY host — that a check runs to completion
  and produces a row the runner can tally — and leave "does the host conform?" to
  the conformance lane, so a laptop without cgroup delegation stays green.
- Imports may reach `internal/*` for the sentinel **codes only** (the e2e binary
  is internal tooling, not a consumer) — this is why it is excluded from the
  Bazel visibility firewall (`.bazelignore`).

## Build system

- **Outside `go.work`** on purpose: the Bazel `go_deps` extension reads `go.work`
  and cannot process this extra module, so `e2e` is in `.bazelignore` and built
  by `go build` only (`GOWORK=off`, via its `replace` directives). Validated by
  the `cross-platform.yml` matrix (cross-compile) and `e2e-vm.yml` (real-kernel
  run). It is intentionally NOT a Bazel target.

## Do NOT

- Add build tags to a check — branch on the runtime `UnsupportedPlatform` result.
- Assert on a nil error alone — read the observable effect back.
- Add it to `go.work` or a Bazel target (it breaks `go_deps`; keep it go-build-only).
