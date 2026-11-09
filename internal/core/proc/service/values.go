package service

import (
	"math"
	"time"
)

const (
	// Infinity is systemd's "infinity" for a duration directive: no timeout, a
	// start-limit window that never ends, no ceiling on the restart delay.
	Infinity time.Duration = math.MaxInt64
	// Zero is systemd's literal 0 for a duration directive, where it differs
	// from the default the Go zero value asks for: RestartSec=0 restarts at
	// once, StartLimitIntervalSec=0 disables the limiter, and a timeout of 0 is
	// no timeout.
	Zero time.Duration = -1
)

// Type is a service's Type= — how the engine learns that its start-up is
// complete. The zero, TypeDefault, is systemd's: dbus when the unit has a
// BusName=, else simple when it has an ExecStart=, oneshot when it has none.
type Type uint8

const (
	// TypeDefault resolves to TypeDbus with a BusName=, else to TypeSimple with
	// an ExecStart=, to TypeOneshot without.
	TypeDefault Type = iota
	// TypeSimple is up once its main process is spawned.
	TypeSimple
	// TypeExec is up once its main process has executed its binary.
	TypeExec
	// TypeForking is up once the process it spawned exits cleanly, the daemon
	// that process forked adopted as the main process — from PIDFile=, or
	// guessed among the cgroup's members (GuessMainPID=).
	TypeForking
	// TypeOneshot is up once its main processes ran to a successful end, one
	// ExecStart= after the other.
	TypeOneshot
	// TypeDbus is up once its main process runs and its BusName= is owned on the
	// bus, and stops when the name is lost (Linux; a BusWatcher follows it).
	TypeDbus
	// TypeNotify is up once its main process sends READY=1.
	TypeNotify
	// TypeNotifyReload is TypeNotify whose reload is ReloadSignal= followed by
	// RELOADING=1 and READY=1.
	TypeNotifyReload
	// TypeIdle runs as TypeSimple: the job queue systemd delays it behind is
	// the caller's.
	TypeIdle
)

// Restart is a service's Restart= policy: which results of a run restart
// it (systemd.service(5), Table 1). The zero is RestartNo.
type Restart uint8

const (
	// RestartNo never restarts.
	RestartNo Restart = iota
	// RestartOnSuccess restarts after a clean end only.
	RestartOnSuccess
	// RestartOnFailure restarts after an unclean exit code, an unclean signal,
	// a timeout, a watchdog, an OOM kill.
	RestartOnFailure
	// RestartOnAbnormal restarts after an unclean signal, a timeout, a
	// watchdog, an OOM kill — not after an exit code.
	RestartOnAbnormal
	// RestartOnWatchdog restarts after a watchdog only.
	RestartOnWatchdog
	// RestartOnAbort restarts after an unclean signal only.
	RestartOnAbort
	// RestartAlways restarts after every end but a skipped condition.
	RestartAlways
)

// RestartMode is a service's RestartMode=: whether an automatic restart
// passes through the failed state. The zero is RestartModeNormal.
type RestartMode uint8

const (
	// RestartModeNormal publishes the failed (or inactive) state, then
	// auto-restart.
	RestartModeNormal RestartMode = iota
	// RestartModeDirect goes to auto-restart without publishing the failure.
	RestartModeDirect
	// RestartModeDebug is RestartModeNormal, and the restarted unit's processes
	// get DEBUG_INVOCATION=1.
	RestartModeDebug
)

// KillMode is a service's KillMode=: which processes a stop signals. The
// zero, KillModeDefault, is systemd's control-group for a unit whose Cgroup
// is a CgroupUnit, process for any other (ADR 0214's S0g amendment).
type KillMode uint8

