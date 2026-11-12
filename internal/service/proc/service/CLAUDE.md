<!-- updated: 2026-11-08T15:00:00Z -->
# internal/service/proc/service/

## Purpose

The engine of a service unit's life cycle (ADR 0214, kitsunium/sdk#732 S0a,
#733 S0b, and the series up to #734 S0g, the cgroup modes, #737 S0e,
`Type=dbus`, and #728 S2's sandbox wired in):
`systemd.service(5)`'s state machine for ONE service, as the supervizio/agent
roadmap (#326, decision D2) moved it into the SDK. Contract:
`internal/core/proc/service`. Facade: `pkg/v1/proc/service`. It imports
nothing of `app` (D4): the start limiter is its own, the restart curve the
kernel's `backoff`, the clock the kernel's.

## Shape — why

- **One actor, a pure reducer, an I/O shell** (ADR 0214 §3). `reduce(model,
  event) → (model, []effect)` in `reduce.go`, `enter.go`, `events.go`,
  `notify.go`, `restart.go` reads nothing but its arguments: the shell draws
  the randomness (`event.rand`) and reads CLOCK_MONOTONIC (`event.monotonic`)
  for it. `actor.go` is the only goroutine that touches the model, the
  processes and the timers; an effect whose outcome the reducer needs (a
  spawn, an adoption) hands it back as the next event, before the inbox.
- **The model is plain data** (`model.go`): no channel, no function, no
  handle, so S0h can serialise it. Keep it so.
- **Timers carry a sequence**: an arming's `seq` travels with its fire; a fire
  whose `seq` is not the model's current one is stale and ignored.
- **The enter* methods are service.c's `service_enter_*`**, the event handlers
  its `service_sigchld_event`, `service_dispatch_timer` and
  `service_notify_message`. Read systemd's code before changing a transition;
  every divergence is stated in ADR 0214's coverage table.
- **Observers never block the actor** (`observe.go`): the newest event is
  dropped on a full queue and counted, the generation shows the gap.
- **Stopping escalates on the unit's context** (`Unit.ctx`), never a caller's
  (ADR 0214 §7).

## Contents

| File | What |
|---|---|
| `config.go` | `Config`: one typed field per directive, the template `Spec`, the ports |
| `settings.go` | `resolve`: the checks and the defaults (`CONFIG_INVALID`, `DIRECTIVE_UNSUPPORTED`, `PRIVILEGE_REFUSED`); `storeKey`, the store's key, the sandbox among it |
| `dbus_resolve.go` | `resolveBus` (S0e): `Type=dbus` needs Linux, a `BusName=` and a `BusWatcher`; `defaultType` (dbus with a name, as `service_add_extras`); `busNameValid` |
| `sandbox.go` | `resolveSandbox` (#728 wired in): the confinement directives into one `SandboxValue` added to `Spec.Sandbox` (`buildSandbox`, `directiveMounts` sorted by path, `directiveSeccomp`, `directiveCapabilities`), their refusals (`checkSandbox`, `checkNotifySocket`, `checkSandboxed`), a user namespace implied for an unprivileged supervisor (`supervisorEUID`), off Linux amd64/arm64 refused (`sandboxPlatform`); `settings.sandboxFor` (RootDirectoryStartOnly=); `sandboxKey`, the sandbox's spelling for the store's key |
| `cgroupmodes_resolve.go` | `resolveCgroup` (S0g): `KillMode`'s zero (control-group with a `CgroupUnit`, process without), `OOMPolicy`'s (stop, continue under `Delegate=`), the refusals without a `CgroupUnit` (`withoutUnit`), the group every spawn goes into (`resolveCgroupPath`) |
| `model.go` | the model, the events, the effects |
| `reduce.go` | the reducer's dispatch, the jobs, the snapshot published |
| `enter.go` | the transitions (`enter*`), the timers, the watchdog, the variables a command gets |
| `events.go` | a process's end: the main process's, a command's (`mainExits`, `controlDone`) |
| `spawned.go` | a spawn's outcome and its failure paths (`spawnFailures`, each with its result: `spawnResult` — `exit-code` for `SANDBOX_FAILED`, `resources` else), the sandbox's failure kept for the start's answer (`noteSandboxFailure`), an adoption's dispatch |
| `forking.go` | `Type=forking` (S0c): the command's end, the PID file's reads and retries (`timerPIDFile`), the guess's outcome, `flagCgroupGood` |
| `guess.go`, `guess_*.go` | `soleRoot`, the shell's `GuessMainPID=` guess; a parent read from `/proc` (Linux), none elsewhere |
| `cgroupmodes.go` | the cgroup modes' decisions (S0g): `groupSignals` (control-group's steps, mixed's kills), `signalGroup` and its outcome (`onSignaled`), `onCgroup`, `groupNews` (an OOM kill before the event, then the population), `onOOM` (`service_notify_cgroup_oom_event`), `onCgroupEmpty` (`service_notify_cgroup_empty_event`) |
| `dbus.go` | `Type=dbus`'s decisions (S0e): `onBusName` (`service_bus_name_owner_change`: an owner in `start` is ready, an announced loss while up is `exitedOrStop`, a lookup leaves the grace), `good`/`goodExceptBus` (`service_good`), `enterRevalidating` (`running-revalidating`, two seconds on `timerState`), `resumeRuntime` (`RuntimeMaxSec=` paused while it awaited its name) |
| `fdstore.go` | the store's decisions (S0d): `FDSTORE=1` kept, refused or deduplicated, `FDSTOREREMOVE=1`, a hangup, the hand-over (`handOver`, newest first), the key (`ownStore`), `FileDescriptorStorePreserve=` (`settleStore`) |
| `actor_store.go` | the store's shell: the descriptors by id (`held`), the listing, the hand-over through `listen.PrepareFiles` after the spec's own activation, the duplicates (`dupsOf`) |
| `fdstore_linux.go`, `fdstore_other.go` | the hangup watch (epoll, `EPOLLONESHOT`, a wake pipe), `sameFile` (fstat and the access mode), `fileID`; stubs elsewhere, where the store is refused |
| `timeout.go` | a timer's fire: the step's timeout (`timeouts`), the failure modes |
| `notify.go` | sd_notify: `NotifyAccess=`, `MAINPID=` (no uid-0 exemption for a sandboxed unit), the states, the metadata, the watchdog, `EXTEND_TIMEOUT_USEC=` |
| `restart.go` | Table 1 (`shallRestart`, `restartTable`), the restart delay on `backoff.Grow`, the exit classification |
| `actor.go` | the shell: the loop, the effects, the processes' waits, the PID file's read and the guess (`attachFile`, `guessMain`, `members`), the end |
| `actor_dbus.go` | the bus name's shell (S0e): `prepareStart` (the group, then the watch, before a start's first spawn), `startBusWatch` (`BusWatcher.WatchName`, its first answer the level as `flagLookup` on the spawn's outcome), `forwardBus` (the watch's goroutine, draining once the actor ended) |
| `actor_cgroup.go` | the group's shell (S0g): `prepare` before a start's first spawn (the watch, `Delegate=`'s ids, `memory.oom.group`), `forwardGroup` (the watch's goroutine), `annotateGroup` (the group read again with each end and each change, an OOM kill turned into `flagOOM`), `signalGroup` (handles for what lies outside, `Signal` for the group, never `cgroup.kill`), `prune`, `delegateIDs` |
| `unit.go` | `Unit`, `NewUnit` and the API: `Start`, `Stop`, `Restart`, `Reload`, `Fail`, `ResetFailed`, `State`, `FDStore`, `Close` |
| `instance.go` | `SpawnInstance` (S0f): an instance per accepted connection, its refusals; the connection is `Unit.conn`, handed by `owed`, closed by `finish` |
| `observe.go` | `Subscribe` (a channel and its cancel), the publication |
| `spawn.go` | a command's spec: `$VAR`/`${VAR}`, the variables — `mergeEnv`, the template's names once each, a repeated one keeping its last value at its last place (ADR 0217's amendment of 2026-11-08), the engine's replacing every occurrence —, `+`/`!` (`+` with no sandbox, `!` keeping it), the sandbox by list, `\|` |
| `spawner.go` | the default `Spawner` (`proc/exec`), `NewAttacher` — the `Attacher` over `exec.Attach` / `exec.AttachPIDFile` (#729; a pidfd on Linux, kqueue knotes on darwin and the BSDs) —, `ListenNotify` over `proc/systemd/notify` (Linux and FreeBSD; the pidfd — a process descriptor on FreeBSD — first in `Files`, the sender's pidfd closed) |
| `handover.go`, `handover_codec.go` | S0h, every platform: `Freeze`, `Resume`, `HandoverState` and its `Close`, the payload's records, `NewHandoverState`; the model's codec — every field but the settings and the waiters, a record this image does not know skipped |
| `handover_linux.go`, `handover_other.go` | S0h's Linux shell (`freeze`, `restore`, `stopHanded`, the store's descriptors held again) and the refusals elsewhere |
| `reexec_linux.go`, `reexec_other.go`, `reexec_linux_{amd64,arm64}.go` | S0h's `OpenImage`, the sealed memfd, the seal of the table and the list lifted under `ForkLock`, `TakeHandover` (`acceptHandover`, `checkRecord`), the two test seams; `memfd_create`'s number |
| `signals*.go`, `monotonic_*.go` | per platform: SIGCONT and SIGHUP (none on Windows), CLOCK_MONOTONIC (Linux) |

## Extension points (the rest of the series)

- **S0c — done** (`Type=forking`, `PIDFile=`, `GuessMainPID=`; ADR 0214's
  amendment of 2026-11-08). The forking command is a control process given
  the main process's variables; its clean end emits `efAttachFile` (the shell
  calls `Attacher.AttachPIDFile`, so the engine reads no PID file) or
  `efGuess` (`CgroupMembers`, `soleRoot`, `Attacher.AttachPID`); the outcome
  comes back as `evAttached` with `flagByFile`/`flagByGuess`, a refusal's
  `reason`, and what the cgroup listed (`flagPopulated`/`flagEmpty`). A
  refused read re-arms `timerPIDFile` (100 ms); `setState` disarms it when
  the step changes. An adopted daemon's end is `flagUnknown`, clean.
- **S0d — done** (the store; ADR 0214's amendment of 2026-11-08). The model
  holds plain entries (`storeEntry`: id, name, poll) and the key they were
  filled under; the shell holds the descriptors by id (`actor.store`). The
  reducer emits `efStore`/`efStoreDrop`/`efClose`; every `ExecStart=` spawn
  carries its hand-over in `effect.entries`, which the shell turns into
  `listen.NamedFileValue`s after the spec's own activation. The shell
  annotates a `FDSTORE=1` message with `dupOf` and posts `evHangup` from the
  watch. `answersLast` puts a step's replies after its other effects, and
  `Close` joins the actor's end (`readers`).
- **S0e — done** (`Type=dbus`, `BusName=`; ADR 0214's amendment of
  2026-11-08, the spike's option (a): `proc/dbus`, a client on the standard
  library). `Config.BusWatcher` is the port. `beginStart` sets `flagPrepare`
  for a dbus unit too, so the start's first `efSpawn` makes the shell watch
  the name (`startBusWatch`) before it: the watch's first answer rides on
  that spawn's `evSpawned` as `flagLookup`/`flagOwner` — the level
  (`flagBusOwned`), never readiness. Every later answer is an `evBusName`
  (`flagOwner`, or `flagUnwatched` when the watch ended): in `SubStart` an
  owner is `enterStartPost`; up, an announced loss is `exitedOrStop`.
  `enterRunning` is `service_enter_running` with the bus: `good` needs the
  name; alive without it is `enterRevalidating` (`SubRunningRevalidating`,
  active, the grace on `timerState`, `timeouts` ending it with
  `exitedOrStop`). Every `SubRunning` test of the reducer counts the
  revalidation as running (`upSub`, `mainExits`, `watchdogRuns`,
  `mainPIDChanges`, `notifyStopping`, `onOOM`, `onCgroupEmpty`), as systemd
  262 does. `SpawnInstance` refuses a dbus template.
- **S0f — done** (instances; ADR 0214's amendment of 2026-11-08).
  `SpawnInstance` resolves the copy and `launch`es it — `NewUnit`'s start —
  with the connection: `setInstance` makes the reducer end the unit once it
  ran (`flagRan`) and rests dead with nothing queued (`instanceOver`); `owed`
  hands `connection` before the store to each `ExecStart=`; `finish` closes
  it, and `closeSubscriptions` — the last of the end — marks `ended`, which
  `Subscribe` reads. `NewUnit` keeps its own body: as a one-line wrapper it
  would inline into the facade's forwarder past kit's budget.
- **S0g — done** (the cgroup modes; ADR 0214's amendment of 2026-11-08).
  A `Config.Cgroup` that is a `CgroupUnit` sets `setGroupUnit`; `KillMode`'s
  zero resolves to control-group there. `beginStart` sets `flagPrepare`, so
  the start's first `efSpawn` carries `optPrepare` and the shell readies the
  group (`prepare`) before it — a refusal is that spawn's failure.
  `enterSignal` hands a group step to `signalGroup` (`efSignalGroup`, its
  outcome `evSignaled` carrying the step); `enterDead` emits `efPrune` under
  `Delegate=`. The shell turns the watch into `evCgroup` and reads the group
  again with every `evExit` and `evCgroup` (`annotateGroup`): the reducer
  takes `flagOOM` first, then the population (`flagGroupPopulated`), never
  the watch's own value. `flagCgroupGood` is the unit up on its group alone
  (S0c's unknown main process, `ExitType=cgroup`'s ended one); the group's
  emptying clears it. The group's kill is `Signal(SIGKILL)`, never
  `CgroupKill.Kill`: after a `cgroup.kill` Linux 6.12 kills every child
  cloned into the group at its fork.
- **S2 wired in — done** (ADR 0214's amendment of 2026-11-08, the sandbox
  wired into the engine). `resolve` builds `settings.sandbox` (and
  `sandboxAway` under `RootDirectoryStartOnly=`) once, from `Spec.Sandbox`
  and the directives; `buildSpec` hands `sandboxFor(list)` to every spawn but
  a `+` one. A sandbox's failure is `spawnResult`'s result and the model's
  `sandboxCause`/`sandboxFields`, which `startFailed` wraps. `setSandboxed`
  removes `MAINPID='s uid-0 exemption.
- **S0h — done** (the supervisor's hand-over; ADR 0214's amendment of
  2026-11-08, Linux amd64 and arm64, every other platform
  `UNSUPPORTED_PLATFORM`). `Freeze` posts `evFreeze`: the actor drains the
  events already posted, writes the model down (`encodeModel`, plain data,
  versioned by `handoverVersion` inside a payload sealed with its SHA-256),
  duplicates the store, an instance's connection and the notification socket
  into the hand-over, and forgets its processes without stopping them. The
  store crosses by id (`storeKey` judged by `ownStore` in the new image,
  `FileDescriptorStorePreserve=` deciding); `flagGroupPopulated`,
  `flagCgroupGood`, `flagPrepare`, `flagBusOwned` and `revalidateBegin` are
  the model's, the shell's watches started again, a bus lookup taken with the
  grace, never as a loss. `Reexec` is `syscall.Exec` of the `O_PATH`
  descriptor `OpenImage` opened at the supervisor's start (`/proc/self/fd/N`,
  not a raw `execveat`: the runtime's exec lock); every descriptor sealed,
  the list and the payload's memfd alone lifted under `ForkLock`, sealed back
  on a refusal. `TakeHandover` validates the memfd and each descriptor by
  `fstat`; a refusal stops every child in order and reaps it (`stopChildren`,
  the `pid` fields of the error are the only event: no unit exists yet). The
  timers come back from `deadlines` (`restore`), a child through
  `exec.Reclaim`, an adopted process through its witness. No migration: one
  version exists.

## Do NOT

- Mutate `settings.sandbox` or `sandboxAway`, or the template's
  `Spec.Sandbox`: every spawn shares them; `cloneSandbox` made them.
- Hand a `+` command a sandbox: systemd applies none of its options to it.

- Call a wall-clock wait of package `time` — `TestNoWallClockWait` fails.
- Read a clock, a random source or a process from the reducer.
- Block the actor on a subscriber, a caller, or an inbox send to itself.
- Import `app` (ADR 0214 D4).
- Call `os.File.Fd` on a stored descriptor: it turns the service's own open
  file blocking; lend it through `SyscallConn` (`control`).
- Put a descriptor in the model: the model holds ids, the shell the files.
- Call `CgroupKill.Kill` on a unit's group: after a `cgroup.kill`, Linux 6.12
  kills every child later cloned into the group, and the next start spawns
  into it (ADR 0214's S0g amendment, §6).
- Decide on the watch's own value: the group is read again with the event
  (`annotateGroup`), a late state never ends a unit early.
- Make a dbus unit ready on the bus watch's first answer: it is the level
  before the main process was spawned (a stale owner's name), and systemd
  waits in `start` for a change.
- Compare `m.sub` with `SubRunning` alone where systemd says `RUNNING` and
  `RUNNING_REVALIDATING`: `upSub` and the tables name both.
- Build a test helper's environment with `append(os.Environ(), ...)`: a child
  reads the first of two entries, and on 2026-11-08 a supervisor's child read
  the supervisor's role that way (`helperEnviron`,
  `helperguard_linux_external_test.go`).

## Tests

| File | What |
|---|---|
| `harness_internal_test.go` | the replay harness: the reducer driven by events, the shell's part played by hand |
| `restart_internal_test.go` | `TestRestartTable1Replay` (7 policies × 7 causes), `TestRestartTable`, `TestExitStatusSets`, `TestRestartDelayManualExample`, `TestRestartDelay`, `TestRestartRandomizedDelay`, `TestRestartMode`, `TestStartLimit` |
| `reduce_internal_test.go` | types, oneshot, the Exec* chain, `ExecCondition=`, timeouts and modes, runtime limit, watchdog, notify, `MAINPID=`, `NotifyAccess=`, reload, kill modes, escalation, jobs, determinism |
| `settings_internal_test.go` | refusals (`TestCgroupDirectivesRefused`, …), defaults, privileges, prefixes, expansion |
| `unit_external_test.go` | the shell on a ManualClock and a fake spawner; `TestUnitLeavesNoGoroutine` in a synctest bubble |
| `attacher_internal_test.go`, `attacher_linux_external_test.go` | `waitEvent` reads STATUS_UNKNOWN as an unknown end; `NewAttacher` over real processes and a PID file anyone may write (#729) |
| `forking_internal_test.go` | S0c's replay: the command as a control process, the PID file adopted, retried until the timeout, read after `ExecStartPost=`, `protocol` on an empty cgroup, the guess, the settings, the daemon's end against Table 1 |
| `forking_linux_external_test.go` | S0c over real daemons: a double fork and its PID file, an absent, linked or garbage file failing after `TimeoutStartSec=`, the guess among marked processes (`markCgroup`); every process carries the test's mark and only marked ones are killed |
| `forking_cgroup_linux_external_test.go` | S0c over S1 (#735 with #727): `TestLiveForkingGuessInARealCgroup` — the group `cgroup.Create` makes is the unit's `CgroupKill` and `CgroupMembers`, the command spawned into it (`Spec.CgroupPath`), the daemon guessed among its real members, the group's `Kill` ending the unit; it skips with no delegated cgroup v2 subtree (`KITSUNIUM_CGROUP_TEST_ROOT`, the mount as root, or `systemd-run --user --scope -p Delegate=yes`) |
| `fdstore_internal_test.go`, `fdstore_other_internal_test.go` | S0d's replay: names, the maximum, `FDSTOREREMOVE=1`, what is closed, the hand-over to `ExecStart=` alone, `FileDescriptorStorePreserve=`, a foreign key, a hangup, the settings; the refusal off Linux |
| `fdstore_linux_external_test.go` | S0d over a real socket: the test binary stores a file and sockets with `SCM_RIGHTS` and reads back what its restart is handed; `TestRealFDStoreLeaksNothing` compares `/proc/self/fd` after ten units with the settled table before them; a hangup removes a pipe, `FDPOLL=0` keeps one; the store units stop under `TimeoutStopSec=10s`, their helpers without the race detector's one-second sleep at exit (`GORACE=atexit_sleep_ms=0`), and `stopReported` fails a stop that needed the final kill, with the main process's signal lines of `/proc/<pid>/status` read 5 s into it; a store helper takes SIGTERM (`listenTerm`) before it notifies anything, so a stop that answers its `READY=1` at once is heard |
| `instance_internal_test.go`, `instance_external_test.go`, `instance_unix_external_test.go` | S0f: the end of an instance replayed; over the fake spawner what each instance is handed, its end, its refusals, a thousand instances in a synctest bubble (goroutines) and `/proc/self/fd` (descriptors, the settled table taken before the bubble, compared after it); three real instances writing to their fd 3 until the end of each stream |
| `unit_unix_external_test.go`, `notify_linux_external_test.go` | real processes over `proc/exec`; `ListenNotify` with the test binary as a notify service |
| `cgroupmodes_internal_test.go` | S0g's replay over `unitStub`: the settings (`TestCgroupSettings`), each kill mode (`TestKillModeControlGroup`, `TestKillModeMixed`, `TestKillModeProcessAndNoneLeaveTheGroup`), `TestExitTypeCgroup`, `TestForkingUpOnItsCgroupEndsWhenEmpty`, `TestCgroupEmptiedWhileStarting`, `TestOOMPolicy`, `TestDelegateReplay`; `TestCgroupDirectivesRefused` (settings) is the non-regression of S0b's refusals |
| `cgroupmodes_external_test.go` | S0g's shell over a fake group of the fake spawner's processes: the preparation, each process signalled once, never `cgroup.kill`, the cleanup (`TestGroupUnitPreparesSignalsAndCleans`), a late watch state read again (`TestLateEmptyNeverEndsAUnit`), an OOM kill from the watch past its baseline, the watch's goroutine joined in a synctest bubble |
| `cgroupmodes_linux_external_test.go` | S0g over real processes and `markUnit`, the engine's fake cgroup of marked processes: every `KillMode` with a forked child (`TestRealKillModesWithAForkingChild`), `TestRealExitTypeCgroup`, `TestRealForkingEndsWithItsCgroup` |
| `cgroupmodes_live_linux_external_test.go` | S0g in a real delegated cgroup v2 group (skips without one): `TestLiveKillModes`, `TestLiveExitTypeCgroup`, `TestLiveDelegate`, `TestLiveOOMPolicy` (memory.max read back, no swap, `ulimit -v` as a second bound), `TestLiveMembershipBoundsAdoption`, `TestLiveStartsAgainAfterTheKill` |
| `dbus_internal_test.go` | S0e's settings on every OS: `TestDbusSettings` (the refusals of S0b lifted on Linux; `DIRECTIVE_UNSUPPORTED` naming `Type` elsewhere; `BusName=` and `BusWatcher` required; the default type; another type's name not watched), `TestBusNames`, `TestDbusInstanceRefused` |
| `dbus_linux_internal_test.go` | S0e's replay (the harness plays the watch: `lookup`, `bus`): `TestDbusReadyOnOwner`, `TestDbusOwnedBeforeTheSpawnIsNoReadiness`, `TestDbusStartTimeout`, `TestDbusNameLostStops`, `TestDbusMainEndsBeforeItsName`, `TestDbusRevalidation`, `TestDbusLookupWhileRunning` (`RuntimeMaxSec=` paused), `TestDbusWatchEndKeepsTheLevel`, `TestDbusRestartWatchesOnce`, `TestDbusRevalidatingUnitStopsAndReloads` |
| `dbus_linux_external_test.go` | S0e's shell over a fake `BusWatcher`, each in a synctest bubble: ready and lost (`TestBusWatcherReadyAndLost`), a refused watch failing the start, an owner before the spawn timing out, a lost connection keeping the unit, one watch for a unit's life with every goroutine joined |
| `dbus_live_linux_external_test.go` | S0e against a real `dbus-daemon` the test launches under `t.TempDir()` (skips without one): the test binary owns the name through `proc/dbus` (`TestRealTypeDbus`), gives it back on SIGUSR1 — the unit stops —, or never owns it (`TestRealTypeDbusNeverOwned`, by timeout) |
| `dbus_cgroup_linux_external_test.go` | S0e with S0g (#734), against the same real bus, in a group made under a delegated root (skips without one): `TestLiveTypeDbusInItsGroup` — up once its main process, spawned into the group, owns the name; the name given back stops the unit and empties the group; twice, neither watch's flag lost |
| `dbus_sandbox_linux_external_test.go` | S0e with S2 (#728): `TestLiveTypeDbusSandboxed` — a unit whose `Config.Spec` carries user and PID namespaces, no_new_privs and a seccomp filter, alone and in its group, or whose directives do (`PrivateUsers=`, `PrivatePIDs=`, `NoNewPrivileges=`, `SystemCallFilter=`, through `resolveSandbox`), in its group, up once its confined main process owns the name (`proc/dbus`'s AUTH EXTERNAL names no identity), its confinement read from `/proc`; skips where the host cannot sandbox |
| `descriptors_external_test.go` | the leak tests' descriptor table (Linux): `descriptors` (`/proc/self/fd`, number and target), `settledDescriptors` — the runtime's lazily opened poller opened first, earlier tests' garbage collected until three tables in a row agree, so a pidfd an adopted handle leaves to the collector is not closed inside the window —, `sameDescriptors`; a window closes without a collection: a descriptor it left to the collector is its leak |
| `sandboxwire_internal_test.go`, `sandboxdirectives_internal_test.go` | the sandbox wired in, by replay and on `resolve`: `+` and `!`, the store's key, `MAINPID=`'s uid 0, a sandbox's failure typed with its result; each directive's field, the template merged and copied, the implied user namespace, `RootDirectoryStartOnly=`, every refusal, off the platform (`sandboxHere` plays one) |
| `sandbox_linux_external_test.go` | the shell over the fake spawner (each command's spec, `+` and `!`), then real units (`requireSandbox` skips a host that cannot sandbox): the directives' sandbox proved by failures in the child against witness directories under `t.TempDir()` — `NoNewPrivileges=`, the filter's errno, Landlock from `Spec.Sandbox`, `ReadOnlyPaths=`, `TemporaryFileSystem=`, `PrivateTmp=`, `PrivateNetwork=`, `ProtectSystem=`'s read-only `/usr` read from `mountinfo` —, a failed mount failing the start, and `TestLiveSandboxedUnitInItsGroup` in a delegated group |
| `handover_codec_internal_test.go` | S0h's codec: the model's round trip, a version it reads, a payload or a model refused, the frozen flags |
| `handover_linux_external_test.go` | S0h within one image: `TestHandoverInProcess` (the same process, its restart count, its store, the next run handed the store, no descriptor and no goroutine left), `TestHandoverCloseStopsWhatNobodyResumed`, `TestHandoverDuringTheRestartDelay` (frozen in `auto-restart`, restarted at its deadline), `TestHandoverKeepsTheStartLimit` (the count crosses a resume) |
| `handover_reexec_linux_external_test.go` | S0h across a real `execve`: this binary as supervisor (`handover-old`, `handover-new`, the guard admitting a supervisor directly under the test only): `TestRealReexecHandover`, `TestRealReexecHandoverRefused` (a later version, an older one, a corrupted payload, a descriptor that is not the one recorded — injected through `ForceHandoverVersion` and `ForceBeforeExec`, the second after the payload is written) |
| `export_linux_internal_test.go` | the re-exec's test seams, exported for those tests |
| `helperguard_linux_external_test.go` | the guard every helper passes in `TestMain`, whichever site spawned it: an unknown name, `PROC_SERVICE_TEST_HELPER` twice in the raw `/proc/self/environ`, a depth past 2 — a depth the guard writes itself, so a site that forgets `helperEnviron` still hands it on —, a supervisor anywhere but directly under the test, a 65th run under a store directory are refused with status 97 before the helper does anything, and a helper dies at 120 s. `helperEnviron` and `reexecEnviron` are the one way a helper's environment is built, Bazel's `TEST_TIMEOUT` never in it: rules_go's generated main reads it and registers a SIGTERM channel nobody reads, which made a helper swallow a stop's SIGTERM until it listened itself, and `TestMain` resets SIGTERM before the guard for a site that bypasses them; `killHolders` is the sweep a failed test leaves (a process whose environment names its store directory, proved through a handle). `TestHelperGuard*` run only harmless probes, no real supervisor, and no real unit is started by them |
| `nosleep_external_test.go` | no wall-clock wait in a production file (its go_test's `data` holds the sources) |

```sh
cd internal && go test -race ./service/proc/service/
```
