package service

import (
	"os"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// jobKind is a job systemd would queue: what a caller asked for.
type jobKind uint8

const (
	// jobNone is no job.
	jobNone jobKind = iota
	// jobStart is a start.
	jobStart
	// jobStop is a stop.
	jobStop
	// jobRestart is a stop, then a start.
	jobRestart
	// jobReload is a reload.
	jobReload
)

// job is the job running, or the one waiting for it, and who waits for it.
type job struct {
	waiters []uint64
	kind    jobKind
	// stopped is true once a restart's stop half reached a dead state, and
	// for a start, which has none.
	stopped bool
}

// notifyState is the last state the unit announced.
type notifyState uint8

const (
	// notifyUnknown is nothing announced since the start.
	notifyUnknown notifyState = iota
	// notifyReady is READY=1.
	notifyReady
	// notifyReloading is RELOADING=1.
	notifyReloading
	// notifyStopping is STOPPING=1.
	notifyStopping
)

// timerKind names one of the unit's two timers.
type timerKind uint8

const (
	// timerState bounds the step the unit is at, or waits out the restart
	// delay.
	timerState timerKind = iota
	// timerWatchdog is the keep-alive deadline.
	timerWatchdog
	// timerPIDFile is when a Type=forking unit reads its PID file again.
	timerPIDFile
	// timerCount is how many timers a unit has.
	timerCount
)

// modelFlag is one of the model's booleans, as a bit.
type modelFlag uint16

const (
	// flagMainExited is set once the main process's exit is known.
	flagMainExited modelFlag = 1 << iota
	// flagForbidRestart is set once a caller asked for a stop.
	flagForbidRestart
	// flagRestarting is set during a restart job's stop.
	flagRestarting
	// flagDebug is set on a RestartMode=debug restart.
	flagDebug
	// flagClosing is set once Close was asked.
	flagClosing
	// flagExited is set once the reducer asked the shell to end.
	flagExited
	// flagCgroupGood is set while a unit with no main process is up on its
	// cgroup's members alone: a Type=forking unit whose main process stayed
	// unknown, an ExitType=cgroup unit whose main process ended. The cgroup's
	// emptying clears it, and ends the unit (S0g).
	flagCgroupGood
	// flagRan is set once a start began: an instance (S0f) ends with the
	// first run that is over.
	flagRan
	// flagGroupPopulated is set while the unit's group held a process when
	// the shell last read it (S0g).
	flagGroupPopulated
	// flagPrepare is set from a start until its first spawn, which prepares
	// the unit's group first: the watch, Delegate=, memory.oom.group (S0g) —
	// and a Type=dbus unit's bus name watch (S0e).
	flagPrepare
	// flagBusOwned is set while a Type=dbus unit's BusName= has an owner, as
	// the bus last said (S0e): the level, kept while the watch is down.
	flagBusOwned
)

// storeEntry is one descriptor of the store, as the model knows it: its id —
// the shell's key to the descriptor itself —, its FDNAME= and whether a
// hangup removes it. The descriptors are the shell's, never the model's.
type storeEntry struct {
	name string
	id   uint64
	poll bool
}

// model is a unit's whole state, as plain data: no channel, no function, no
// handle, so a sequence of events replays to the same model and the model can
// be written down (ADR 0214 §11). Only reduce changes it.
type model struct {
	s *settings
	// changed is when state last changed; limitBegin opens the limiter's
	// window; runtimeAt is when RuntimeMaxSec= ends the run, zero for never;
	// revalidateBegin is when a running Type=dbus unit lost its name, zero
	// when it was not running then (S0e); deadlines are what each timer is
	// armed for.
	changed, limitBegin, runtimeAt, revalidateBegin time.Time
	deadlines                                       [timerCount]time.Time
	// published is the last snapshot published, its generation included.
	published coresvc.StateValue
	// status, busError, varlinkError and exitStatusText are what the unit
	// last sent; pidFileCause is why its PID file was last refused.
	status, busError, varlinkError, exitStatusText, pidFileCause string
	job, pending                                                 job
	// closeWaiters wait for the unit to close.
	closeWaiters []uint64
	// store is the descriptor store, oldest first; storeKey the key of the
	// unit it was filled for (settings.storeKey, unless restored from
	// elsewhere — S0h).
	store    []storeEntry
	storeKey string
	// sandboxFields are the fields of the run's first sandbox failure, whose
	// code is sandboxCause (0: none) — what the start's answer wraps.
	sandboxFields []errs.FieldValue
	sandboxCause  errs.Code
	mainExit      coreproc.ExitValue
	// seq identifies each timer's current arming; a fire of an older one is
	// stale.
	seq [timerCount]uint64
	// generation counts published snapshots; notifyRefused the refused
	// notifications; reloadBegin the CLOCK_MONOTONIC microsecond the reload
	// began at, 0 when unknown.
	generation, notifyRefused, reloadBegin uint64
	// storeSeq numbers the stored descriptors; storeRefused and
	// storeDiscarded count those refused and those closed for a key the unit
	// no longer matches.
	storeSeq, storeRefused, storeDiscarded uint64
	// watchdogPeriod is WATCHDOG_USEC='s override, 0 for WatchdogSec=.
	watchdogPeriod time.Duration
	// mainPID and controlPID are the processes the unit runs; cmd is the
	// index, in list, of the command running.
	mainPID, controlPID, cmd, errno, limitCount int
	restarts                                    uint32
	state                                       coresvc.State
	sub                                         coresvc.Sub
	result, reloadResult                        coresvc.Result
	notifyAccess                                coresvc.NotifyAccess
	notify                                      notifyState
	list                                        list
	// mainFlags are the prefixes of the main command, for "-".
	mainFlags coresvc.CommandFlags
	// flags are the model's booleans.
	flags modelFlag
}

// eventKind is what happened to the unit.
type eventKind uint8

const (
	// evStart is a caller's start.
	evStart eventKind = iota
	// evStop is a caller's stop.
	evStop
	// evRestart is a caller's restart.
	evRestart
	// evReload is a caller's reload.
	evReload
	// evFail is a caller's failed health check.
	evFail
	// evResetFailed is a caller's reset-failed.
	evResetFailed
	// evClose is a caller's close.
	evClose
	// evSpawned is a spawn's outcome.
	evSpawned
	// evExit is a process's end.
	evExit
	// evTimer is a timer's fire.
	evTimer
	// evNotify is a notification.
	evNotify
	// evAttached is an adoption's outcome.
	evAttached
	// evHangup is a stored descriptor's hangup or error.
	evHangup
	// evCgroup is a change of the unit's group the watch announced (S0g).
	evCgroup
	// evSignaled is a group signal's outcome: whether the group held a
	// process when it was sent (S0g).
	evSignaled
	// evBusName is a change of BusName='s owner the bus announced, or the end
	// of the watch (S0e).
	evBusName
	// evFreeze is a caller's freeze (S0h): the shell's alone, never the
	// reducer's.
	evFreeze
)

// eventFlag is one of an event's booleans, as a bit.
type eventFlag uint16

const (
	// flagUnknown is an exit whose status is not knowable.
	flagUnknown eventFlag = 1 << iota
	// flagMember says a notification's sender belongs to the unit's cgroup.
	flagMember
	// flagMainMember says the MAINPID= a notification names does.
	flagMainMember
	// flagOK is an adoption that succeeded.
	flagOK
	// flagByFile is an adoption from PIDFile=.
	flagByFile
	// flagByGuess is an adoption GuessMainPID= guessed.
	flagByGuess
	// flagPopulated says the unit's cgroup listed a member.
	flagPopulated
	// flagEmpty says the unit's cgroup listed none.
	flagEmpty
	// flagOOM says the unit's group counted an OOM kill the reducer has not
	// seen (S0g).
	flagOOM
	// flagUnwatched says the watch of the unit's group ended: the group is
	// gone (S0g) — or the watch of the bus name: the connection is (S0e).
	flagUnwatched
	// flagOwner says BusName= has an owner (S0e).
	flagOwner
	// flagLookup says a spawn's preparation looked BusName='s owner up — the
	// level before the main process runs, flagOwner its answer (S0e).
	flagLookup
)

// event is one input of the reducer, stamped with the clock's Now when the
// actor takes it.
type event struct {
	at time.Time
	// msg is a notification.
	msg coresvc.NotifyValue
	// spawnErr is a spawn's failure; nil when pid is the spawned process.
	spawnErr error
	// reason is why an adoption was refused: its error's reason.
	reason string
	// exit is a process's end.
	exit coreproc.ExitValue
	// waiter is the caller of a request.
	waiter uint64
	// seq is the arming a timer fired for.
	seq uint64
	// monotonic is CLOCK_MONOTONIC in microseconds when the event was taken,
	// 0 where the shell cannot read it.
	monotonic uint64
	// rand is a random draw the shell makes for every event: the reducer
	// takes its jitters from it, and stays a function.
	rand uint64
	// stored is the store entry a hangup names.
	stored uint64
	// oomKills is what the watch read of the group's OOM kills, for the
	// shell, which turns a new one into flagOOM.
	oomKills int64
	// dupOf says, for each of a notification's descriptors, which stored one
	// it duplicates — an id, dupEarlier for one before it in the message, 0
	// for none —, as the shell found by fstat.
	dupOf []uint64
	// pid is a spawned, exited or adopted process.
	pid  int
	kind eventKind
	// list is the list a spawned process belongs to.
	list list
	// timer is the timer that fired.
	timer timerKind
	// sub is the step a group signal was sent for (evSignaled).
	sub coresvc.Sub
	// flags are the event's booleans.
	flags eventFlag
}

// effectKind is what the shell does.
type effectKind uint8

const (
	// efSpawn spawns a command.
	efSpawn effectKind = iota
	// efSignal signals a process.
	efSignal
	// efArm arms a timer.
	efArm
	// efDisarm disarms a timer.
	efDisarm
	// efReply answers callers.
	efReply
	// efAttach adopts a process.
	efAttach
	// efAttachFile adopts the process PIDFile= names.
	efAttachFile
	// efGuess lists the cgroup and adopts the main process it guesses.
	efGuess
	// efClose closes descriptors.
	efClose
	// efStore keeps descriptors in the store.
	efStore
	// efStoreDrop closes stored descriptors and forgets them.
	efStoreDrop
	// efForget leaves a process running, unwatched.
	efForget
	// efPublish publishes a snapshot.
	efPublish
	// efExit ends the actor.
	efExit
	// efSignalGroup signals the unit's group and reports whether it held a
	// process (S0g).
	efSignalGroup
	// efPrune removes the groups a delegated service made beneath the unit's
	// group (S0g).
	efPrune
)

// effect is one output of the reducer.
type effect struct {
	at time.Time
	// err answers the waiters.
	err error
	// snap is the snapshot to publish.
	snap coresvc.StateValue
	// cmd is the command to spawn.
	cmd coresvc.CommandValue
	// vars are the variables the engine sets for the command.
	vars []string
	// signals are a group signal's signal and those that follow it.
	signals []coreproc.Signal
	// pids are the main and control processes a group signal reaches through
	// their handles when they are outside the group.
	pids []int
	// waiters are the callers to answer.
	waiters []uint64
	// files are descriptors to close, or to store.
	files []*os.File
	// entries are what efStore keeps (one per file), what efStoreDrop
	// closes, or what a main command's spawn is handed, in order.
	entries []storeEntry
	// seq identifies an arming.
	seq uint64
	// pid is the process to signal, adopt or forget.
	pid  int
	kind effectKind
	// list and main place a spawned process.
	list list
	// sig is the signal to send.
	sig coreproc.Signal
	// timer is the timer to arm or disarm.
	timer timerKind
	// prevState and prevSub are what the published snapshot replaces; sub
	// is the step a group signal is sent for.
	prevState coresvc.State
	prevSub   coresvc.Sub
	sub       coresvc.Sub
	// main is true for the unit's main process.
	main bool
	// opts are the effect's options.
	opts effectOpt
}

// effectOpt is one of an effect's options, as a bit.
type effectOpt uint8

const (
	// optGuess asks efGuess to adopt the process it guesses, not only to
	// count the members.
	optGuess effectOpt = 1 << iota
	// optPrepare asks a spawn to prepare the unit's group first (S0g).
	optPrepare
)

// has reports whether option o is set.
func (ef *effect) has(o effectOpt) bool {
	//: the bit.
	return ef.opts&o != 0
}

// optIf is o when on, none otherwise.
func optIf(o effectOpt, on bool) effectOpt {
	//: set.
	if on {
		//: the option.
		return o
	}
	//: none.
	return 0
}

// step is one application of the reducer: the model it changes, the event it
// reads, and the effects it asks for.
type step struct {
	m   *model
	ev  *event
	out []effect
}

// has reports whether flag f is set.
func (m *model) has(f modelFlag) bool {
	//: the bit.
	return m.flags&f != 0
}

// set sets or clears flag f.
func (m *model) set(f modelFlag, on bool) {
	//: set.
	if on {
		m.flags |= f
		return
	}
	m.flags &^= f
}

// has reports whether flag f is set.
func (ev *event) has(f eventFlag) bool {
	//: the bit.
	return ev.flags&f != 0
}