const (
	// KillModeDefault resolves to KillModeControlGroup when the unit's Cgroup
	// is a CgroupUnit, to KillModeProcess otherwise.
	KillModeDefault KillMode = iota
	// KillModeProcess signals the main and control processes only.
	KillModeProcess
	// KillModeControlGroup signals every process of the unit's cgroup and
	// waits for it to be empty. It needs a Cgroup that is a CgroupUnit.
	KillModeControlGroup
	// KillModeMixed signals the main and control processes, then kills every
	// process of the cgroup at the kill. It needs a Cgroup that is a
	// CgroupUnit.
	KillModeMixed
	// KillModeNone signals nothing: the stop commands run, the processes are
	// left.
	KillModeNone
)

// ExitType is a service's ExitType=: what ends it. The zero is ExitTypeMain.
type ExitType uint8

const (
	// ExitTypeMain ends the unit when its main process exits.
	ExitTypeMain ExitType = iota
	// ExitTypeCgroup ends the unit when its cgroup is empty, whether its main
	// process ended or not. It needs a Cgroup that is a CgroupUnit.
	ExitTypeCgroup
)

// NotifyAccess is a service's NotifyAccess=: whose sd_notify messages the
// engine reads. The zero is NotifyAccessNone, which the engine reads as
// NotifyAccessMain for a notify type, a watchdog or a descriptor store, as
// systemd does.
type NotifyAccess uint8

const (
	// NotifyAccessNone reads no message.
	NotifyAccessNone NotifyAccess = iota
	// NotifyAccessMain reads the main process's messages.
	NotifyAccessMain
	// NotifyAccessExec reads the main and the control processes' messages; it
	// needs the unit's cgroup.
	NotifyAccessExec
	// NotifyAccessAll reads the messages of every process of the unit's
	// cgroup; it needs the unit's cgroup.
	NotifyAccessAll
)

// TimeoutMode is TimeoutStartFailureMode= or TimeoutStopFailureMode=: what a
// start or a stop that timed out does. The zero is TimeoutTerminate.
type TimeoutMode uint8

const (
	// TimeoutTerminate sends KillSignal=, then FinalKillSignal= after
	// TimeoutStopSec=.
	TimeoutTerminate TimeoutMode = iota
	// TimeoutAbort sends WatchdogSignal=, then FinalKillSignal= after
	// TimeoutAbortSec=.
	TimeoutAbort
	// TimeoutKill sends FinalKillSignal= at once.
	TimeoutKill
)

// OOMPolicy is a service's OOMPolicy=: what an OOM kill in the unit's cgroup
// does. The zero, OOMPolicyDefault, is systemd's: stop, continue under
// Delegate=. Any other value needs a Cgroup that is a CgroupUnit.
type OOMPolicy uint8

const (
	// OOMPolicyDefault is stop, or continue under Delegate=; without a
	// CgroupUnit no OOM kill is seen.
	OOMPolicyDefault OOMPolicy = iota
	// OOMPolicyContinue keeps the unit running after an OOM kill.
	OOMPolicyContinue
	// OOMPolicyStop stops the unit, which ends with the result oom-kill.
	OOMPolicyStop
	// OOMPolicyKill has the kernel kill the whole cgroup (memory.oom.group,
	// which the engine sets at each start), the unit ending with the result
	// oom-kill.
	OOMPolicyKill
)

// Preserve is a service's FileDescriptorStorePreserve= (S0d). The zero is
// PreserveRestart, systemd's default.
type Preserve uint8

const (
	// PreserveRestart keeps the store while the unit is neither inactive nor
	// failed, or is about to restart.
	PreserveRestart Preserve = iota
	// PreserveNo releases the store when the unit stops.
	PreserveNo
	// PreserveYes keeps the store until the unit is removed.
	PreserveYes
	// PreserveOnSuccess is PreserveYes, released once the unit is failed for
	// good.
	PreserveOnSuccess
)

// Toggle is a boolean directive whose systemd default is yes — SendSIGKILL=,
// GuessMainPID= — so that its Go zero can be that default. The zero is
// ToggleDefault.
type Toggle uint8

const (
	// ToggleDefault is the directive's default.
	ToggleDefault Toggle = iota
	// ToggleYes is yes.
	ToggleYes
	// ToggleNo is no.
	ToggleNo
)

