package service

import (
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// Config is one service unit: systemd.service(5)'s directives as typed
// values, the template every command of the unit is spawned from, and the
// ports the engine reaches the world through. A field's zero is the
// directive's systemd default; coresvc.Zero and coresvc.Infinity are
// systemd's literal 0 and infinity for a duration (ADR 0214 §9).
type Config struct {
	// Spawner starts the unit's processes. Nil is proc/exec's Start.
	Spawner coresvc.Spawner
	// Attacher adopts a process the engine did not start: a MAINPID=, a
	// Type=forking daemon. Nil refuses every MAINPID= naming one, and
	// Type=forking.
	Attacher coresvc.Attacher
	// Cgroup is the unit's control group. Nil is none: NotifyAccess=exec and
	// all are refused, MAINPID= is accepted from a privileged sender only, and
	// a Type=forking unit needs PIDFile=. One that is also a
	// coresvc.CgroupMembers lets such a unit guess its main process. One that
	// is also a coresvc.CgroupUnit — proc/cgroup's Linux group — runs the
	// cgroup modes: every process is spawned into it, KillMode= defaults to
	// control-group, ExitType=cgroup, Delegate= and OOMPolicy= run, and its
	// emptying ends a unit that was up on its members alone.
	Cgroup coresvc.CgroupKill
	// Notify is the unit's sd_notify socket, which the unit owns and closes.
	// Nil is none: NOTIFY_SOCKET is not set, and a notify type, a watchdog or
	// a NotifyAccess= other than none is refused.
	Notify coresvc.NotifySource
	// BusWatcher follows who owns BusName= on the bus: a Type=dbus unit needs
	// one, which the unit watches through from its first start to Close.
	// proc/dbus's Conn is one; the caller closes it after the unit. Every other
	// type ignores it.
	BusWatcher coresvc.BusWatcher
	// Clock is what every wait runs on. Nil is the system clock.
	Clock coresvc.Clock
	// CapabilityBoundingSet is CapabilityBoundingSet=: the capabilities the
	// unit's processes keep in their bounding set, bit n capability n — every
	// other one is dropped, and cut from what root holds. Nil leaves the set as
	// inherited; systemd's "~" is the complement, the caller's to compute.
	CapabilityBoundingSet *uint64
	// Name names the unit in its snapshots and its errors. Required.
	Name string
	// PIDFile is PIDFile=: for Type=forking, the absolute, clean path the daemon
	// writes its PID to, read through Attacher.AttachPIDFile once the process
	// the engine started exited — again every 100 ms, until TimeoutStartSec=,
	// while it cannot be read. Every other type ignores it, as systemd does,
	// and the engine never removes it.
	PIDFile string
	// BusName is BusName=: the well-known name a Type=dbus unit is up on — owned
	// once its main process runs, lost to stop it. A unit with one and no Type=
	// is Type=dbus, as systemd reads it. Every other type accepts it and does
	// not watch it: systemd adopts the owner's PID as the main process when it
	// knows none, which the engine does not (ADR 0214's S0e amendment).
	BusName string
	// Shell is the shell a "|" command runs through. Empty is /bin/sh: the
	// user database's shell is the caller's to look up.
	Shell string
	// RootDirectory is RootDirectory=: the absolute directory every command
	// pivots into (pivot_root(2)), in a mount namespace, the old root detached;
	// a command's path must then hold a '/', and lies inside it. The paths of
	// ProtectSystem=, PrivateTmp=, TemporaryFileSystem= and BindPaths='s
	// targets are beneath it; ReadOnlyPaths= and BindPaths='s sources are the
	// host's. With RootDirectoryStartOnly it is ExecStart='s alone.
	RootDirectory string
	// Spec is the template every command is spawned from: its working
	// directory, environment, credentials, process group, limits, cgroup and
	// standard streams. Its Path and Args are the command's. With a
	// coresvc.CgroupUnit its CgroupPath is the unit's group when empty, and
	// must lie within it. Its Sandbox is the base the sandbox directives add
	// to — its namespaces, its mounts first —; a "+" command runs without any
	// sandbox, a "!" one keeps it and drops the credentials alone.
	Spec coreproc.Spec
	// ExecCondition is ExecCondition=: 1 to 254 skips the unit, 255 or an
	// abnormal end fails it.
	ExecCondition []coresvc.CommandValue
	// ExecStartPre is ExecStartPre=.
	ExecStartPre []coresvc.CommandValue
	// ExecStart is ExecStart=: one command, several for TypeOneshot, none for a
	// TypeOneshot with RemainAfterExit and an ExecStop.
	ExecStart []coresvc.CommandValue
	// ExecStartPost is ExecStartPost=.
	ExecStartPost []coresvc.CommandValue
	// ExecReload is ExecReload=.
	ExecReload []coresvc.CommandValue
	// ExecReloadPost is ExecReloadPost=, after a successful reload.
	ExecReloadPost []coresvc.CommandValue
	// ExecStop is ExecStop=, after a successful start only.
	ExecStop []coresvc.CommandValue
	// ExecStopPost is ExecStopPost=.
	ExecStopPost []coresvc.CommandValue
	// ReadOnlyPaths is ReadOnlyPaths=: absolute paths of the host made
	// read-only, recursively, in the unit's mount namespace.
	ReadOnlyPaths []string
	// BindPaths is BindPaths=: paths of the host bound, recursively and
	// writable, onto paths of the unit's view, which must exist.
	BindPaths []coresvc.BindPathValue
	// BindReadOnlyPaths is BindReadOnlyPaths=: as BindPaths, read-only.
	BindReadOnlyPaths []coresvc.BindPathValue
	// TemporaryFileSystem is TemporaryFileSystem=: an empty tmpfs, mode 0755,
	// on each directory of the unit's view, which must exist.
	TemporaryFileSystem []coresvc.TemporaryFileSystemValue
	// SystemCallArchitectures is SystemCallArchitectures=: "native" is the one
	// identifier taken, and only beside a SystemCallFilter=, whose filter kills
	// a call of any other ABI — x32 included — whatever this says.
	SystemCallArchitectures []string
	// SuccessExitStatus is SuccessExitStatus=.
	SuccessExitStatus coresvc.ExitStatusValue
	// RestartPreventExitStatus is RestartPreventExitStatus=.
	RestartPreventExitStatus coresvc.ExitStatusValue
	// RestartForceExitStatus is RestartForceExitStatus=.
	RestartForceExitStatus coresvc.ExitStatusValue
	// SystemCallFilter is SystemCallFilter=: a seccomp filter of named calls, an
	// allow-list, or a deny-list with Deny; a call it refuses kills the process,
	// unless SystemCallErrorNumber=. The names are taken as given: an
	// allow-list's implicit @default, as every @group, is the caller's to expand.
	SystemCallFilter coresvc.SystemCallFilterValue
	// RestartDelay is RestartSec=; zero is 100 ms.
	RestartDelay time.Duration
	// RestartMaxDelay is RestartMaxDelaySec=; zero is infinity.
	RestartMaxDelay time.Duration
	// RestartRandomizedDelay is RestartRandomizedDelaySec=.
	RestartRandomizedDelay time.Duration
	// TimeoutStart is TimeoutStartSec=; zero is 90 s, infinity for a
	// TypeOneshot.
	TimeoutStart time.Duration
	// TimeoutStop is TimeoutStopSec=; zero is 90 s.
	TimeoutStop time.Duration
	// TimeoutAbort is TimeoutAbortSec=; zero is TimeoutStop.
	TimeoutAbort time.Duration
	// RuntimeMax is RuntimeMaxSec=; zero is infinity.
	RuntimeMax time.Duration
	// RuntimeRandomizedExtra is RuntimeRandomizedExtraSec=.
	RuntimeRandomizedExtra time.Duration
	// Watchdog is WatchdogSec=; zero is off.
	Watchdog time.Duration
	// AmbientCapabilities is AmbientCapabilities=: the capabilities raised in
	// the ambient set, bit n capability n, which a process that is not root
	// keeps across execve; they must lie within CapabilityBoundingSet=.
	AmbientCapabilities uint64
	// StartLimitInterval is StartLimitIntervalSec=; zero is 10 s, coresvc.Zero
	// no limit, coresvc.Infinity a window that never ends.
	StartLimitInterval time.Duration
	// StartLimitBurst is StartLimitBurst=; zero is 5.
	StartLimitBurst int
	// RestartSteps is RestartSteps=.
	RestartSteps int
	// FileDescriptorStoreMax is FileDescriptorStoreMax=: how many descriptors
	// FDSTORE=1 may keep in the unit's store, handed back to every ExecStart=
	// process at fd 3 on — after the sockets Spec activates, newest first —
	// and $FDSTORE set to it for every command; zero is no store. It needs
	// Notify, implies NotifyAccess=main, and is Linux's only: no other
	// platform's notify socket carries descriptors.
	FileDescriptorStoreMax int
	// SystemCallErrorNumber is SystemCallErrorNumber=: the errno, 1 to 4095, a
	// call SystemCallFilter= refuses fails with; zero kills the process,
	// systemd's default.
	SystemCallErrorNumber int
	// KillSignal is KillSignal=; zero is SIGTERM (SIGKILL on Windows).
	KillSignal coreproc.Signal
	// RestartKillSignal is RestartKillSignal=; zero is KillSignal.
	RestartKillSignal coreproc.Signal
	// FinalKillSignal is FinalKillSignal=; zero is SIGKILL.
	FinalKillSignal coreproc.Signal
	// WatchdogSignal is WatchdogSignal=; zero is SIGABRT (SIGKILL on Windows).
	WatchdogSignal coreproc.Signal
	// ReloadSignal is ReloadSignal=; zero is SIGHUP.
	ReloadSignal coreproc.Signal
	// SecureBits is SecureBits=: the securebits set, as linux/securebits.h
	// numbers them (noroot 1<<0, keep-caps 1<<4, …); zero sets none.
	SecureBits uint32
	// Type is Type=.
	Type coresvc.Type
	// Restart is Restart=.
	Restart coresvc.Restart
	// RestartMode is RestartMode=.
	RestartMode coresvc.RestartMode
	// TimeoutStartFailureMode is TimeoutStartFailureMode=.
	TimeoutStartFailureMode coresvc.TimeoutMode
	// TimeoutStopFailureMode is TimeoutStopFailureMode=.
	TimeoutStopFailureMode coresvc.TimeoutMode
	// NotifyAccess is NotifyAccess=.
	NotifyAccess coresvc.NotifyAccess
	// KillMode is KillMode=; its zero is control-group with a
	// coresvc.CgroupUnit, process without (ADR 0214's S0g amendment).
	KillMode coresvc.KillMode
	// ExitType is ExitType=; cgroup needs a coresvc.CgroupUnit.
	ExitType coresvc.ExitType
	// OOMPolicy is OOMPolicy=: what an OOM kill in the cgroup does — stop by
	// default, continue under Delegate=. Continue, stop and kill need a
	// coresvc.CgroupUnit; kill sets memory.oom.group at each start.
	OOMPolicy coresvc.OOMPolicy
	// FileDescriptorStorePreserve is FileDescriptorStorePreserve=: when the
	// store is released — PreserveRestart once the unit rests dead with no job
	// and no restart due, PreserveNo at every end of a run, PreserveYes at
	// Close only, PreserveOnSuccess at Close or once the unit is failed for
	// good. Close releases every store.
	FileDescriptorStorePreserve coresvc.Preserve
	// SendSIGKILL is SendSIGKILL=; its default is yes.
	SendSIGKILL coresvc.Toggle
	// GuessMainPID is GuessMainPID=: for Type=forking without PIDFile=, the
	// main process is the one member of the Cgroup — a coresvc.CgroupMembers —
	// whose parent is not a member, when there is exactly one; no leaves the
	// main process unknown, the unit up while the cgroup has members. Every
	// other unit ignores it, as systemd does.
	GuessMainPID coresvc.Toggle
	// ProtectSystem is ProtectSystem=: yes makes /usr read-only, and /boot and
	// /efi where they exist, full /etc too; strict is refused.
	ProtectSystem coresvc.ProtectSystem
	// PrivateTmp is PrivateTmp=: disconnected mounts a tmpfs, mode 1777, on /tmp
	// and /var/tmp for each command; yes is refused.
	PrivateTmp coresvc.PrivateTmp
	// RemainAfterExit is RemainAfterExit=.
	RemainAfterExit bool
	// SendSIGHUP is SendSIGHUP=.
	SendSIGHUP bool
	// SurviveFinalKillSignal is SurviveFinalKillSignal=: Close leaves the
	// unit's processes running.
	SurviveFinalKillSignal bool
	// Delegate is Delegate=: at each start the unit's group is handed to the
	// user and the group Spec runs as, and at each end of a run the groups
	// the service made beneath it are removed. It needs a coresvc.CgroupUnit.
	Delegate bool
	// NoNewPrivileges is NoNewPrivileges=: no execve of the unit's processes, or
	// of theirs, gains a privilege.
	NoNewPrivileges bool
	// PrivateUsers is PrivateUsers=: a new user namespace, its root mapped to
	// the supervisor's effective uid and gid, one id wide, unless Spec's
	// Sandbox names the maps. A supervisor that is not root implies it beside
	// any other namespace, as systemd's per-user manager does.
	PrivateUsers bool
	// PrivateNetwork is PrivateNetwork=: a new network namespace, with a
	// loopback alone.
	PrivateNetwork bool
	// PrivatePIDs is PrivatePIDs=: a new PID namespace, each command its PID 1;
	// /proc is not mounted again. Type=forking refuses it, as systemd does.
	PrivatePIDs bool
	// ProtectHostname is ProtectHostname=: a new UTS namespace — systemd's
	// private: the hostname changes there alone; filtering sethostname, as its
	// yes does, is SystemCallFilter='s.
	ProtectHostname bool
	// PrivateIPC is PrivateIPC=: a new IPC namespace.
	PrivateIPC bool
	// RootDirectoryStartOnly is RootDirectoryStartOnly=: RootDirectory= reaches
	// ExecStart= alone; every other command keeps the rest of the sandbox in the
	// host's view.
	RootDirectoryStartOnly bool
	// AllowPrivilegedControl admits the "+" and "!" commands; false refuses
	// them (PRIVILEGE_REFUSED).
	AllowPrivilegedControl bool
}
