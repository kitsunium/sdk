# exec — process spawn primitive

`internal/service/proc/exec` is the concrete spawn behind `pkg/v1/process`. It
forks/execs an immutable `coreproc.Spec` into a running process and returns a
`coreproc.Process` handle with `PID`, `Wait`, `Signal`, `SignalGroup`, and a
group-aware `Stop`.

```go
import (
    "context"
    coreproc "github.com/kitsunium/sdk/internal/core/proc"
    svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

p, err := svcexec.Start(context.Background(), coreproc.Spec{
    Path:    "/bin/sh",
    Args:    []string{"sh", "-c", "sleep 1000 & wait"},
    Setpgid: true, // group-killable tree
})
if err != nil {
    return err
}
// SIGTERM the group, escalate to SIGKILL after 5s.
_ = p.Stop(context.Background(), 5*time.Second, coreproc.Signal(syscall.SIGTERM))
exit, _ := p.Wait()
```

## What it does

- **Credentials** — `Spec.User/Group/Groups` (names or numeric ids) resolved via
  `os/user` into a `syscall.Credential`.
- **Isolation** — `Setpgid` (own process group), `Setsid` (new session).
- **Known-state env** — nil `Spec.Env` ⇒ empty environment, never inherited.
- **Best-effort attrs** — `Nice` (setpriority) and `OOMScoreAdj` (procfs),
  applied post-start; a refusal returns a typed error and the child is torn down.
- **Group-aware Stop** — signals `-pgid`, waits `grace`, escalates to `SIGKILL`,
  so forked grandchildren never leak.
- **Exit accounting** — `Wait` returns code/signal plus `UserTime`/`SystemTime`
  /`MaxRSS` from the wait4 rusage.

## What it cannot do (typed error, never silent)

Go offers no in-child `setrlimit`/`umask` hook, so a `Spec` with `Rlimits` or a
non-nil `Umask` is rejected (`RlimitFailed`); an unmapped resource is
`UnknownResource`. See `CLAUDE.md` for the full rationale and the re-exec
trampoline path a future iteration could take.

## Platform

Unix only. On non-Unix targets `Start` returns `UnsupportedPlatform`; the package
compiles on every GOOS.

This is an internal implementation package. Consumers import `pkg/v1/process`.