// State is where a unit stands, the nine states a supervisor reports. Its
// ActiveState is systemd's coarser reading of it. The zero is
// StateInactive.
type State uint8

const (
	// StateInactive is not running, after a success or before any start.
	StateInactive State = iota
	// StateActivating is starting: a condition, the pre-start commands, the
	// main process before it is up, the post-start commands.
	StateActivating
	// StateActive is up.
	StateActive
	// StateReloading is reloading.
	StateReloading
	// StateDeactivating is stopping: the stop commands, the signals, the
	// post-stop commands.
	StateDeactivating
	// StateFailed is not running, after a failure.
	StateFailed
	// StateAutoRestart is waiting out the restart delay.
	StateAutoRestart
	// StateExited is up with no process left: RemainAfterExit=.
	StateExited
	// StateConditionSkipped is not running because ExecCondition= said so.
	StateConditionSkipped
)

// Sub is the step a unit is at — systemd's SubState. The zero is SubDead.
type Sub uint8

const (
	// SubDead is inactive.
	SubDead Sub = iota
	// SubCondition runs ExecCondition=.
	SubCondition
	// SubStartPre runs ExecStartPre=.
	SubStartPre
	// SubStart runs ExecStart= until the unit is up.
	SubStart
	// SubStartPost runs ExecStartPost=.
	SubStartPost
	// SubRunning is up with its main process.
	SubRunning
	// SubExited is up without a process (RemainAfterExit=).
	SubExited
	// SubReload runs ExecReload=.
	SubReload
	// SubReloadSignal waits for RELOADING=1 after ReloadSignal=.
	SubReloadSignal
	// SubReloadNotify waits for READY=1 after RELOADING=1.
	SubReloadNotify
	// SubReloadPost runs ExecReloadPost=.
	SubReloadPost
	// SubStop runs ExecStop=.
	SubStop
	// SubStopWatchdog waits after WatchdogSignal=.
	SubStopWatchdog
	// SubStopSigterm waits after KillSignal=.
	SubStopSigterm
	// SubStopSigkill waits after FinalKillSignal=.
	SubStopSigkill
	// SubStopPost runs ExecStopPost=.
	SubStopPost
	// SubFinalWatchdog waits after WatchdogSignal= sent to what ExecStopPost=
	// left.
	SubFinalWatchdog
	// SubFinalSigterm waits after KillSignal= sent to what ExecStopPost= left.
	SubFinalSigterm
	// SubFinalSigkill waits after FinalKillSignal= sent to what ExecStopPost=
	// left.
	SubFinalSigkill
	// SubFailed is failed.
	SubFailed
	// SubAutoRestart waits out the restart delay.
	SubAutoRestart
	// SubRunningRevalidating is a Type=dbus unit up without its BusName= owned,
	// given two seconds to own it again before it stops.
	SubRunningRevalidating
)

// Result is why a run of a unit ended — systemd's Result=, which Restart=
// reads (systemd.service(5), Table 1). The zero is ResultSuccess.
type Result uint8

const (
	// ResultSuccess is a clean end.
	ResultSuccess Result = iota
	// ResultResources is a command that could not be spawned.
	ResultResources
	// ResultProtocol is a notify unit whose main process ended before
	// READY=1.
	ResultProtocol
	// ResultTimeout is a start, stop, reload or runtime limit that expired.
	ResultTimeout
	// ResultExitCode is an unclean exit code.
	ResultExitCode
	// ResultSignal is an unclean signal.
	ResultSignal
	// ResultCoreDump is an unclean signal that dumped core.
	ResultCoreDump
	// ResultWatchdog is a missed keep-alive, WATCHDOG=trigger, or Fail.
	ResultWatchdog
	// ResultStartLimitHit is a start the limiter refused.
	ResultStartLimitHit
	// ResultOOMKill is an OOM kill in the unit's cgroup (S0g).
	ResultOOMKill
	// ResultExecCondition is an ExecCondition= that said skip.
	ResultExecCondition
)
