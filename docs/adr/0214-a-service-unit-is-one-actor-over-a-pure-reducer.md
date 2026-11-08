# ADR 0214 — a service unit is one actor over a pure reducer

- **Status**: Accepted
- **Date**: 2026-11-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0016](0016-sdk-process-supervision-domain.md) §Context, "The
  supervisor's *policy* (restart/backoff, …) stays in the product": the life
  cycle of ONE service — `systemd.service(5)`'s state machine, its timeouts, its
  watchdog, `Restart=` and the start limiter — moves into the SDK as a new proc
  domain, `proc/service`. The graph between services, the timers, the unit
  files and the product's observability stay the product's.
- **Related**: [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (the range
  `0.3.105.*`), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero is
  the directive's default), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (the ports grow by siblings), [ADR 0050](0050-sdk-lifecycle-domain.md),
  [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md),
  [ADR 0112](0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md),
  [ADR 0120](0120-a-state-machine-keeps-an-agenda-not-a-sweep.md),
  [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md)
  (the layout this domain copies), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md),
  [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md);
  kitsunium/sdk#732 (S0a, this record), #733 (S0b, the engine), #735 (S0c),
  #736 (S0d), #737 (S0e), #740 (S0f), #734 (S0g), #738 (S0h), and around them
  #726 (SB1), #727 (S1), #728 (S2), #729 (S3), #730 (S4), #739 (S5), #731 (S6);
  the roadmap supervizio/agent#326.

## Context

`supervizio/agent` supervises services the way systemd does, and today carries
its own loop for it (`manager.go`, `executor.go`, `restarter.go`): a subset of
`systemd.service(5)`, no `Type=`, no `ExecStartPre/Post/Reload/Stop`, no
`RemainAfterExit`, no start limiter, a start that does not wait for readiness
(`manager.go:476-485`) and a restart on a failed health check wired by hand
(`manager.go:938`). The roadmap's three-round challenge decided two things the
SDK has to carry:

