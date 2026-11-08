package service

import (
	"os"
	"slices"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// ExitStatusValue is a list of exit statuses — SuccessExitStatus=,
// RestartPreventExitStatus=, RestartForceExitStatus= —: exit codes and
// terminating signals. The zero is the empty set.
type ExitStatusValue struct {
	// Codes are exit codes, 0–255.
	Codes []int
	// Signals are terminating signals.
	Signals []coreproc.Signal
}

// Contains reports whether exit is in the set: its code when it exited, its
// signal when one ended it.
func (s ExitStatusValue) Contains(exit coreproc.ExitValue) bool {
	//: a signal is matched against the signals.
	if exit.Signaled {
		//: membership of the terminating signal.
		return slices.Contains(s.Signals, exit.Signal)
	}
	//: an exit code against the codes.
	return slices.Contains(s.Codes, exit.Code)
}

// Empty reports whether the set holds nothing.
func (s ExitStatusValue) Empty() bool {
	//: no code and no signal.
	return len(s.Codes) == 0 && len(s.Signals) == 0
}

// NotifyValue is one sd_notify message as a NotifySource hands it to the
// engine: its assignments, who sent it, and the descriptors it carried.
type NotifyValue struct {
	// Fields are the message's NAME=value assignments; a name given twice
	// keeps its last value.
	Fields map[string]string
	// Files are the descriptors the message carried (SCM_RIGHTS), in order —
	// with MAINPIDFD=1 the first is the main process's pidfd —, which the
	// engine owns once Recv returned them: the store keeps those FDSTORE=1
	// hands it, and every other is closed when the message is taken, which is
	// what BARRIER=1 waits for.
	Files []*os.File
	// PID is the sender's process, 0 when unknown.
	PID int
	// UID is the sender's user, -1 when unknown.
	UID int
	// Verified is true when the kernel named PID (and UID, when known): a
	// message whose sender is not verified is dropped.
	Verified bool
}

// StateValue is a unit at one instant — the level a consumer resynchronises
// on: what Unit.State returns and what every EventValue carries.
type StateValue struct {
	// At is when the snapshot was published, on the unit's clock.
	At time.Time
	// Changed is when State last changed.
	Changed time.Time
	// Name is the unit's name.
	Name string
	// Status is the last STATUS= the unit sent.
	Status string
	// BusError is the last BUSERROR= the unit sent.
	BusError string
	// VarlinkError is the last VARLINKERROR= the unit sent.
	VarlinkError string
	// ExitStatusText is the last EXIT_STATUS= the unit sent, informative only.
	ExitStatusText string
	// MainExit is the main process's last exit, valid when MainExited.
	MainExit coreproc.ExitValue
	// Generation grows by one with every snapshot published; two events whose
	// generations are not consecutive have lost the ones between.
	Generation uint64
	// Dropped is how many events the unit's subscriptions have dropped.
	Dropped uint64
	// NotifyRefused is how many notifications were refused: an unverified or
	// unadmitted sender, a MAINPID= that could not be accepted.
	NotifyRefused uint64
	// FDStoreRefused is how many descriptors FDSTORE=1 handed that the store
	// refused and closed: past FileDescriptorStoreMax=, or to a unit without a
	// store.
	FDStoreRefused uint64
	// FDStoreDiscarded is how many stored descriptors were closed because the
	// unit no longer runs what stored them — another ExecStart=, user or
	// group.
	FDStoreDiscarded uint64
	// MainPID is the main process, 0 when there is none.
	MainPID int
	// ControlPID is the command running, 0 when there is none.
	ControlPID int
	// Errno is the last ERRNO= the unit sent.
	Errno int
	// FDStore is how many descriptors the unit's store holds.
	FDStore int
	// Restarts is how many automatic restarts ran since the last start the
	// caller asked for or RESTART_RESET=1.
	Restarts uint32
	// State is where the unit stands.
	State State
	// Sub is the step it is at.
	Sub Sub
	// Result is why its last run ended, or ResultSuccess while it runs well.
	Result Result
	// ReloadResult is how its last reload ended.
	ReloadResult Result
	// NotifyAccess is the effective NotifyAccess=, NOTIFYACCESS= included.
	NotifyAccess NotifyAccess
	// MainExited is true once the main process's exit is known.
	MainExited bool
}

// StoredFDValue is one descriptor of a unit's store (FileDescriptorStoreMax=,
// FDSTORE=1), as Unit.FDStore lists it — systemd-analyze fdstore's columns:
// what it is, never the descriptor itself.
type StoredFDValue struct {
	// Name is its FDNAME=, "stored" when the message named none.
	Name string
	// Device is the device of the object it refers to, as fstat names it.
	Device uint64
	// Inode is the inode of the object it refers to, as fstat names it.
	Inode uint64
	// Mode is the object's type and permissions, as fstat reads them.
	Mode os.FileMode
	// Poll is true while a hangup or an error on it removes it from the store:
	// FDPOLL=0 clears it, and so does an object epoll cannot watch.
	Poll bool
}

// EventValue is one published change of a unit: the snapshot after it and the
// state before it.
type EventValue struct {
	// StateValue is the unit after the change.
	StateValue
	// Previous is the state before the change.
	Previous State
	// PreviousSub is the step before the change.
	PreviousSub Sub
}
