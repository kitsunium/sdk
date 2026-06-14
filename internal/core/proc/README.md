# proc — OS process-supervision contract

`internal/core/proc` declares the ports, value types, and error sentinels for the
SDK's **OS process-supervision** domain (ADR 0016). It is the shared foundation
behind six public facades:

| Facade | Built on |
|---|---|
| `pkg/v1/process` | `Process`, `Spec`, `ExitValue` |
| `pkg/v1/signal` | `Signal`, `Parse` |
| `pkg/v1/reaper` | `Reaper` |
| `pkg/v1/rlimit` | `Resource`, `LimitValue` |
| `pkg/v1/cgroup` | `Group` |
| `pkg/v1/sdnotify` | `Listener`, `NotificationValue` |

It is an `internal/` package — consumers import the `pkg/v1` facades, never this
package directly.

## Shape

- **Ports** (`Process`, `Reaper`, `Group`, `Listener`) are interfaces;
  implementations live in `internal/service/proc/*`, selected at build time by
  platform tag (no runtime registry).
- **Value types** (`Spec`, `ExitValue`, `LimitValue`, `NotificationValue`,
  `Signal`, `Resource`) are immutable and carry no policy.
- **Errors**: the whole domain shares one dotted-quad PP octet (`0.2.6.*`); every
  sentinel is declared in `errors.go` and only wrapped downstream.

## Platform

`process`, `signal`, and `rlimit` are Unix-wide; `reaper` (subreaper), `cgroup`,
and the `sdnotify` listener's credential check are Linux-only. Non-supporting
platforms return a no-op plus the `UNSUPPORTED_PLATFORM` sentinel — they never
panic and always compile.

## See also

- ADR 0016 — `docs/adr/0016-sdk-process-supervision-domain.md`
- `internal/core/proc/CLAUDE.md` — maintainer notes
