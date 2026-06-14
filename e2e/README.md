# e2e — SDK conformance binary

A standalone program that exercises the SDK's **public `pkg/v1` API** on the host
and exits non-zero if any behaviour that *should* work on this platform is wrong.
It is the runtime counterpart to the cross-compile matrix: a green `go build`
proves the syscalls link; this binary proves cgroups actually enforce, the rlimit
trampoline actually caps fds, signals actually deliver, the subreaper actually
adopts orphans — on the real kernel.

## Run it

```sh
cd e2e && GOWORK=off go run .
```

It prints one line per check and a summary, e.g.:

```
cgroup      UNSUPPORTED cgroup/lifecycle   cgroup v2 not delegated here
process     PASS        exit-code          exit code 7
rlimit      PASS        nofile             ulimit -n = 64 (trampoline applied)
reaper      PASS        orphan-adoption    grandchild reparented and reaped
...
summary: 58 pass · 0 fail · 1 unsupported · 0 skip
```

`GOWORK=off` is required because the module is intentionally **outside** the root
`go.work` (so the Bazel `go_deps` graph ignores it); it resolves `pkg`/`internal`
via its own `replace` directives.

## Statuses

| Status | Meaning |
|---|---|
| `PASS` | behaviour exercised, observable effect correct |
| `FAIL` | should work here but the effect was wrong → **exit 1** |
| `UNSUPPORTED` | the platform has no native mechanic; the API returned the uniform `UnsupportedPlatform` contract (a success) |
| `SKIP` | environmental gap (no `/bin/sh`, no cgroup delegation) — no claim made |

Only `FAIL` counts against the exit code.

## Where it runs

`.github/workflows/e2e-vm.yml` cross-compiles this binary per GOOS, ships it to
each real OS VM (kodflow/labs Proxmox), and runs it on the real kernel — the
definitive "the SDK works everywhere" proof. Locally it runs on whatever host you
are on.
