// Package service runs one service the way systemd.service(5) does (ADR 0214):
// Type=, the Exec*= commands and their prefixes, the timeouts, the watchdog,
// Restart= and the start limiter, as typed values in a Config, driven by one
// actor per unit over a pure reducer.
//
//	start, _ := service.ParseCommand([]string{"/usr/bin/daemon", "--foreground"})
//	unit, err := service.NewUnit(&service.Config{
//		Name:      "daemon",
//		ExecStart: []service.Command{start},
//		Restart:   service.RestartOnFailure,
//	})
//	err = unit.Start(ctx)      // up, or START_FAILED with its result
//	events, cancel := unit.Subscribe(0) // every change, never waited on
//	err = unit.Stop(ctx)        // ExecStop=, the signals, ExecStopPost=
//
// # Directives are values
//
// Config holds one typed field per directive; a field's zero is systemd's
// default, and Zero and Infinity are systemd's literal 0 and infinity. The
// unit-file syntax, the % specifiers, the graph between units and the
// actions on the system (SuccessAction=, StartLimitAction=) are the
// caller's. ParseCommand reads the prefixes "@-:+!|"; "+" and "!" run only
// where Config.AllowPrivilegedControl allows them.
//
// # Observing a unit
//
// Unit.State is the level — state, step, result, processes, a generation
// that grows with every change. Unit.Subscribe's queue is bounded and the
// unit never waits on it: the newest event is dropped when it is full,
// counted, and a consumer that sees a gap in the generations reads
// Unit.State.
//
// # A daemon that forks
//
// Type=forking adopts the daemon its command leaves through an Attacher —
// NewAttacher's, on Linux, darwin and the BSDs: the PID PIDFile= names, or,
// on Linux, with a Cgroup that lists its members (CgroupMembers), the one
// member whose parent is outside the cgroup (GuessMainPID=).
//
// # The descriptor store
//
// FileDescriptorStoreMax= keeps the descriptors a service hands with
// FDSTORE=1 and gives them back to its next main process, at fd 3 on with
// their FDNAME=; Unit.FDStore lists them. Linux only.
//
// # One instance per connection
//
// SpawnInstance runs a unit of a template for one accepted connection —
// Accept=yes —, the connection at fd 3 (LISTEN_FDS=1), ended with its run.
// Not on Windows.
//
// # The unit's control group
//
// A Cgroup that is a CgroupUnit — proc/cgroup's group, on Linux — runs
// KillMode=control-group (then the default) and mixed, ExitType=cgroup,
// Delegate= and OOMPolicy=, every process of the unit spawned into it.
// NewAttacher's scope names a cgroup as /proc/<pid>/cgroup does:
// proc/cgroup's Membership turns a group's directory into that path.
//
// # Type=dbus
//
// A Type=dbus unit is up once its main process runs and its BusName= is
// owned — its Config.BusWatcher, pkg/v1/proc/dbus's Conn, follows the
// name —, and stops when the bus announces the name lost. Linux only.
//
// # The sandbox
//
// ProtectSystem=, PrivateTmp=, ReadOnlyPaths=, BindPaths=,
// TemporaryFileSystem=, RootDirectory=, the Private…= namespaces,
// ProtectHostname=, NoNewPrivileges=, the capability sets, SecureBits= and
// SystemCallFilter= are Config fields, resolved into proc/process's
// Sandbox on Linux amd64 and arm64 — refused elsewhere —, added to the one
// Config.Spec carries. A "+" command runs unconfined, a "!" one keeps the
// sandbox; a start whose sandbox cannot be applied is START_FAILED
// wrapping SANDBOX_FAILED or SANDBOX_UNAVAILABLE.
//
// # Platforms
//
// The reducer and every type that needs no notification run everywhere.
// Notifications need a NotifySource — ListenNotify on Linux and FreeBSD.
// Windows kills and does nothing else: a signal other than SIGKILL is
// refused there.
package service