- **D2.** The life cycle of one service is reusable — MCP hosts, CI runners,
  any init-shaped Go program — so it lives in the SDK. The agent keeps the YAML
  vocabulary, the graph between services (ADR 0050 refuses a graph here), the
  timers (agent#328, ADR 0041) and the product's observability.
- **D4.** The engine is `pkg/v1/proc/service`, in the proc family (ADR 0016),
  and imports nothing of `app`: `design/sdk.yaml` has `pkg/v1/proc` require
  `[internal, pkg/v1/errs]`, and `app` requires `data` and `security`, which
  require `proc` — an edge from proc to app is a cycle. The start limiter is
  the engine's own; the restart curve is the kernel's `internal/kernel/backoff`.

ADR 0016's Context said the opposite: restart and backoff stayed in the
product. That sentence is amended here, for one service and nothing more.

## Decision

### 1. A domain of its own, laid out as `ipc` is

| Layer | Path | What it holds |
|---|---|---|
| core | `internal/core/proc/service` | the contract: the ports, the directive and state VALUES (`Type`, `Restart`, `State`, `StateValue`, `EventValue`, `CommandValue`, …), `ParseCommand`, the codes `0.3.105.*` |
| service | `internal/service/proc/service` | the engine: `Config`, `Unit`, `NewUnit`, the reducer, the I/O shell, the default `Spawner` (`proc/exec`) and a `NotifySource` over the existing sd_notify listener |
| public | `pkg/v1/proc/service` | aliases, re-exports and forwarders (ADR 0166) |

Its design is its own file, `design/proc/service.yaml` (`domain:
proc/service`), as `ipc`'s is `design/proc/ipc.yaml`: the family file
`design/proc.yaml` is edited by every other proc issue of the series, and a
domain that never touches it never conflicts with them. The range is
`0.3.105.*`, allocated to the engine and declared by the core at its path
(ADR 0160), as `ipc`'s `0.3.91.*` is.

### 2. Not a `Component`, not a `Supervisor`, not a `statemachine`

- **Not a `lifecycle.Component`.** A component is started once and stopped
  once, in an order the caller gives (ADR 0050). A service unit is started,
  stopped, restarted, reloaded and failed from the outside at any time, and
  restarts itself. A component is something a unit's HOST may be; the unit is
  not one. A caller that wants it in a lifecycle wraps `Start`/`Stop` in two
  lines — and that caller is in `app`, which may import `proc`.
- **Not `lifecycle.Supervisor`** (ADR 0112). It restarts one function after
  every early end, on one curve, and gives up past an Erlang intensity. A
  service has eight types, a dozen states, seven restart policies decided per
  exit cause, `ExecStop` that must not run after a failed start, three
  escalation signals and their timeouts, a watchdog and a notification
  protocol. Grown to that, it would be this engine with `app`'s import path —
  which D4 forbids.
- **Not `app/statemachine`** (ADR 0120). Its entities live in the caller's
  store, its transitions are declared guards evaluated on an agenda, and its
  side effects are hooks. Here the "store" is a process table the kernel owns,
  the transitions are systemd's and fixed, and every side effect — a fork, a
  signal, a timer — must be ordered with the events it causes. It is also
  `app`, which D4 forbids.

### 3. One actor per unit: a pure reducer and an I/O shell

Each `Unit` is ONE goroutine reading ONE channel of events. Everything that
happens to the unit — a request (`Start`, `Stop`, `Restart`, `Reload`, `Fail`,
`ResetFailed`), a process that exited, a timer that fired, a notification that
arrived — is an event on that channel, stamped with the injected clock's `Now`
when it is taken.

The decision of what to do is a **pure reducer**:

    reduce(model, event) → (model', []effect)

In the code the model is moved in place — `reduce(*model, *event) []effect`,
sparing a copy of several hundred bytes per event — which changes nothing of
the function: the model before is the argument, the model after is the same
variable. It reads nothing but its arguments — no clock, no randomness, no
process, no lock — and the model is plain data (no channel, no function, no handle), so a
recorded sequence of events replays to the same states and the same effects,
and the model can be serialised (§11). The effects are what the **shell** does:
spawn a command, signal a process, arm or disarm a timer, publish a snapshot,
answer a waiting caller, close received descriptors, adopt a PID. An effect
whose outcome the reducer must know — a spawn, an adoption — hands its outcome
back as the next event, processed before anything else in the channel, so the
reducer never sees the world change between an effect and its result.

What the reducer does not draw, the shell does: `RestartRandomizedDelaySec=`
and `RuntimeRandomizedExtraSec=` are drawn by the shell when it arms the timer
(the effect carries the bound), and the CLOCK_MONOTONIC instant a reload began
is read by the shell and carried by the reload's event, so the reducer stays a
function.

One goroutine per process the unit runs waits on it (`Process.Wait`) and posts
its exit; one goroutine reads the `NotifySource` and posts each message; timers
post when they fire. Every one of them is joined by `Close`. A timer's event
carries the deadline it was armed for, and the reducer ignores it unless that
deadline is still the one it holds — a re-armed or cancelled timer cannot fire
late into the next state.

### 4. Observers: a bounded, non-blocking queue, and a level snapshot

`State()` returns the unit's current `StateValue` — a LEVEL, always current,
never queued: state, sub-state, result, main and control PIDs, the main
process's last exit, `STATUS=`, `ERRNO=`/`BUSERROR=`/`VARLINKERROR=`, the
restart counter, and `Generation`, a counter that grows by one at every
snapshot the reducer publishes. It is read under a lock the actor holds for a
copy, never across an effect.

`Subscribe(capacity)` returns a channel that receives one `EventValue` — the
snapshot after the change and the state before it — per published snapshot,
and the `cancel` that ends it. The actor NEVER waits on a subscriber: when a
channel is full, the NEWEST event is dropped, the unit's observer-loss counter
(`StateValue.Dropped`, in `State()` and in every event) grows by one, and
nothing else happens.
A consumer detects the gap by `Generation` (two consecutive events whose
generations are not consecutive) and resynchronises with `State()`, which is
the level the lost events were heading to. Capacity is at least 1, at most
4096; zero is 64 (ADR 0031).

### 5. Every wait on an injected clock: the kernel's

Every timer — start, stop, abort, runtime, reload, watchdog, restart delay,
the start limiter's window — runs on `Config.Clock`, of type `Clock`, which is
an alias of `internal/kernel/clock.Timed`. The choice is the kernel's clock and
not a new edge: `internal` is already among `pkg/v1/proc`'s requirements, so
`design/sdk.yaml` changes nowhere, no cycle can appear, and a consumer passes
`pkg/v1/clock.System` or a `ManualClock` from `pkg/v1/clock`, whose types are
aliases of the same kernel types (ADR 0090). A nil clock is `clock.System`.
No file of the engine calls a wall-clock wait of package `time`; a test
audits it.

### 6. The ports

Each is declared in `design/proc/service.yaml`, written by kit into the core's
`gen_ports.go`, and frozen at its methods (ADR 0039: a later capability is a
sibling interface the engine detects by assertion).

| Port | Methods | Used by | Default |
|---|---|---|---|
| `Spawner` | `Spawn(ctx, coreproc.Spec) (coreproc.Process, error)` | every command (S0b) | `proc/exec.Start` |
| `Attacher` | `AttachPID(ctx, pid)`, `AttachPIDFile(ctx, path)` → `coreproc.Process` | `MAINPID=` (S0b), `PIDFile=` (S0c), re-adoption (S0h) | none: a `MAINPID=` naming a PID the engine did not spawn is refused |
| `CgroupKill` | `Kill()`, `Signal(sig)`, `Contains(pid)` | `MAINPID=` and `NotifyAccess=all` membership (S0b); `KillMode=control-group\|mixed` (S0g) | none: no cgroup |
| `NotifySource` | `Socket()`, `Recv(ctx) (NotifyValue, error)`, `Close()` | sd_notify (S0b; S4 makes it complete) | none: `NOTIFY_SOCKET` is not set |
| `Clock` | alias of `clock.Timed` | every wait | `clock.System` |

An adopted process (`Attacher`) is a `coreproc.Process` whose `Wait` returns
the sentinel `ExitUnknown` (`0.3.105.11`) when it is gone: it was not this
engine's child, so its status is not knowable, and the reducer reads that as
§10's "gone without status".

### 7. Stopping escalates on its own deadlines

A caller's `ctx` bounds only how long the CALLER waits for its job. The
escalation — `ExecStop`, `KillSignal`, `SIGHUP`, `FinalKillSignal`,
`ExecStopPost` — runs on the actor, every spawn of it on
`context.WithoutCancel` of the unit's context, with `TimeoutStopSec=` /
`TimeoutAbortSec=` as its only deadlines. A `Stop(ctx)` whose `ctx` expires
returns `ctx.Err()` and the stop goes on; a second `Stop` waits for the same
job.

### 8. The external API

`Start`, `Stop`, `Restart`, `Reload` are systemd's jobs; each returns when its
job is done — or when its `ctx` ends, the job going on. `ResetFailed` is
`systemctl reset-failed` (the restart counter and the start limiter flushed, a
failed unit inactive). `Fail(ctx)` is the agent's failed health check
(`manager.go:938`): it is `WATCHDOG=trigger` from outside — the unit is
aborted with `WatchdogSignal=` and ends with the result `watchdog`, so
`Restart=on-watchdog|on-failure|on-abnormal|always` restarts it, by the same
path as a missed keep-alive. Jobs merge as systemd's do: a start joins a start
running; a stop cancels a start running (its callers get `JOB_CANCELED`) and
stops; a start during a stop waits for the stop, then starts; a restart is a
stop with `RestartKillSignal=` then a start, and counts against the limiter.

### 9. Directives are typed values, and a zero is the default

No YAML key, no unit-file syntax: `Config` carries one typed field per
directive (`Type`, `Restart`, `RestartDelay`, `TimeoutStart`, …), each
documented with the directive it maps. A field's Go zero is the directive's
systemd default (ADR 0031: never inert); systemd's literal `0` and `infinity`
are the constants `Zero` (−1) and `Infinity` (the longest duration). The
deliberate exceptions are stated: `KillMode`'s zero is `process` (systemd's is
`control-group`, which needs the cgroup S0g brings; the agent passes it
explicitly), and `Type=idle` runs as `simple` (the job queue it waits for is
the agent's). `ParseCommand` reads one command's words — the agent splits the
line by `systemd.syntax(7)` and expands `%` specifiers — and parses the
prefixes `@ - : + ! |` in any order; `+` with `!`, a repeated prefix (so `!!`)
and an unknown one are `COMMAND_INVALID`. `$VAR` and `${VAR}` are expanded by
the engine at spawn time, against the unit's environment and the variables it
sets (`MAINPID`, `SERVICE_RESULT`, `EXIT_CODE`, `EXIT_STATUS`, …), unless `:`.

`+` and `!` run a control command with full privileges or without the
credentials: refused unless `Config.AllowPrivilegedControl` (false by
default); such a command runs with the fixed search path
`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`, without `LD_*`,
`ENV` or `BASH_ENV`, and always in the unit's cgroup.

### 10. The oracle: `Restart=` Table 1, and the replay that tests it

The reducer folds every outcome into the unit's result as `service.c` does
(the first failure wins) and decides a restart in `shallRestart`, whose order
is systemd's: a stop the caller asked for never restarts; an exit status in
`RestartPreventExitStatus=` never restarts; one in `RestartForceExitStatus=`
always does (but a `Type=oneshot` that succeeded); otherwise the table:

| Exit cause → result | `no` | `always` | `on-success` | `on-failure` | `on-abnormal` | `on-abort` | `on-watchdog` |
|---|---|---|---|---|---|---|---|
| clean exit code or signal → `success` | | X | X | | | | |
| unclean exit code → `exit-code` | | X | | X | | | |
| unclean signal → `signal` / `core-dump` | | X | | X | X | X | |
| timeout → `timeout` | | X | | X | X | | |
| watchdog (and `Fail`) → `watchdog` | | X | | X | X | | X |
| OOM kill (S0g) → `oom-kill` | | X | | X | X | | |
| spawn failure → `resources` | | X | | X | X | | |
| protocol (`READY=1` never sent) → `protocol` | | X | | X | X | | |
| `ExecCondition=` skip → `exec-condition` | | | | | | | |
| start limit hit → `start-limit-hit` | | | | | | | |

"Clean" is exit 0, a status in `SuccessExitStatus=`, or — except for
`Type=oneshot` — `SIGHUP`, `SIGINT`, `SIGTERM`, `SIGPIPE`. An adopted main
process that is gone without a status (S0c, `ExitUnknown`) is CLEAN, as
systemd reads a vanished main PID of a forking service (`service_enter_running`
→ `service_enter_stop(SERVICE_SUCCESS)`). `Type=oneshot` with `always` or
`on-success` is refused (`CONFIG_INVALID`), as systemd refuses it.

The replay scenario, S0b's acceptance test: for each of the seven values and
each cause the reducer is driven from `inactive` by events only — a start, the
spawn, the exit (code, signal, core dump), the timer, the missed keep-alive,
`Fail` — and the test asserts the sub-state it lands in: `auto-restart` where
the table has an X, `failed` or `dead` elsewhere; plus the exceptions (prevent,
force, oneshot, a stop the caller asked for). The restart delay is the
kernel's curve: `RestartSteps=n`, `RestartSec=b`, `RestartMaxDelaySec=m` is
`backoff.Grow(b, m, (m/b)^(1/n), restart)`, checked on the manual's example
(10 s, 4 steps, 160 s → 10, 20, 40, 80, 160, 160 s).

The start limiter is the engine's: `StartLimitIntervalSec=`/`StartLimitBurst=`
count EVERY start — the caller's, a restart job's, an automatic restart — in a
window that begins at the first start and ends `interval` later (`Infinity`:
never; `Zero`: no limit); the start past the burst ends `failed` with
`start-limit-hit`, without a restart; `ResetFailed` flushes it.

### 11. Across a re-exec of the supervisor (S0h)

The model is plain data so it can be written down: S0h serialises, with a
version, every unit's model and its descriptor store, and the incoming image
restores them. Across `execve` the PID does not change, so every child stays a
child and is waited for again (`childwait`); the descriptors to keep — pidfds,
the notification socket, the store — have `CLOEXEC` lifted under
`syscall.ForkLock` and are named in the payload, and the new image checks each
by `fstat` before it trusts it. Linux re-executes through an `O_PATH` descriptor
opened at start and `execveat`. **On the BSDs and macOS** — no `execveat`, no
pidfd — the image is re-executed by the path recorded at start, and the
children, still children, are waited for with `wait4`; the window in which that
path could be swapped is accepted, because a supervisor re-executes precisely
to run the binary an update put there. Windows has no re-exec in place and
refuses. An image that cannot read the payload's version adopts the processes
by cgroup or PID file (S3) or stops them in order, and never leaves an orphan.
`FileDescriptorStorePreserve=` decides whether the store is part of it.

### 12. The codes, `0.3.105.*`

| Code | Reason | When | Issue |
|---|---|---|---|
| `0.3.105.1` | `CONFIG_INVALID` | `NewUnit` refuses a configuration: the `directive` field names the directive | S0b |
| `0.3.105.2` | `DIRECTIVE_UNSUPPORTED` | a directive value the engine does not run yet or on this platform — cgroup modes until S0g, `Type=forking` until S0c, `Type=dbus` until S0e, the store until S0d, `NotifyAccess=exec\|all` without a cgroup | S0b |
| `0.3.105.3` | `COMMAND_INVALID` | `ParseCommand`: no word, an empty path, an unknown or repeated prefix, `+` with `!` | S0b |
| `0.3.105.4` | `START_FAILED` | a start job that ended with the unit failed; the `result` field names why | S0b |
| `0.3.105.5` | `START_LIMIT_HIT` | a start refused by the limiter | S0b |
| `0.3.105.6` | `RELOAD_FAILED` | a reload whose result is not `success` | S0b |
| `0.3.105.7` | `RELOAD_UNSUPPORTED` | no `ExecReload=` and not `Type=notify-reload` | S0b |
| `0.3.105.8` | `JOB_CANCELED` | a job a conflicting one replaced | S0b |
| `0.3.105.9` | `UNIT_CLOSED` | a call on a closed unit | S0b |
| `0.3.105.10` | `PRIVILEGE_REFUSED` | a `+` or `!` command without `AllowPrivilegedControl` | S0b |
| `0.3.105.11` | `EXIT_UNKNOWN` | an adopted process's `Wait`: gone, status unknowable | S0b (contract), S0c |
| `0.3.105.12` | `HANDOVER_INVALID` | a hand-over the incoming image cannot read: a version it does not read, a payload that does not hold together, or a refused model | S0h |
| `0.3.105.13` | `HANDOVER_MISMATCH` | a descriptor a hand-over names that is not the one recorded | S0h |
| `0.3.105.14` | `REEXEC_FAILED` | a re-exec the kernel refused | S0h |
| `0.3.105.15`–`19` | reserved | — | — |

## Coverage, directive by directive

Owner: **SDK** (this engine, or the proc package named), **agent**, or **no**
(not supported, with the reason). Platforms: **L** Linux, **B** FreeBSD,
OpenBSD, NetBSD, DragonFly, macOS, illumos and Solaris, **W** Windows; "refused"
is a typed error naming the directive, never a silent no-op. The test column
names the test (S0b) or the issue that brings it.

### `systemd.service(5)`

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `Type=simple` | SDK | yes | yes | yes | `TestTypes` |
| `Type=exec` | SDK — active once `Spawn` returned, which the SDK's spawner does after `execve` succeeded; so `simple` and `exec` both report an exec failure as a start failure (a stated difference: systemd's `simple` reports it after) | yes | yes | yes | `TestTypes` |
| `Type=oneshot` | SDK, several `ExecStart=` | yes | yes | yes | `TestOneshot*` |
| `Type=notify` | SDK | yes | FreeBSD (S4), else refused | refused | `TestNotify*` |
| `Type=notify-reload` | SDK | yes | FreeBSD (S4), else refused | refused | `TestReloadNotify*` |
| `Type=idle` | SDK runs it as `simple`; the wait for the job queue is the agent's | yes | yes | yes | `TestTypes` |
| `Type=forking` | SDK S0c (#735); refused until then | S0c | S0c | refused | S0c |
| `Type=dbus` | SDK S0e (#737); refused until then | S0e | refused | refused | S0e |
| `ExitType=main` | SDK | yes | yes | yes | `TestTypes` |
| `ExitType=cgroup` | SDK S0g (#734); refused until then | S0g | refused | refused | `TestCgroupDirectivesRefused` |
| `RemainAfterExit=` | SDK | yes | yes | yes | `TestOneshotRemainAfterExit` |
| `GuessMainPID=` | SDK S0c | S0c | S0c | refused | S0c |
| `PIDFile=` | SDK S0c, read by S3's `AttachPIDFile` only | S0c | S0c | refused | S0c |
| `BusName=` | SDK S0e | S0e | refused | refused | S0e |
| `ExecStart=` | SDK | yes | yes | yes | every test |
| `ExecStartPre=`, `ExecStartPost=` | SDK | yes | yes | yes | `TestExecChain` |
| `ExecCondition=` | SDK: 1–254 skips, 255 and abnormal fail | yes | yes | yes | `TestExecCondition` |
| `ExecReload=` | SDK, `$MAINPID` set | yes | yes | yes | `TestReload*` |
| `ExecReloadPost=` | SDK, after a successful reload | yes | yes | yes | `TestReload*` |
| `ExecStop=` | SDK, only after a successful start | yes | yes | yes | `TestExecStop*` |
| `ExecStopPost=` | SDK, `SERVICE_RESULT`, `EXIT_CODE`, `EXIT_STATUS` set | yes | yes | yes | `TestExecStopPostEnv` |
| prefixes `@ - : + ! \|` | SDK `ParseCommand`; `!!` refused | yes | yes | `+`/`!` refused | `TestParseCommand`, `TestPrefixes` |
| `RestartSec=` | SDK, default 100 ms | yes | yes | yes | `TestRestartDelay` |
| `RestartSteps=`, `RestartMaxDelaySec=` | SDK, the kernel's backoff curve | yes | yes | yes | `TestRestartDelayManualExample` |
| `RestartRandomizedDelaySec=` | SDK, drawn by the shell | yes | yes | yes | `TestRestartRandomizedDelay` |
| `TimeoutStartSec=`, `TimeoutStopSec=`, `TimeoutSec=` | SDK (`TimeoutSec=` is the agent's shorthand for the two) | yes | yes | yes | `TestTimeouts` |
| `TimeoutAbortSec=` | SDK | yes | yes | yes | `TestWatchdog*` |
| `TimeoutStartFailureMode=`, `TimeoutStopFailureMode=` | SDK: terminate, abort, kill | yes | yes | yes | `TestTimeoutModes` |
| `RuntimeMaxSec=`, `RuntimeRandomizedExtraSec=` | SDK | yes | yes | yes | `TestRuntimeMax` |
| `WatchdogSec=` | SDK, `WATCHDOG_USEC`/`WATCHDOG_PID` set | yes | FreeBSD (S4) | refused | `TestWatchdog*` |
| `Restart=` | SDK, Table 1 | yes | yes | yes | `TestRestartTable1Replay` |
| `RestartMode=normal\|direct\|debug` | SDK; `debug` sets `DEBUG_INVOCATION=1` on the restarted unit | yes | yes | yes | `TestRestartMode` |
| `SuccessExitStatus=`, `RestartPreventExitStatus=`, `RestartForceExitStatus=` | SDK, codes and signals | yes | yes | yes | `TestExitStatusSets` |
| `RootDirectoryStartOnly=` | SDK with S2 (#728), `RootDirectory=` being S2's | S2 | refused | refused | S2 |
| `NonBlocking=` | SDK with S5 (#739) | S5 | S5 | refused | S5 |
| `NotifyAccess=` | SDK: `main` implied by `notify*`, `WatchdogSec=`, the store; `exec\|all` need a cgroup | yes | FreeBSD `main` (S4) | refused | `TestNotifyAccess` |
| `Sockets=` | agent: socket units are its vocabulary; the descriptors reach the SDK through S5 | — | — | — | agent |
| `FileDescriptorStoreMax=` | SDK S0d (#736); refused until then | S0d | FreeBSD (S0d) | refused | S0d |
| `FileDescriptorStorePreserve=` | SDK S0d and S0h | S0d | S0d | refused | S0d |
| `LUOSession=` | no: the Live Update Orchestrator is a kernel interface of `kexec`, which a Go supervisor does not drive | — | — | — | — |
| `USBFunctionDescriptors=`, `USBFunctionStrings=` | no: FunctionFS gadgets belong to socket units | — | — | — | — |
| `OOMPolicy=` | SDK S0g (#734); refused until then | S0g | refused | refused | `TestCgroupDirectivesRefused` |
| `OpenFile=` | agent opens, SDK passes (S5) | S5 | S5 | refused | S5 |
| `ReloadSignal=` | SDK, default `SIGHUP` | yes | FreeBSD (S4) | refused | `TestReloadNotify*` |
| `RefreshOnReload=` | no: extension images and credentials are not supervised | — | — | — | — |

### `systemd.kill(5)`

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `KillMode=process` | SDK (the zero) | yes | yes | yes | `TestKillModes` |
| `KillMode=none` | SDK: nothing is signalled, the main process is left | yes | yes | yes | `TestKillModes` |
| `KillMode=control-group`, `KillMode=mixed` | SDK S0g, over `CgroupKill`; refused until then | S0g | refused | refused | `TestCgroupDirectivesRefused` |
| `KillSignal=` | SDK, default `SIGTERM`, followed by `SIGCONT` | yes | yes | `SIGKILL` only (Windows has no other) | `TestStopEscalation` |
| `RestartKillSignal=` | SDK, during a restart job | yes | yes | as above | `TestRestartKillSignal` |
| `SendSIGHUP=` | SDK | yes | yes | refused | `TestStopEscalation` |
| `SendSIGKILL=` | SDK | yes | yes | yes | `TestStopEscalation` |
| `FinalKillSignal=` | SDK, default `SIGKILL` | yes | yes | as above | `TestStopEscalation` |
| `WatchdogSignal=` | SDK, default `SIGABRT` | yes | yes | refused | `TestWatchdog*` |

### `systemd.unit(5)` — the start limit, the actions

| Directive | Owner | Note |
|---|---|---|
| `StartLimitIntervalSec=`, `StartLimitBurst=` | SDK | every start counted, `Infinity`, `Zero`, flushed by `ResetFailed`; `TestStartLimit` |
| `StartLimitAction=` | agent | an action on the SYSTEM (reboot, exit…): the engine reports `start-limit-hit`, the agent acts |
| `SuccessAction=`, `FailureAction=`, `SuccessActionExitStatus=`, `FailureActionExitStatus=`, `RebootArgument=` | agent | idem, on `success` / any other result |
| `SurviveFinalKillSignal=` | SDK | `Unit.Close` — the supervisor's own end, its "final kill" — leaves such a unit's processes running |
| `OnSuccess=`, `OnFailure=`, `JobTimeoutSec=`, `JobRunningTimeoutSec=`, `JobTimeoutAction=`, `Condition…=`, `Assert…=` and every dependency | agent | the graph and the job queue (ADR 0050) |

### `systemd.exec(5)` — launching

`proc/exec` is the spawner; a directive owned "exec" is a field of
`coreproc.Spec` the unit's `Config.Spec` carries to every command.

| Directives | Owner |
|---|---|
| `WorkingDirectory=` | exec (`Spec.Dir`) |
| `User=`, `Group=`, `SupplementaryGroups=` | exec (`Spec.User`, `Group`, `Groups`); `!` lifts them, `+` too |
| `Environment=`, `UnsetEnvironment=` | exec (`Spec.Env`, built by the agent); `EnvironmentFile=` and `PassEnvironment=` are read by the agent (#935's allow-list is exec's) |
| `UMask=`, `Nice=`, `OOMScoreAdjust=` | exec |
| `LimitCPU=`, `LimitFSIZE=`, `LimitDATA=`, `LimitSTACK=`, `LimitCORE=`, `LimitAS=`, `LimitNOFILE=`, `LimitNPROC=`, `LimitMEMLOCK=` | exec (`Spec.Rlimits`) |
| `LimitRSS=`, `LimitLOCKS=`, `LimitSIGPENDING=`, `LimitMSGQUEUE=`, `LimitNICE=`, `LimitRTPRIO=`, `LimitRTTIME=` | exec, to add to `Resource` (refused until then) |
| `CPUSchedulingPolicy=`, `CPUSchedulingPriority=`, `CPUSchedulingResetOnFork=`, `IOSchedulingClass=`, `IOSchedulingPriority=` | exec, #940 |
| `CPUAffinity=`, `NUMAPolicy=`, `NUMAMask=`, `TimerSlackNSec=`, `Personality=`, `CoredumpFilter=`, `KeyringMode=` | no until an issue asks: each is one syscall the trampoline would make |
| `StandardInput=`, `StandardOutput=`, `StandardError=` | exec (`Spec.Stdio`, `Stdin`/`Stdout`/`Stderr`); journal, socket and file targets are the agent's writers |
| `StandardInputText=`, `StandardInputData=` | agent (a reader in `Spec.Stdin`) |
| `TTYPath=`, `TTYReset=`, `TTYVHangup=`, `TTYColumns=`, `TTYRows=`, `TTYVTDisallocate=` | exec, #934 (a pseudo-terminal); a console VT is no |
| `IgnoreSIGPIPE=` | exec (Go starts a child with default dispositions) |
| `ExecSearchPath=` | the unit's `PATH` in `Spec.Env`; `+`/`!` use the fixed path (§9) |
| `CapabilityBoundingSet=`, `AmbientCapabilities=`, `NoNewPrivileges=`, `SecureBits=` | S2 (#728) |
| `RootDirectory=`, `RootImage=`, `RootImageOptions=`, `RootEphemeral=`, `RootHash=`, `RootHashSignature=`, `RootVerity=`, `RootImagePolicy=`, `MountImagePolicy=`, `ExtensionImagePolicy=`, `RootMStack=`, `MountAPIVFS=`, `BindLogSockets=`, `ProtectProc=`, `ProcSubset=`, `BindPaths=`, `BindReadOnlyPaths=`, `MountImages=`, `ExtensionImages=`, `ExtensionDirectories=`, `TemporaryFileSystem=`, `PrivateTmp=`, `PrivateDevices=`, `PrivateMounts=`, `MountFlags=`, `ProtectSystem=`, `ProtectHome=`, `ReadWritePaths=`, `ReadOnlyPaths=`, `InaccessiblePaths=`, `ExecPaths=`, `NoExecPaths=` | S2's mount stage for directories and binds; images, verity and extensions are no |
| `PrivateNetwork=`, `NetworkNamespacePath=`, `PrivateIPC=`, `IPCNamespacePath=`, `PrivateUsers=`, `UserNamespacePath=`, `PrivatePIDs=`, `ProtectHostname=`, `RestrictNamespaces=`, `DelegateNamespaces=` | S2 (namespaces) |
| `ProtectClock=`, `ProtectKernelTunables=`, `ProtectKernelModules=`, `ProtectKernelLogs=`, `ProtectControlGroups=`, `LockPersonality=`, `MemoryDenyWriteExecute=`, `RestrictRealtime=`, `RestrictSUIDSGID=`, `RestrictAddressFamilies=`, `SystemCallFilter=`, `SystemCallErrorNumber=`, `SystemCallArchitectures=`, `SystemCallLog=` | S2 (seccomp, mounts); the `@group` names are the agent's policy (agent#331) |
| `RestrictFileSystems=`, `PrivateBPF=`, `BPFDelegateCommands=`, `BPFDelegateMaps=`, `BPFDelegatePrograms=`, `BPFDelegateAttachments=` | S6 (#731) where BPF reaches; else no |
| `DynamicUser=`, `SetLoginEnvironment=`, `PAMName=`, `SELinuxContext=`, `AppArmorProfile=`, `SmackProcessLabel=`, `MemoryKSM=`, `MemoryTHP=`, `RemoveIPC=` | no: each needs a library or a policy daemon outside the standard library (ADR 0156) |
| `RuntimeDirectory=`, `StateDirectory=`, `CacheDirectory=`, `LogsDirectory=`, `ConfigurationDirectory=` and their `…Mode=`, `…Quota=`, `…Accounting=`, `RuntimeDirectoryPreserve=`, `TimeoutCleanSec=` | agent (directories it creates before `Start`) |
| `LogLevelMax=`, `LogExtraFields=`, `LogRateLimitIntervalSec=`, `LogRateLimitBurst=`, `LogFilterPatterns=`, `LogNamespace=`, `SyslogIdentifier=`, `SyslogFacility=`, `SyslogLevel=`, `SyslogLevelPrefix=` | agent (its log pipeline) |
| `LoadCredential=`, `LoadCredentialEncrypted=`, `ImportCredential=`, `SetCredential=`, `SetCredentialEncrypted=` | agent |
| `UtmpIdentifier=`, `UtmpMode=` | no |
| the variables the manager sets: `MAINPID`, `MANAGERPID`, `NOTIFY_SOCKET`, `WATCHDOG_PID`, `WATCHDOG_USEC`, `SERVICE_RESULT`, `EXIT_CODE`, `EXIT_STATUS`, `DEBUG_INVOCATION` | SDK; `LISTEN_*` S5, `FDSTORE` S0d, `INVOCATION_ID` the agent's |

### Windows

The subset that runs: the reducer, every `Type=` that needs no notification
(`simple`, `exec`, `oneshot`, `idle`), `Exec*` without `+`/`!`, the timeouts
without `abort` (no signal to send but `SIGKILL`), `Restart=`, the limiter.
Windows delivers no signal but a kill (`TerminateProcess`), so there the zero
`KillSignal=`, `RestartKillSignal=`, `FinalKillSignal=` and `WatchdogSignal=`
are `SIGKILL`, any other signal is refused at `NewUnit` (`DIRECTIVE_UNSUPPORTED`),
and so are `SendSIGHUP=`, `WatchdogSec=` and every notification type.

## Consequences

- The agent drops its loop for `pkg/v1/proc/service` (agent A1): one `NewUnit` per
  unit file, the YAML read into a `Config`, its graph above.
- The engine's correctness is the reducer's, and the reducer is tested by
  replay: no process, no sleep, no flake.
- `proc/service` is the second proc domain with a core of its own; its design
  file is its own, so S0c to S0h change it alone.
- With l4-exec's work (#928 `ProcessHandle`, #931 `DieWithParent`, #935
  `WaitContext`, #1089 the sealed fd table), the default spawner gains a
  race-free signal and a bounded wait; the port does not change.

## Breaking changes

None: a new domain, a new range, three new packages. ADR 0016's Context
sentence is amended, not its decision.

## Alternatives considered

- **Extend `lifecycle.Supervisor`, or build on `app/statemachine`.** Refused
  in §2: wrong shape, and an `app` import D4 forbids.
- **A port per directive group, or a plug-in registry of service types.** A
  registry hides which type does what; the eight types are systemd's and
  closed. A new type (S0c, S0e) is a reducer branch and a refusal lifted.
- **An impure state machine (methods that fork and sleep).** What systemd
  writes in C, and why its tests are VMs. A pure reducer replays the Table 1
  oracle in microseconds and serialises for S0h.
- **A new edge `proc → clock` written by kit.** No gain: the kernel clock is
  the same type, already reachable (§5).
- **A blocking observer channel.** A slow consumer would stall the unit's
  escalation — a watchdog missed because a dashboard was busy. Dropped, counted
  and resynchronised instead (§4).
- **`KillMode`'s zero as `control-group`.** It would make every zero `Config`
  refused until S0g, and change meaning when S0g lands; a zero that works
  everywhere, stated, is ADR 0031's reading.

## Amendment — 2026-11-08: a forking daemon is adopted from its PID file or its cgroup ([#735](https://github.com/kitsunium/sdk/issues/735), S0c)

The body above is kept as it was decided; where it and this section disagree,
this section holds.

1. **The forking command is a control process.** `Type=forking`'s
   `ExecStart=` runs as service.c runs it — a control process, `SubStart`,
   under `TimeoutStartSec=` — given the main process's variables
   (`NOTIFY_SOCKET`, `WATCHDOG_USEC`) for the daemon it forks to inherit. A
   failure of it fails the start; its clean end looks for the daemon.
2. **The PID file is read by S3 alone.** With `PIDFile=` (absolute and clean,
   else `CONFIG_INVALID`) the shell calls `Attacher.AttachPIDFile` and the
   engine never opens the file. A read refused — the file absent, a symbolic
   link, garbage, a PID outside the attacher's scope — is retried every
   100 ms on the unit's clock, systemd's inotify watch made a wait the
   reducer can replay, until `TimeoutStartSec=` ends it with the result
   `timeout`; `START_FAILED` then names the last refusal's reason in its
   `pidfile` field. With an `ExecStartPost=` the file is read again after it
   (service.c: "the PID file might be created by a START_POST script"); with
   a cgroup that lists nobody the start fails at once with `protocol`
   (`cgroup_good() == 0`).
3. **Without a PID file, the cgroup.** `CgroupMembers` — `Members() ([]int,
   error)`, `cgroup.procs` as proc's `GroupReader.Members` lists it — is a
   sibling of `CgroupKill` (ADR 0039) the engine asserts on `Config.Cgroup`.
   `GuessMainPID=` (default yes) adopts, through `Attacher.AttachPID`, the one
   member whose parent is not a member: `unit_search_main_pid`'s "a child of
   the manager" made independent of whether the supervisor reaps orphans —
   the daemon's parent is gone, its workers' parent is the daemon. None or
   several, or `GuessMainPID=no`, leaves the main process unknown and the unit
   up while the cgroup has members (`service_good` falling back on
   `cgroup_good`), stopped cleanly when it has none.
4. **An adopted daemon's end has no status.** Its `Wait` is
   `STATUS_UNKNOWN`, read as §10's clean "gone without status": `Restart=` on
   it follows Table 1's success row — `always` and `on-success` restart, no
   other policy does.
5. **Refusals.** Windows refuses `Type=forking` (`DIRECTIVE_UNSUPPORTED`); a
   forking unit without an `Attacher` is `CONFIG_INVALID`; without `PIDFile=`
   and without a `CgroupMembers` it is `DIRECTIVE_UNSUPPORTED` naming
   `GuessMainPID` — its main process could never be found. Every other type
   ignores `PIDFile=` and `GuessMainPID=`, as systemd does.
6. **Stated divergences.** The engine never removes `PIDFile=` (systemd
   unlinks it after the stop): it writes no path a service may steer, and the
   attacher's scope (`NewAttacher`'s cgroup or users) bounds what a stale
   file may name. A `MAINPID=` that named the daemon before its command ended
   is kept, the file not read again. Off Linux the guess finds nothing (no
   parent is read without `/proc`), and `NewAttacher` is
   `UNSUPPORTED_PLATFORM` (S3's BSD half, ADR 0219).

The ports table gains a row:

| Port | Methods | Used by | Default |
|---|---|---|---|
| `CgroupMembers` | `Members() ([]int, error)`, asserted on `Config.Cgroup` | `GuessMainPID=`, the PID file's wait (S0c) | none: a forking unit needs `PIDFile=` |

The coverage rows of `systemd.service(5)`, restated:

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `Type=forking` | SDK S0c: the command a control process, the daemon adopted | yes | with an `Attacher` that adopts there (`NewAttacher` adopts on darwin and the BSDs since ADR 0219's amendment of 2026-11-08, #729; illumos and Solaris refuse) | refused | `TestForkingPIDFile`, `TestForkingCommandFails`, `TestRealForkingPIDFile` |
| `GuessMainPID=` | SDK S0c, over `CgroupMembers` | yes | the guess finds none (no parent read) | refused | `TestForkingGuess`, `TestRealForkingGuess` |
| `PIDFile=` | SDK S0c, read by S3's `AttachPIDFile` only, retried until `TimeoutStartSec=`, never removed | yes | as `Type=forking` | refused | `TestForkingPIDFileRetriedUntilTimeout`, `TestForkingPIDFileFromStartPost`, `TestForkingEmptyCgroupIsProtocol`, `TestRealForkingPIDFileRefused` |

A PID reused between the read and the adoption is S3's to detect: the pidfd
is opened, the scope checked, then the process proven still alive
(`exec.Attach`, ADR 0219 §2); a stale file naming a process gone is refused
and read again.

## Amendment — 2026-11-08: the descriptor store is a table the shell owns ([#736](https://github.com/kitsunium/sdk/issues/736), S0d)

The body above is kept as it was decided; where it and this section disagree,
this section holds.

1. **Who owns what.** The reducer keeps the store as plain entries — an id,
   the `FDNAME=`, whether a hangup removes it — in the model, oldest first,
   with the key it was filled under; the shell holds the descriptors by id
   (`efStore`, `efStoreDrop`). The model stays plain data (§11), and S0h
   serialises the entries and hands the descriptors by id.
2. **What `FDSTORE=1` keeps.** An admitted message's descriptors are stored
   under `FDNAME=` — `stored` when absent or invalid, as systemd ignores an
   invalid name —, with `MAINPIDFD=1`'s pidfd, the first descriptor, never
   among them. A descriptor referring to an object stored already, or to one
   before it in the message, is dropped: systemd's `same_fd`, without `kcmp`
   (which the standard library does not name on every architecture): the
   same device and inode, never a device node, the same access mode. Past
   `FileDescriptorStoreMax=` — or on a unit without a store — a descriptor is
   closed and counted (`StateValue.FDStoreRefused`). Every descriptor a
   message carried that the store does not keep is closed when the message is
   taken — what `BARRIER=1` waits for —, and an unadmitted sender's at once.
   The listener's adapter closes the sender's pidfd (`SCM_PIDFD`), which no
   message carried.
3. **`FDSTOREREMOVE=1`** with a valid `FDNAME=` closes every descriptor of
   that name; without one nothing is.
4. **`FDPOLL=`.** Unless `FDPOLL=0`, a stored descriptor is watched in an
   epoll set with no event asked (`EPOLLHUP`, `EPOLLERR`), once
   (`EPOLLONESHOT`), under its id; its hangup removes it. An object epoll
   cannot watch (a regular file) stays, unwatched (`StoredFDValue.Poll`
   false). The watch's one goroutine starts with the first watched descriptor
   and is joined when the unit ends; a descriptor leaves the set before it is
   closed, and descriptors are lent to the kernel through `SyscallConn`, never
   `os.File.Fd`, which would turn the service's own open file blocking.
5. **The hand-over is the main process's alone.** Every `ExecStart=` spawn
   — a forking unit's command and each of a oneshot's included, as
   `EXEC_PASS_FDS` is — receives the store, newest first as
   `service_collect_fds` walks systemd's list, through
   `listen.PrepareFiles`: after the sockets `Config.Spec` already activates
   (`LISTEN_FDS` counting them all), at fd 3 on, with `LISTEN_FDNAMES` and the
   child's own `LISTEN_PID`; no other command gets a descriptor. Every command
   of a unit with a store gets `$FDSTORE`.
6. **The key.** A store is filled under `<name>/<sha256>` of the main
   process's identity — every `ExecStart=` command, the user, the group and
   the groups (the sandbox, once S2 brings one). Before a store is handed or
   added to, one whose key the unit does not match is closed whole and
   counted (`StateValue.FDStoreDiscarded`). Within one unit the key never
   changes, `Config` being fixed at `NewUnit`: the check guards what S0h
   restores, and is tested by injection.
7. **`FileDescriptorStorePreserve=`.** `restart` (the zero) releases the
   store once the unit rests dead — inactive, failed or skipped — with no job
   running or queued and no restart due; `no` at every `enterDead`, a restart
   to come included (`service_enter_dead`); `yes` at `Close` only;
   `on-success` at `Close` or once the unit is failed for good. `Close`
   releases every store.
8. **Listing.** `Unit.FDStore` lists the store — name, device, inode, mode,
   watched — newest first, never the descriptors; `StateValue.FDStore` counts
   them, so every change is a published event.
9. **Platforms and refusals.** Linux only: the store's hangup watch
   (epoll), its duplicate test and its ids are Linux's (`fdstore_linux.go`),
   so `FileDescriptorStoreMax=` is `DIRECTIVE_UNSUPPORTED` elsewhere — on
   FreeBSD too, whose notify socket carries descriptors since ADR 0219's
   amendment of 2026-11-08 (#730): the store is not ported there. A negative maximum, or a store without
   a `NotifySource`, is `CONFIG_INVALID`; a store implies
   `NotifyAccess=main`.
10. **A step answers last.** The reducer's answers to callers (`efReply`,
    `efExit`) are now the last effects of their step, so a caller whose job
    returned reads the snapshot and the store the job left; `Close` returns
    once the actor's end — the store and the socket closed — is complete.

The coverage rows, restated:

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `FileDescriptorStoreMax=` | SDK S0d | yes | refused | refused | `TestStoreKeepsNamesRefusesRemoves`, `TestStoreClosesWhatItDoesNotKeep`, `TestStoreHandedToExecStartOnly`, `TestRealFDStore`, `TestRealFDStoreLeaksNothing` |
| `FileDescriptorStorePreserve=` | SDK S0d (across a re-exec, S0h) | yes | refused | refused | `TestStorePreserve` |
| `FDSTORE=1`, `FDSTOREREMOVE=1`, `FDNAME=`, `FDPOLL=0` | SDK S0d | yes | refused | refused | `TestStoreKeepsNamesRefusesRemoves`, `TestStoreHangup`, `TestRealFDStoreHangup` |
| `$FDSTORE` | SDK S0d | yes | refused | refused | `TestStoreSettings` |

## Amendment — 2026-11-08: one instance per accepted connection ([#740](https://github.com/kitsunium/sdk/issues/740), S0f)

The body above is kept as it was decided; where it and this section disagree,
this section holds.

1. **The primitive.** `SpawnInstance(ctx, template, instance, conn)` is
   `systemd.socket(5)`'s `Accept=yes` reduced to what the SDK carries: a copy
   of `template` named `instance` — the identifier is the caller's, the SDK
   generates none and composes no `@` name —, owning `conn` from the call
   on, started at once. It is a `Unit` like any: the same reducer, the same
   jobs, `Restart=` included. How many instances, `MaxConnections=`, the
   template's choice and the instance's name are the agent's (A4).
2. **The connection is the main process's.** Every `ExecStart=` spawn of the
   instance is handed `conn` through `listen.PrepareFiles` — after the
   sockets `template.Spec` activates, before the descriptor store, systemd's
   `service_collect_fds` order —: fd 3 when nothing else is activated,
   `LISTEN_FDS=1`, `LISTEN_FDNAMES=connection` (systemd's name for it), and
   `LISTEN_PID` set by the trampoline to the process's own PID (`Spec.PIDEnv`,
   ADR 0219). No other command receives it. The template is never written
   to: each spawn's spec is a copy.
3. **The instance ends with its run.** Once a start began (`flagRan`), the
   instance ends when it rests dead with no job running or queued and no
   restart due — the main process gone, a failed start over, a stop done —:
   the actor ends as `Close` ends it, `conn` is closed, every goroutine is
   joined, every subscription closed, every later call `UNIT_CLOSED`. A
   subscription closed now says the end is complete: `Subscribe` on an ended
   unit returns a closed channel only once everything it held is released.
4. **Failures.** A start that fails returns `START_FAILED` with the unit —
   ended, `conn` closed, its `State` saying why (the published events are the
   typed trail). A nil `conn` is `CONFIG_INVALID` (`directive` `Accept`), a
   configuration `NewUnit` refuses is refused the same, and Windows, where no
   descriptor is handed to a child, is `DIRECTIVE_UNSUPPORTED`: no unit, `conn`
   and `template.Notify` closed. A `template.Notify` is the instance's, as
   `NewUnit` takes it — each instance needs its own.
5. **Platforms.** Every Unix: `PrepareFiles` and `Spec.PIDEnv` are there
   (ADR 0219). Windows refuses.

A coverage row is added (`systemd.socket(5)`):

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `Accept=yes` | SDK S0f: `SpawnInstance`, the primitive; the instances' count, names and template are the agent's | yes | yes | refused | `TestInstanceEndsWithItsRun`, `TestInstancesEachTheirConnection`, `TestInstanceStartFailure`, `TestInstancesLeakNothing` (a thousand instances), `TestRealInstances` |

The codes `0.3.105.12`–`15`, reserved for S0d and S0f, stay reserved: both
refuse with the existing codes, and a store's refusals are counters of the
published `StateValue` — no caller waits on an `FDSTORE=1`.

## Amendment — 2026-11-08: the cgroup modes run on the unit's group ([#734](https://github.com/kitsunium/sdk/issues/734), S0g)

The body above is kept as it was decided; where it and this section disagree,
this section holds. Where ADR 0016's amendment of 2026-11-08 (#727) and this
section disagree on `proc/cgroup`, this section holds.

1. **A sibling port, `CgroupUnit`.** The cgroup modes need more of a group
   than `CgroupKill`: its directory, its state followed and read again, its
   members at any depth, its delegation, the groups made beneath it,
   `memory.oom.group`. They are one sibling of `CgroupKill` (ADR 0039),
   `CgroupUnit`, which the engine asserts on `Config.Cgroup`. The group
   `proc/cgroup`'s `Create` makes on Linux is a `CgroupKill`, a
   `CgroupMembers` and a `CgroupUnit` as it is — a compile-time assertion of
   `proc/cgroup`'s tests pins the three —: the "default `CgroupKill` over
   `proc/cgroup`" S0c anticipated is that type assertion, and it lists its
   members. Windows' Job Object and FreeBSD's rctl are no `CgroupKill`, so the
   modes are Linux's, on cgroup v2.
2. **Refusals lifted where a group runs them.** With a `CgroupUnit`,
   `KillMode=control-group|mixed`, `ExitType=cgroup`, `Delegate=` and every
   `OOMPolicy=` resolve. Without one — no `Cgroup`, a `CgroupKill`, a
   `CgroupMembers` — each stays `DIRECTIVE_UNSUPPORTED` naming the directive,
   its `issue` field naming the `CgroupUnit` that lifts it: §12's
   `0.3.105.2` now reads "a cgroup mode without a `CgroupUnit`".
   `TestCgroupDirectivesRefused` pins both, and S0e's refusals kept.
3. **`KillMode`'s zero.** §9 made the zero `process` because `control-group`
   needed S0g, and the alternative it refused — the zero as `control-group` —
   was refused because every zero `Config` would be refused until S0g and the
   zero would change meaning when S0g landed. S0g lands in the same
   unreleased series: nothing published changes meaning. A unit with a cgroup
   that kept `process` would leave every child its main process forked
   running after the stop — the group then cannot be removed, a restart runs
   beside the leftovers —, the pitfall systemd's default `control-group`
   exists against. The zero becomes `KillModeDefault`, the other constants
   moving up by one (`process` 1, `control-group` 2, `mixed` 3, `none` 4):
   `control-group` with a `CgroupUnit`, `process` without — as `TypeDefault`
   resolves by `ExecStart=` and `OOMPolicyDefault` by `Delegate=`. A zero
   `Config` still runs everywhere.
4. **Every process in the group.** With a `CgroupUnit`, an empty
   `Spec.CgroupPath` is the group's `Path` for every spawn (CLONE_INTO_CGROUP,
   ADR 0217), commands included, as systemd places them; a `CgroupPath` the
   template names must be the group or a group beneath it (what
   `DelegateSubgroup=` places), else `CONFIG_INVALID` naming `CgroupPath`.
5. **The stop.** Under `control-group` every signal step, under `mixed` the two
   kills (`stop-sigkill`, `final-sigkill`), reach the group —
   `unit_kill_context`'s cgroup kill. The shell signals the main and control
   processes through their handles only where they are outside the group,
   then the group through `Signal` — the subtree's `cgroup.procs` read again
   until no process is new, as `cg_kill_recursive` —, so each process gets
   each signal once (a second SIGTERM is a forced quit for many daemons). The
   step waits while the group holds a process (`wait_for_exit`): the group
   signal's outcome says whether it held one, and a step that found it empty,
   with no main or control process, goes on at once.
   `service_notify_cgroup_empty_event` is the reducer's `onCgroupEmpty`: the
   stop steps go on, a unit up on its group alone stops, a notify unit nobody
   is left to make ready fails with `protocol`, a PID file nobody is left to
   write is given up.
6. **No `cgroup.kill`.** The issue asked for the kill through `cgroup.kill`.
   Measured on Linux 6.12.101 in a delegated scope: after one `cgroup.kill` on
   a group — an empty one included —, every child later cloned into it
   (CLONE_INTO_CGROUP) is killed at its fork, while a process migrated in is
   not: the kill sequence the fork compares is not the target group's. A unit
   stopped through it could never start again in its group. The kill goes
   through `Signal(SIGKILL)`, whose passes catch a tree that forks no faster
   than 64 passes; `CgroupKill.Kill` stays in the frozen port, unused by the
   engine, for a caller that discards the group. `TestLiveStartsAgainAfterTheKill`
   pins it: with `Kill` its second start's main process dies at once.
7. **`ExitType=cgroup`.** The main process's clean end with members left keeps
   the unit up — `service_good` falling back on `cgroup_good` —:
   `flagCgroupGood`, which S0c set for a forking unit whose main process stayed
   unknown, now also stands for an `ExitType=cgroup` unit whose main process
   ended. A main process that failed stops the unit, as under `main`. The
   group's emptying ends both, S0c's forking unit up on its cgroup alone —
   which only a stop ended — included.
8. **The group read when the reducer decides.** A watch state can reach the
   actor after the end of the process it followed. With every process's end
   and every state the watch announces, the shell reads the group again —
   `Members`, `Stat` — and the reducer acts on that read, never on the
   watch's value: a late "empty" while a child runs changes nothing
   (`TestLateEmptyNeverEndsAUnit`); a group that cannot be read holds nothing
   to wait for. The watch is the wake-up: it starts before a start's first
   spawn — the group's preparation — and runs until `Close`, again after the
   group went. `proc/cgroup`'s watch now publishes after every change the
   kernel announces, a burst that left the state as it was included
   (populated 0, 1, 0 between two reads): a reader that read the group
   meanwhile learns it changed back.
9. **`OOMPolicy=`.** `memory.events`' `oom_kill` is read with each event; a
   count above the last one is an OOM kill, taken before the event — systemd
   dispatches its OOM queue before SIGCHLD —, so a main process the OOM killer
   ended is `oom-kill`, not `signal`. The count when the watch starts is the
   baseline: a kill counted before is no news. `service_notify_cgroup_oom_event`
   decides: `continue` keeps the unit; `stop` signals it while it starts and
   runs `ExecStop=` once it is up; `kill` sends the final kill at once; both
   end with `oom-kill`, §10's row. The zero is `stop`, `continue` under
   `Delegate=`. `kill` writes `memory.oom.group=1` at each start's
   preparation; the engine never writes it back to 0.
10. **`Delegate=`.** At each start's preparation the group is handed
    (`Delegate(uid, gid)`) to the user and the group `Spec` runs as — the
    user's primary group when `Spec.Group` is empty, nothing changed for the
    supervisor's own —; an unknown user is `UNKNOWN_USER`, and any refusal
    fails the start's first spawn (`resources`), as systemd fails a start
    whose cgroup it cannot realise. At each end of a run the groups the
    service made beneath it are removed (`DeleteBeneath`, deepest first, no
    link followed); one still held — a process `KillMode=process` left —
    stays, as systemd leaves a cgroup it cannot trim. The unit's group itself
    stays: it is the caller's and the next start spawns into it — systemd
    re-creates its own; ADR 0016's #727 amendment named `DeleteTree`, which
    would remove it. Which controllers the group offers its service is the
    caller's (`WithControllers`): `Delegate=` is a boolean here, systemd's
    controller list is not carried.
11. **`Members` over the subtree.** `proc/cgroup`'s Linux `GroupReader.Members`
    read the group's own `cgroup.procs`, while `Contains`, `Signal` and
    `populated` reach the subtree: a delegated service that moved into a group
    of its own made the unit's group look empty to the guess and to the
    population. `Members` lists the subtree; `CgroupMembers` says so.
12. **A group's directory is not its cgroup path.** `AttachValue.Cgroup` names
    a cgroup as `/proc/<pid>/cgroup` does (`/system.slice/web.service`),
    `GroupTree.Path` its directory (`/sys/fs/cgroup/system.slice/web.service`);
    passed as is, the directory refuses every adoption. The engine builds no
    adoption scope — `NewAttacher`'s is the caller's —, so the conversion is
    an API of `proc/cgroup`, `Membership(dir)`: the path below the cgroup2
    mount that holds `dir`, under the part of the hierarchy that mount exposes,
    `INVALID_SPEC` where none holds it, `UNSUPPORTED_PLATFORM` off Linux.
    `NewAttacher`'s doc names it; `TestLiveMembershipBoundsAdoption` adopts
    through the converted path and is refused through the directory.
13. **Instances.** A `SpawnInstance` template's `Cgroup` is the instance's, as
    its `Notify` is: instances sharing a `CgroupUnit` would signal and watch
    one another.
14. **Codes.** None: every refusal is an existing code; `0.3.105.12`–`15` stay
    reserved.

The ports table gains a row:

| Port | Methods | Used by | Default |
|---|---|---|---|
| `CgroupUnit` | `Path()`, `Members()`, `Stat()`, `Watch(ctx)`, `Delegate(uid, gid)`, `DeleteBeneath()`, `SetMemoryOOMGroup(enabled)`, asserted on `Config.Cgroup` | the cgroup modes (S0g) | none: the modes refused; `proc/cgroup`'s Linux group is one |

The coverage rows, restated:

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `ExitType=cgroup` | SDK S0g, with a `CgroupUnit` | yes (cgroup v2) | refused | refused | `TestExitTypeCgroup`, `TestRealExitTypeCgroup`, `TestLiveExitTypeCgroup` |
| `OOMPolicy=` | SDK S0g: `memory.events`' `oom_kill` read with each event, `memory.oom.group` for `kill`; the zero `stop`, `continue` under `Delegate=` | yes (the memory controller) | refused | refused | `TestOOMPolicy`, `TestOOMKillFromTheWatch`, `TestLiveOOMPolicy` |
| `KillMode=process` | SDK, the zero without a `CgroupUnit` | yes | yes | yes | `TestKillModes`, `TestKillModeProcessAndNoneLeaveTheGroup` |
| `KillMode=control-group` | SDK S0g, the zero with a `CgroupUnit`; the group signalled through `Signal`, never `cgroup.kill` | yes | refused | refused | `TestKillModeControlGroup`, `TestRealKillModesWithAForkingChild`, `TestLiveKillModes`, `TestLiveStartsAgainAfterTheKill` |
| `KillMode=mixed` | SDK S0g, the kills to the group | yes | refused | refused | `TestKillModeMixed`, `TestRealKillModesWithAForkingChild`, `TestLiveKillModes` |
| `Delegate=` (`systemd.resource-control(5)`) | SDK S0g: the group handed to the service's user at each start, what it made beneath removed at each end; a boolean, the controllers the caller's; `DelegateSubgroup=` is a `Spec.CgroupPath` beneath the group | yes | refused | refused | `TestDelegateReplay`, `TestGroupUnitPreparesSignalsAndCleans`, `TestLiveDelegate` |
| `ManagedOOMSwap=`, `ManagedOOMMemoryPressure=` and the slices | agent: systemd-oomd's policy and the slices stay supervizio/agent#330's | — | — | — | agent |

What S0h carries over: `flagGroupPopulated`, `flagCgroupGood` and
`flagPrepare` are the model's plain data. The OOM baseline and the watch are
the shell's: the incoming image starts the watch again before it trusts a
count, its baseline the count at that start, so an OOM kill during the
hand-over itself is not seen — a stated gap, as systemd's own across a
re-exec.

## Amendment — 2026-11-08: the BPF rows after S6 ([#731](https://github.com/kitsunium/sdk/issues/731), ADR 0215)

S6 landed as [ADR 0215](0215-a-cgroups-bpf-programs-are-built-in-go-in-a-quarantined-vendor-module.md):
`proc/bpf` attaches a device policy (`cgroup_device`), an IP policy that
counts its traffic (`cgroup_skb`) and a bind policy (`cgroup_sock_addr`) to a
group's directory, its backend `third-party/ebpf/cgroup` linked by a blank
import, `NOT_LINKED` without it; Linux amd64 and arm64 load, every other cell
is `UNSUPPORTED_PLATFORM`. It is a primitive the engine does not call: no
field of `Config` or `Spec` carries a policy, and the shell attaches nothing
to a `CgroupUnit`. Wiring the directives into the engine is future work.
Until then the agent attaches them itself, through `pkg/v1/proc/bpf`, to the
group it gives the unit as `Config.Cgroup` (its `Path()`), before `Start`,
and detaches them with `Close` or by removing the group.

S6 builds none of the programs the `systemd.exec(5)` row above named it for:
`RestrictFileSystems=` is a BPF LSM program, and `PrivateBPF=` and the
`BPFDelegate…=` directives are a bpffs mounted with delegation for the unit,
not a cgroup program.

The rows, restated:

| Directives | Owner |
|---|---|
| `RestrictFileSystems=` | no until an issue asks: a BPF LSM program (a kernel with `bpf` among its LSMs), which S6 does not build |
| `PrivateBPF=`, `BPFDelegateCommands=`, `BPFDelegateMaps=`, `BPFDelegatePrograms=`, `BPFDelegateAttachments=` | no until an issue asks: a private bpffs with BPF token delegation, which S6 does not mount |
| `DeviceAllow=`, `DevicePolicy=` (`systemd.resource-control(5)`) | agent, through `proc/bpf`'s `AttachDevice` (S6) on the unit's group; not wired into the engine |
| `IPAddressAllow=`, `IPAddressDeny=`, `IPAccounting=` (`systemd.resource-control(5)`) | agent, through `proc/bpf`'s `AttachSKB` (S6) on the unit's group, its `Counters` the accounting; not wired into the engine |
| `SocketBindAllow=`, `SocketBindDeny=` (`systemd.resource-control(5)`) | agent, through `proc/bpf`'s `AttachBind` (S6) on the unit's group; not wired into the engine |

## Amendment — 2026-11-08: the sandbox rows after S2 ([#728](https://github.com/kitsunium/sdk/issues/728), ADR 0217)

S2 landed as [ADR 0217](0217-a-spawn-gives-the-child-only-what-it-names-and-waits-a-bounded-time.md)'s
amendment of 2026-11-08: `Spec.Sandbox` (`coreproc.SandboxValue`) — new
namespaces with their id maps, bind, read-only and tmpfs mounts, a new root
by `pivot_root`, the bounding, ambient and secure-bit sets, `no_new_privs`,
Landlock rules and a seccomp filter of named calls with one action and a
default — applied by `proc/exec`'s two-stage trampoline on Linux amd64 and
arm64, `UNSUPPORTED_PLATFORM` everywhere else. It is a primitive of the
spawn, not yet wired into the engine:

- the engine carries it as it is: `Config.Spec` is the template of every
  command (§`systemd.exec(5)`), so a `Spec.Sandbox` there confines every
  `Exec*=` line, the unit's group included — the child lands in the group
  it is spawned into, confined, and the group a kill by passes emptied
  takes the next spawn (`TestLiveSandboxedSpawnLandsInTheGroup`,
  `TestLiveSandboxedGroupReusedAfterSignalPasses` in `proc/cgroup`);
- no field of `Config` takes a directive of the rows below: turning
  `CapabilityBoundingSet=`, `PrivateNetwork=`, `ProtectSystem=` or
  `SystemCallFilter=` into a `SandboxValue` is the agent's until then;
- what the engine does not do yet for a sandboxed unit: a `+` command lifts
  the credentials alone and keeps the sandbox, where systemd applies neither
  `CapabilityBoundingSet=` nor the namespacing options to a `+` line;
  `RootDirectoryStartOnly=` has no field, a root reaching every command; the
  descriptor store's key (§ the store's amendment) holds the user and the
  groups, not the sandbox; and `MAINPID=`'s uid-0 exemption without a
  cgroup is unchanged. The amendment that follows wires them.

The rows, restated:

| Directives | Owner |
|---|---|
| `RootDirectoryStartOnly=` | SDK, to wire: no `Config` field; a `Spec.Sandbox.Root` reaches every command |
| `CapabilityBoundingSet=`, `AmbientCapabilities=`, `NoNewPrivileges=`, `SecureBits=` | agent, through `Spec.Sandbox`'s `Capabilities` and `NoNewPrivileges` (S2) in `Config.Spec`; not wired into the engine |
| `RootDirectory=`, `BindPaths=`, `BindReadOnlyPaths=`, `TemporaryFileSystem=`, `PrivateTmp=`, `PrivateMounts=`, `ProtectSystem=`, `ProtectHome=`, `ReadWritePaths=`, `ReadOnlyPaths=`, `InaccessiblePaths=` | agent, through `Spec.Sandbox`'s `Root` and `Mounts` (S2: bind, read-only, tmpfs, `pivot_root`); not wired into the engine. The images, verity, extensions and the other rows of the mount line stay as the line above says |
| `PrivateNetwork=`, `PrivateIPC=`, `PrivateUsers=`, `PrivatePIDs=`, `ProtectHostname=` | agent, through `Spec.Sandbox`'s `Namespaces`, `UIDMap` and `GIDMap` (S2); not wired into the engine |
| `NetworkNamespacePath=`, `IPCNamespacePath=`, `UserNamespacePath=`, `DelegateNamespaces=` | no until an issue asks: S2 makes new namespaces, owned by the child's own user namespace; it joins none and hands none over |
| `ProtectClock=`, `ProtectKernelTunables=`, `ProtectKernelModules=`, `ProtectKernelLogs=`, `ProtectControlGroups=` | agent, through `Spec.Sandbox`'s read-only `Mounts`, its `Seccomp` by call names and its bounding set (S2), a device policy where one is needed (`proc/bpf`, S6); not wired into the engine |
| `SystemCallFilter=`, `SystemCallErrorNumber=` | agent, through `Spec.Sandbox`'s `Seccomp` — named calls, one action, a default (S2); the `@group` names are the agent's policy (agent#331); not wired into the engine |
| `SystemCallArchitectures=` | S2's filter kills every ABI but the native one (x32 included on amd64): `native` always, nothing else |
| `RestrictNamespaces=`, `RestrictAddressFamilies=`, `MemoryDenyWriteExecute=`, `RestrictSUIDSGID=`, `LockPersonality=`, `RestrictRealtime=`, `SystemCallLog=` | no until an issue asks: each filters on a call's arguments — the flags of `clone` and `unshare`, a socket's family, a mapping's protection, a mode, a personality, a policy — or logs, which S2's filter does not |

## Amendment — 2026-11-08: Type=dbus is up on its bus name, through a client on the standard library ([#737](https://github.com/kitsunium/sdk/issues/737), S0e)

The body above is kept as it was decided; where it and this section disagree,
this section holds. The issue asked for a spike first, then the Linux
implementation; both are recorded here. No dependency is added, so no ADR of
its own (the issue asked for one only if a vendor came in).

### The spike

The question: how does the engine learn that `BusName=` is owned on the bus —
which dependency, which bus, without cgo, tested without a daemon.

| Option | What | Cost | Verdict |
|---|---|---|---|
| (a) | a minimal client in the SDK, on the standard library: AUTH EXTERNAL, `Hello`, `AddMatch` on `NameOwnerChanged`, `GetNameOwner`, `RequestName`, `ReleaseName` | 1 694 lines with their comments, 1 107 without (`internal/service/proc/dbus`); `go list -deps` on linux/amd64: 79 packages, 75 of the standard library and the SDK's `errs`, `core/proc`, `core/proc/dbus` and itself; no `os/exec`, no `os/user`, no reflection in the client | **retained** |
| (b) | `github.com/godbus/dbus/v5` (v5.2.4) in a quarantined vendor module (ADR 0157) | 8 058 lines of non-test code; its `go.mod` requires `golang.org/x/sys`; `go list -deps`: 81 packages; a marshaller over `reflect`, and `exec.Command` to autolaunch a session bus (`dbus-launch`, `launchctl`) — a process the supervisor never asked to start; a new module, a new ADR, a vendor's release cadence | refused |
| (c) | the port alone, no implementation | none here; the agent (A1) would bring (b) or write (a) itself, and the issue's acceptance — a test against a real `dbus-daemon` — would move with it | refused: it hands the question on |

What a supervisor needs of the bus is five calls of the bus driver and one
signal; what the SDK needs of a vendor module is a reason the standard library
cannot be. (a) is what ADR 0156 asks. The bus is the caller's: `NewConn` takes
an address — `SystemBusAddress()` (`$DBUS_SYSTEM_BUS_ADDRESS`, else
`/run/dbus/system_bus_socket`), `SessionBusAddress()`
(`$DBUS_SESSION_BUS_ADDRESS`, else `$XDG_RUNTIME_DIR/bus`) or any — and never
launches one. Tested without a daemon: the client over a `net.Pipe` whose
other end replays dbus-daemon's part byte for byte; with one: a
`dbus-daemon` the test launches, its socket and configuration in the test's
directory, skipped where none is installed.

### The decision

1. **A port, `BusWatcher`.** `WatchName(ctx, name) (<-chan string, error)`:
   the owner's unique name when the watch began — looked up once the bus
   announces the name's changes to the watcher —, then the owner after each
   change the bus announces, in order, the channel closed when `ctx` ends or
   the connection is lost. Frozen at its one method (ADR 0039), a new field of
   `Config` (`BusWatcher`), as `Attacher` is. `proc/dbus`'s `*Conn` is one as
   it is; a test hands a double.
2. **`proc/dbus`, a new proc domain.** Core `internal/core/proc/dbus` (the
   codes `0.3.108.*`), engine `internal/service/proc/dbus`, facade
   `pkg/v1/proc/dbus`, design `design/proc/dbus.yaml`. Unix sockets only
   (`unix:path=`, `unix:abstract=`), AUTH EXTERNAL only, naming no identity
   — the empty `DATA` sd-bus sends —: a client in a user namespace, a
   sandboxed service's (S2), is the user the kernel names as the socket's
   peer, where its own uid there would be `REJECTED`. A header's field
   array past 64 KiB, a byte order that is none, a string that is not UTF-8:
   `PROTOCOL_VIOLATION`, the connection closed; a body past 1 MiB read and
   dropped, never allocated; a reply or a `NameOwnerChanged` from anyone but
   the bus driver dropped. `GetNameOwner` follows `AddMatch` and its reply is
   taken by the reader before the next message: an announcement read before it
   is older and dropped, none after it lost or reordered; a watcher that falls
   64 changes behind has its newest coalesced into the last, its oldest kept.
   Every call, and the authentication, waits 25 seconds at most — sd-bus's
   and libdbus's default reply timeout — or its context's deadline when
   sooner (`CALL_TIMEOUT`): the engine waits for `WatchName` on the actor at
   a start's preparation, so a bus that never answers costs that start at
   most fifty seconds and a `resources` failure, never the unit.
   Linux only: the BSDs' daemons want the credentials byte with `SCM_CREDS`,
   macOS's bus is launchd's, Windows' a TCP address with a nonce file;
   `NewConn` is `UNSUPPORTED_PLATFORM` there.
3. **The watch.** It begins at a start's first spawn, before it — the
   preparation S0g's group has (`flagPrepare`) — and runs until `Close`, again
   at the next start after the connection went. Its first answer is the
   level, not readiness (`flagLookup` on that spawn's outcome): systemd waits
   in `start` for a change, so a name some stale process owned before the main
   process was spawned never makes the unit ready — its start ends by
   `TimeoutStartSec=`, as systemd's would. A watch the `BusWatcher` refuses
   fails that spawn (`resources`), as systemd fails a unit whose name it
   cannot watch. The watch's end — the connection lost — changes nothing: the
   last owner stands, as systemd keeps `bus_name_good` while disconnected.
4. **Ready.** In `start`, with the main process running, an owner announced
   is `service_enter_start_post`. `service_good` gains the name: a Type=dbus
   unit is up only while its name is owned and something runs.
5. **Lost** (systemd 262, `service_bus_name_owner_change` and
   `service_enter_running`). Running, a loss the bus announces —
   `NameOwnerChanged` is definitive — is `service_enter_exited_or_stop`:
   `ExecStop=`, then the signals, with the result `success` (so
   `Restart=always|on-success` restart it, Table 1's success row), or
   `exited` under `RemainAfterExit=`. A unit that comes up again without its
   name while something runs — the name lost while `ExecStartPost=` or a
   reload ran, or a lookup that found none (a watch begun while it runs:
   S0h's) — is `running-revalidating`, a new sub-state at the end of `Sub`
   (`SubRunningRevalidating`, active): two seconds
   (`SERVICE_BUS_NAME_GRACE_USEC`) to own the name again, `RuntimeMaxSec=`'s
   clock stopped meanwhile when it was running; the name back is `running`,
   the grace over or a loss announced is exited-or-stop. It is up for
   everything else: a stop runs `ExecStop=`, a reload is taken, the watchdog
   runs, `MAINPID=` is read, an OOM kill and the group's emptying act as in
   `running`. The main process gone before the name is `service_enter_running`
   — a clean end stops the unit, an unclean one fails it.
6. **Settings.** `Type=dbus` needs Linux (else `DIRECTIVE_UNSUPPORTED`
   naming `Type`, its `issue` the platform), a `BusName=` (else
   `CONFIG_INVALID`, systemd's own refusal) and a `BusWatcher` (else
   `CONFIG_INVALID` naming it). A `BusName=` that is not a well-known name is
   `CONFIG_INVALID`. A unit with a `BusName=` and no `Type=` is `Type=dbus`,
   as `service_add_extras` reads it — `TypeDefault`'s resolution gains that
   first rule. `SpawnInstance` refuses a Type=dbus template
   (`CONFIG_INVALID` naming `Accept`): its instances would share one name,
   which systemd refuses to a second unit.
7. **Stated divergences.** (i) On a type other than dbus `BusName=` is
   accepted and not watched: systemd also adopts the owner's PID as the main
   process when it knows none (`pick_up_pid_from_bus_name`) — a forking unit
   without a PID file, a unit up on its cgroup —, which the engine does not;
   those find their main process by `PIDFile=`, the guess or the group. (ii)
   The dependency on `dbus.socket` and the refusal of two units on one name
   are the agent's graph. (iii) The SDK's client does not reconnect: a lost
   connection ends its watches and the units keep their last owner; systemd's
   manager reconnects and looks every name up again — the grace of (5) exists
   for that lookup, and a caller's `BusWatcher` that reconnects ends its
   channel so the next start watches again. (iv) The watch begins at the
   first start, not when the unit is loaded.
8. **S0h.** `flagBusOwned` and `revalidateBegin` are the model's plain data;
   the watch is the shell's. The incoming image watches again and hands its
   first answer as a lookup (`evBusName` with `flagLookup`), which a running
   unit reads with the grace, never as a loss.
9. **Codes.** None in `0.3.105.*`: every refusal is an existing code, and
   `0.3.105.12`–`15` stay reserved. §12's `0.3.105.2` now reads "`Type=dbus`
   off Linux". `proc/dbus` has `0.3.108.*`: `ADDRESS_INVALID`,
   `BUS_UNREACHABLE`, `AUTH_REFUSED`, `PROTOCOL_VIOLATION`, `CALL_FAILED`,
   `CALL_CANCELLED`, `CONN_CLOSED`, `NAME_INVALID`, `CALL_TIMEOUT`.

The ports table gains a row:

| Port | Methods | Used by | Default |
|---|---|---|---|
| `BusWatcher` | `WatchName(ctx, name) (<-chan string, error)`, the field `Config.BusWatcher` | `Type=dbus` (S0e) | none: `Type=dbus` is `CONFIG_INVALID`; `proc/dbus`'s `*Conn` is one |

The coverage rows of `systemd.service(5)`, restated:

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `Type=dbus` | SDK S0e: up once the main process runs and the name is owned, stopped when its loss is announced, two seconds of grace otherwise | yes | refused | refused | `TestDbusReadyOnOwner`, `TestDbusOwnedBeforeTheSpawnIsNoReadiness`, `TestDbusStartTimeout`, `TestDbusNameLostStops`, `TestDbusRevalidation`, `TestBusWatcherReadyAndLost`, `TestRealTypeDbus`, `TestLiveTypeDbusInItsGroup`, `TestLiveTypeDbusSandboxed` |
| `BusName=` | SDK S0e: `Type=dbus`'s name, the type when none is set; accepted and not watched on another type | yes | refused with `Type=dbus`, else accepted | idem | `TestDbusSettings`, `TestBusNames` |

## Amendment — 2026-11-08: the sandbox wired into the engine ([#728](https://github.com/kitsunium/sdk/issues/728), [#733](https://github.com/kitsunium/sdk/issues/733))

The body above is kept as it was decided; where it, or the amendment "the
sandbox rows after S2", and this section disagree, this section holds.

1. **The directives are `Config` fields.** `ProtectSystem=`, `PrivateTmp=`,
   `ReadOnlyPaths=`, `BindPaths=`, `BindReadOnlyPaths=`,
   `TemporaryFileSystem=`, `RootDirectory=`, `RootDirectoryStartOnly=`,
   `PrivateUsers=`, `PrivateNetwork=`, `PrivatePIDs=`, `ProtectHostname=`,
   `PrivateIPC=`, `NoNewPrivileges=`, `CapabilityBoundingSet=`,
   `AmbientCapabilities=`, `SecureBits=`, `SystemCallFilter=`,
   `SystemCallErrorNumber=` and `SystemCallArchitectures=` are typed
   fields — `coresvc.ProtectSystem`, `coresvc.PrivateTmp`,
   `coresvc.BindPathValue`, `coresvc.TemporaryFileSystemValue`,
   `coresvc.SystemCallFilterValue`, a `*uint64` bounding set (nil: as
   inherited), masks and booleans —, each zero systemd's default. `NewUnit`
   resolves them (`resolveSandbox`) into one `coreproc.SandboxValue` added to
   `Config.Spec.Sandbox`, which stays the base: its namespaces, its mounts
   first, its Landlock — no directive carries Landlock —, copied, so a later
   change to the caller's value reaches no unit. A template's root,
   capability state or filter beside the directive that would set it again
   is `CONFIG_INVALID`.
2. **The mounts.** Sorted by target, component by component — a tree before
   what lies beneath it, as systemd sorts its mount list —, the order of
   the directives above among equal targets. `ProtectSystem=yes` makes
   `/usr` read-only, and `/boot` and `/efi` where they exist; `full` adds
   `/etc`. `PrivateTmp=disconnected` mounts on `/tmp` and `/var/tmp` a new
   tmpfs each, of mode 1777 — S2's mount kinds gain `MountTmpfsSticky` for
   it (ADR 0217's amendment of the same day), a `User=` other than the one
   that mounts it having to write there. With a root, the paths of
   `ProtectSystem=`, `PrivateTmp=`, `TemporaryFileSystem=` and
   `BindPaths='s targets lie beneath it; `ReadOnlyPaths=` and
   `BindPaths='s sources are the host's, as systemd reads them.
3. **A user namespace implied.** A supervisor that is not root reaches a
   namespace through its own user namespace: a directive that asks for a
   namespace — a mount included — adds `NamespaceUser`, as systemd's
   per-user manager implies `PrivateUsers=`. A root supervisor gets none it
   did not ask for.
4. **`+` and `!`.** A `+` command runs with no sandbox at all — no
   namespace, mount, root, capability set, `no_new_privs`, Landlock or
   filter —, systemd applying none of them to such a line; a `!` command
   keeps the sandbox and drops `User=`, `Group=` and `SupplementaryGroups=`
   alone.
5. **`RootDirectoryStartOnly=`.** `true` gives the root to `ExecStart=`
   alone (systemd's `EXEC_APPLY_CHROOT`); every other command keeps the
   rest of the sandbox, in the host's view — its mounts not beneath the
   root. Without a root it changes nothing.
6. **The store's key.** The digest of the store's amendment §6 now holds a
   deterministic spelling of `ExecStart='s sandbox — every field, a part
   absent told from an empty one —: a store filled outside the sandbox is
   never handed in.
7. **`MAINPID=`.** Without a cgroup, a sender of uid 0 named the main
   process. A sandboxed unit's uid 0 is no privilege — root of its own user
   namespace, or confined —: its `MAINPID=` is refused unless the cgroup
   says the PID is the unit's (#733's "no uid 0 exemption for a sandboxed
   unit").
8. **A sandbox that fails.** A spawn whose sandbox failed ends the run with
   an explicit result: `exit-code` for `SANDBOX_FAILED` — a step failed in
   the child before its program ran, what systemd's `EXIT_*` statuses
   (226/NAMESPACE, 228/SECCOMP…) are —, `resources` for
   `SANDBOX_UNAVAILABLE` — refused before the fork, as `service_spawn`'s
   failures are. The start's answer is `START_FAILED` outside — its code,
   its reason, `errors.Is(err, StartFailed)`, as every other failed start —
   and the sandbox's failure inside: `errs.Wrap`'s origin is the first
   `*Error` its cause holds, so the cause is `START_FAILED` joined
   (`errors.Join`) to the sandbox's failure, which `errors.Is`,
   `errors.AsType` and `errs.HasCode` reach through the join —
   `errs.HasCode` reads `0.3.105.4` and `0.2.6.30` or `0.2.6.36` —, the
   `step` or `feature` field kept with `unit` and `result`. The model keeps
   the code and the fields, plain data. Any other spawn failure is
   `resources`, as before.
9. **Refusals at `NewUnit`.** Off Linux amd64 and arm64 every sandbox — a
   directive's, or a template's — is `DIRECTIVE_UNSUPPORTED` naming the
   first directive (`Sandbox` for the template's), never a start that fails
   later. `DIRECTIVE_UNSUPPORTED`: `ProtectSystem=strict`, `PrivateTmp=yes`,
   a `SystemCallArchitectures=` other than `native`, `native` without a
   filter, a notification socket a tmpfs would hide or a root would move
   (S2 makes no mount point to bind it on; `ListenNotify` puts it under
   `TMPDIR`). `CONFIG_INVALID`: a value past its last constant, a path not
   absolute and clean, an `@group` or an empty call name, an errno past
   4095, `PrivatePIDs=` with `Type=forking` (systemd refuses it), an
   ambient set outside the bounding set, `FDsInherited` or supplementary
   groups in a user namespace beside a sandbox, a bare command name in a
   root.
10. **Stated divergences.** `PrivateTmp=disconnected` is a tmpfs per
    command, as systemd's is; `yes`'s host-backed directories shared by the
    unit are not carried. `PrivatePIDs=` makes each command PID 1 of its
    namespace and does not mount `/proc` again: the child sees the host's;
    a PID 1 receives from the supervisor only the signals it handles, so a
    stop past `TimeoutStopSec=` ends with `SIGKILL`. `ProtectHostname=` is
    systemd's `private`: the hostname may change inside the namespace;
    filtering `sethostname` and `setdomainname` is `SystemCallFilter='s.
    `PrivateUsers=` maps the namespace's root to the supervisor's
    effective ids, one wide, unless `Spec.Sandbox` names the maps; systemd's
    `self` mapping of the unit's own user, `identity`, `full` and `managed`
    are the template's. A bind whose source lies beneath a read-only tree
    is read-only (a bind copies its source's mount flags), where systemd
    binds before its read-only pass. S2 makes no mount point: a target must
    exist, and a bind or a `ReadOnlyPaths=` beneath a `TemporaryFileSystem=`
    cannot be made; `BindPaths='s `-`, `norbind`, a tmpfs's options other
    than `ro`, a path's `-` and `+` prefixes are the agent's to resolve
    before `NewUnit`. `SystemCallFilter=` names are taken as given: an
    allow-list's implicit `@default`, as every `@group`, and a per-call
    errno are the agent's (agent#331); `SystemCallErrorNumber=log` is no.
    `SystemCallErrorNumber=` without a filter changes nothing, as in
    systemd.
11. **Codes.** None: `CONFIG_INVALID`, `DIRECTIVE_UNSUPPORTED` and
    `START_FAILED` wrapping proc's `0.2.6.30` and `0.2.6.36`;
    `0.3.105.12`–`15` stay reserved.

What S0h carries over: the settings — the sandboxes among them — are
resolved again from `Config` by the incoming image; the model's
`sandboxCause` and `sandboxFields` are plain data.

The rows, restated (`systemd.service(5)`):

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `RootDirectoryStartOnly=` | SDK: the root `ExecStart='s alone | amd64, arm64 | refused | refused | `TestRootDirectoryStartOnly` |
| prefixes `+`, `!` with a sandbox | SDK: `+` unconfined, `!` the sandbox kept, the credentials dropped | amd64, arm64 | refused | refused | `TestPrivilegedPrefixesAndTheSandbox`, `TestSandboxedCommandsOverTheShell`, `TestRealSandboxFromDirectives` |
| `MAINPID=` of a sandboxed unit | SDK: no uid-0 exemption without a cgroup | yes | — | — | `TestMainPIDRootExemptionLostWhenSandboxed` |
| `FileDescriptorStoreMax=` | SDK S0d, its key holding the sandbox | yes | refused | refused | `TestStoreKeyHoldsTheSandbox` |

The rows, restated (`systemd.exec(5)`), "L" Linux amd64 and arm64 — every
other GOARCH and OS refused:

| Directive | Owner | L | B | W | Test |
|---|---|---|---|---|---|
| `ProtectSystem=` | SDK: `yes` (`/usr`, `/boot`, `/efi`), `full` (and `/etc`); `strict` no until an issue asks — a read-only root whose `/dev`, `/proc` and `/sys` stay writable is a recursive read-only with exceptions, which S2's mounts do not make | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives` |
| `PrivateTmp=` | SDK: `disconnected` (a sticky tmpfs on `/tmp` and `/var/tmp`); `yes` no until an issue asks | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives`, `TestLiveSandboxedUnitInItsGroup`, `TestSandboxMounts` |
| `ReadOnlyPaths=` | SDK | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives` |
| `BindPaths=`, `BindReadOnlyPaths=` | SDK: recursive binds onto existing targets | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFailureFailsTheStart` |
| `TemporaryFileSystem=` | SDK: mode 0755, `ro` | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives` |
| `RootDirectory=` | SDK: `pivot_root(2)`; refused beside a notification socket | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRootDirectoryStartOnly`, `TestSandboxDirectivesRefused` |
| `PrivateUsers=` | SDK: S2's one-line maps, or the template's; implied for an unprivileged supervisor | yes | refused | refused | `TestSandboxImpliesAUserNamespace` |
| `PrivateNetwork=`, `PrivateIPC=` | SDK | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives` |
| `PrivatePIDs=` | SDK: `/proc` not mounted again; refused with `Type=forking` | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestSandboxDirectivesRefused` |
| `ProtectHostname=` | SDK: systemd's `private` | yes | refused | refused | `TestSandboxDirectivesResolve` |
| `NoNewPrivileges=` | SDK | yes | refused | refused | `TestRealSandboxFromDirectives`, `TestLiveSandboxedUnitInItsGroup` |
| `CapabilityBoundingSet=`, `AmbientCapabilities=`, `SecureBits=` | SDK: masks, bit n capability n; nil bounding set as inherited | yes | refused | refused | `TestSandboxDirectivesResolve` |
| `SystemCallFilter=`, `SystemCallErrorNumber=` | SDK: an allow-list, or `~` a deny-list; kill, or the errno; the `@group` names the agent's (agent#331) | yes | refused | refused | `TestSandboxDirectivesResolve`, `TestRealSandboxFromDirectives`, `TestLiveSandboxedUnitInItsGroup` |
| `SystemCallArchitectures=` | SDK: `native`, beside a filter; any other ABI no until an issue asks | yes | refused | refused | `TestSandboxDirectivesRefused` |

Every other row of the amendment "the sandbox rows after S2" stands:
`PrivateMounts=`, `ProtectHome=`, `ReadWritePaths=`, `InaccessiblePaths=`
and the `Protect…=` rows of the kernel's trees stay the agent's through
`Spec.Sandbox`; joining a namespace and the argument filters stay no.

## Amendment — 2026-11-08: the supervisor's hand-over, S0h ([#738](https://github.com/kitsunium/sdk/issues/738))

S0h implements §11 on Linux amd64 and arm64: `Freeze` ends units without
stopping a process and writes each model down, `Reexec` executes the program
again with it, `TakeHandover` reads it in the incoming image and `Resume`
makes each unit from it. A `HandoverState` — `Handover` in the facade — holds
what no `Resume` took, and `Close` stops it in order. §11 stands but for the
following.

1. **The re-exec is `syscall.Exec` of `/proc/self/fd/N`, not a raw
   `execveat`.** §11 named `execveat`; `syscall.Exec` runs `execve` under the
   runtime's exec lock, so no thread is created during it, which a raw
   `execveat` from one of the runtime's threads would not get, and the file
   stays pinned by the `O_PATH` descriptor `/proc/self/fd/N` names — what
   `execveat(N, "", AT_EMPTY_PATH)` runs. The close-on-exec dance is §11's:
   every descriptor sealed (`sealAll`), the flag lifted under `ForkLock` on the
   payload's memfd and the list alone, and on a refusal set back before any
   fork.
2. **`OpenImage` is the caller's, at its start** (`OpenImage` is `O_PATH` and
   close-on-exec): the descriptor keeps naming the program whatever later
   replaces its path.
3. **The payload** is sealed (a magic, the version, the length, then SHA-256 of
   the body) in a memfd sealed whole; `TakeHandover` reads the memfd, not a
   file anyone can edit, and checks each descriptor it names by `fstat` — open,
   not close-on-exec, the same type, device and inode. A record an image does
   not know is skipped; a later field needs no new version.
4. **One version exists, so nothing migrates.** `handoverVersion` is 1. A
   version above it or below the first (0) is `HANDOVER_INVALID`, read the same
   way from any image (`TestRealReexecHandoverRefused/version`, `/older`); there
   is no reader of a previous version until a second one ships, and `Resume`'s
   tolerance of a field a later image added is the only compatibility promised.
5. **A refused hand-over is stopped, not adopted.** §11 allowed adoption by
   cgroup or PID file (S3) "or" an ordered stop; the ordered stop is implemented
   and adoption is not. `TakeHandover` closes every descriptor the exec handed
   over, stops every child of the process in order (SIGTERM, the grace, SIGKILL),
   reaps it and returns `HANDOVER_INVALID` or `HANDOVER_MISMATCH` with one `pid`
   field per process stopped. Issue #738 asks for "refusal + ordered stop + an
   event": at that point the incoming image has no unit, so there is nothing an
   event could be published on — the typed error naming each pid stopped is the
   event of an image without units, and a caller logs it. No API was added for
   this.
6. **The platforms.** The re-exec is Linux amd64's and arm64's; `Freeze` and
   `Resume` are every Linux's, a hand-over within one image needing no exec.
   The BSDs and macOS are decided: `UNSUPPORTED_PLATFORM`, the fallback §11
   described (re-exec by path, `wait4`) is not implemented — `Freeze` and
   `Resume` refuse, `OpenImage` and `Reexec` likewise, and `TakeHandover`
   answers nil. Windows refuses too, as §11 said.
7. **`selfupdate` is unconcerned.** Its update replaces the executable file and
   leaves the running process alone: `framework/internal/service/selfupdate`
   renames over the path and probes the new binary with `exec.CommandContext`,
   and nothing in it re-executes itself, so `Reexec` is not its path (§11's last
   sentence, ADR 0077).
8. **What crosses.** The model — `Restart=` waiting (the timer re-armed for its
   deadline, `TestHandoverDuringTheRestartDelay`), the start limiter's count
   (`TestHandoverKeepsTheStartLimit`), the store (`FileDescriptorStorePreserve=`
   deciding, `TestHandoverInProcess`) —, a child held again through `proc/exec`'s
   `Reclaim`, an adopted process through its witness, the notification socket
   through `systemd/notify`'s `Detach`. `TestRealReexecHandover` crosses a real
   `execve` and scans the incoming image's descriptors (`/proc/self/fd`, the
   list alone inheritable); `TestRealReexecHandoverRefused` covers a later version,
   an older one, a corrupted payload and a descriptor that is no longer the one
   recorded, each stopped in order with no descriptor left; `TestHandoverInProcess`
   counts goroutines and descriptors.

The test harness itself is bounded since the incident of 2026-11-08 — every
helper of this package's tests passes a guard (an unknown role, a role set
twice, a depth past 2, a supervisor under a helper, more than 64 runs, 120 s of
life) and restarts are capped: `internal/service/proc/service/CLAUDE.md`
§Tests, and ADR 0217's amendment for the defect in `Spec.Env` that made it a
bomb.
