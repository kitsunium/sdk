# proc — OS process-supervision contract

`internal/core/proc` declares the ports, value types, and error sentinels for the
SDK's **OS process-supervision** domain (ADR 0016). It is the shared foundation
behind six public facades:

| Facade | Built on |
|---|---|
| `pkg/v1/proc/process` | `Process`, `Spec`, `ExitValue` |
| `pkg/v1/proc/signal` | `Signal`, `Parse` |
| `pkg/v1/proc/reaper` | `Reaper` |
| `pkg/v1/proc/rlimit` | `Resource`, `LimitValue` |
| `pkg/v1/proc/cgroup` | `Group` |
| `pkg/v1/proc/systemd/notify` | `Listener`, `NotificationValue` |

It is an `internal/` package — consumers import the `pkg/v1` facades, never this
package directly.

## Shape

- **Ports** (`Process`, `Reaper`, `Group`, `Listener`) are interfaces;
  implementations live in `internal/service/proc/*`, selected at build time by
  platform tag (no runtime registry).
- **Value types** (`Spec`, `ExitValue`, `LimitValue`, `NotificationValue`,
  `Signal`, `Resource`) are immutable and carry no policy.
- **Errors**: the whole domain shares one dotted-quad PP octet (`0.2.6.*`); every
  sentinel is declared in `codes_gen.go` (written by kit gen from the design, ADR 0164) and only wrapped downstream.

## Platform

`process`, `signal`, and `rlimit` are Unix-wide. The rest is per kernel, as ADR
0018's verdict table states it: the `reaper`'s subreaper on Linux, FreeBSD and
DragonFly; `cgroup` as cgroup v2 on Linux, rctl on FreeBSD and a Job Object on
Windows; adoption (`Attach`, `AttachPIDFile`) on Linux, darwin and the BSDs (ADR
0219); the `systemd/notify` listener's credential check on Linux and FreeBSD.
Non-supporting platforms return a no-op plus the `UNSUPPORTED_PLATFORM` sentinel
— they never panic and always compile.

## See also

- ADR 0016 — `docs/adr/0016-sdk-process-supervision-domain.md`
- `internal/core/proc/CLAUDE.md` — maintainer notes

The ports — `Process`, `Reaper`, `Group` and `Listener` — are generated from
`design/proc.yaml` into `design_gen.go` (ADR 0163): a port changes in the
design, then `kit gen`.
