// Package service is the engine of a service unit's life cycle (ADR 0214):
// systemd.service(5)'s state machine for one service — Type=, the Exec*=
// commands and their prefixes, the timeouts and their failure modes, the
// watchdog, the notification protocol, Restart=, RestartSec= on the
// kernel's backoff curve, the start limiter, the stop's escalation.
//
// # One actor over a pure reducer
//
// A Unit is one goroutine reading one channel of events: a caller's request,
// a process's end, a timer's fire, a notification. The decision is a pure
// function, reduce(model, event) → (model', effects), which reads nothing
// but its arguments; the actor runs the effects — a spawn, a signal, a
// timer, an answer, a published snapshot — and hands the outcome of a spawn
// or an adoption back as the next event. A recorded sequence of events
// replays to the same states: Table 1 of systemd.service(5) is tested so.
//
// # What a consumer sees
//
// State is the unit's level — its state, step, result, processes and
// generation. Subscribe hands out a bounded queue the actor never waits on:
// a full queue drops the newest event and counts it, and the generation
// shows the gap. Start, Stop, Restart and Reload return when their job is
// done; Fail is a failed health check, the watchdog's path; ResetFailed is
// reset-failed; Close ends the unit.
//
// # A daemon that forks
//
// Type=forking runs ExecStart= as a command and, once it exited cleanly,
// adopts the daemon it left as the main process through the Attacher: the
// PID PIDFile= names, read by the Attacher alone and retried until
// TimeoutStartSec=, or the one process of the cgroup whose parent is not in
// it (GuessMainPID=). An adopted process's end has no status, which the
// reducer reads as a clean end.
//
// # The descriptor store
//
// FileDescriptorStoreMax= keeps what a service hands with FDSTORE=1 — named
// by FDNAME=, a duplicate of a stored object dropped, past the maximum
// closed and counted — in a table the shell owns, and hands it to the next
// ExecStart= alone through proc/systemd/listen, never to a command. A
// hangup removes a descriptor unless FDPOLL=0, FDSTOREREMOVE=1 removes a
// name, FileDescriptorStorePreserve= says when the table is released, and
// a store the unit no longer matches — its ExecStart=, user or group — is
// closed whole.
//
// # One instance per connection
//
// SpawnInstance is Accept=yes's primitive: a unit of the template per
// accepted connection, handed it as LISTEN_FDS=1 at fd 3 with its own
// LISTEN_PID, ending on its own — the connection closed — once its run is
// over. How many, and their names, are the caller's.
//
// # The unit's control group
//
// A Cgroup that is also a CgroupUnit — the group proc/cgroup makes on
// Linux — runs the cgroup modes: every process of the unit is spawned into
// it; a stop signals every process in it (KillMode=control-group, the
// default there) or kills them all at the kill (mixed) — through Signal,
// never cgroup.kill, after which Linux 6.12 kills every child cloned into
// the group — and waits for it to be empty; ExitType=cgroup ends the unit
// with the group's last process; Delegate= hands the group to the
// service's user at each start and removes the groups the service made
// beneath it at each end; OOMPolicy= acts on the OOM kills its memory
// events count. Its state is watched, and read again whenever the reducer
// decides on it, so an event that arrives late never ends a unit early.
//
// # Type=dbus
//
// A Type=dbus unit is up once its main process runs and its BusName= is
// owned on the bus Config.BusWatcher follows — proc/dbus's Conn is one. The
// watch begins at the first start, before the main process is spawned: the
// owner it finds then is the level, and only a change the bus announces
// after the spawn makes the unit ready, as systemd waits for one. A name the
// bus announces lost while the unit runs stops it at once — ExecStop=, then
// the signals; exited with RemainAfterExit= —; a unit that comes up again
// without its name, lost while ExecStartPost= or a reload ran, is given two
// seconds to own it (running-revalidating) before it is stopped, as
// systemd 262 gives it. Linux only: Type=dbus is DIRECTIVE_UNSUPPORTED
// elsewhere.
//
// # The sandbox
//
// The confinement directives of systemd.exec(5) — ProtectSystem=,
// PrivateTmp=, ReadOnlyPaths=, BindPaths=, TemporaryFileSystem=,
// RootDirectory=, the Private…= namespaces, ProtectHostname=,
// NoNewPrivileges=, the capability sets, SecureBits= and the system call
// filter — are resolved by New into one coreproc.SandboxValue added to
// Spec's, which proc/exec applies before each command's program runs: on
// Linux amd64 and arm64, DIRECTIVE_UNSUPPORTED elsewhere. A "+" command
// runs without it, a "!" one keeps it; RootDirectoryStartOnly= gives the
// root to ExecStart= alone. A sandbox that cannot be applied fails the
// start with START_FAILED wrapping SANDBOX_FAILED (the result exit-code)
// or SANDBOX_UNAVAILABLE (resources); a sandboxed unit's uid 0 names no
// MAINPID= without a cgroup, and its store is keyed by its sandbox too.
//
// # What a later issue brings
//
// The supervisor's hand-over (S0h) builds on the model, which is plain
// data.
package service
