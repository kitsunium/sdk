package proc

import "os"

// NotificationValue is a parsed sd_notify(3) datagram received by a Listener: the
// raw newline-separated state fields plus the kernel-verified sender PID. It
// carries no policy — a supervisor decides what READY or WATCHDOG means for
// ordering. The lifecycle flags are read through methods so State stays the
// single source of truth.
type NotificationValue struct {
	// State is the raw NAME=value field set from the datagram (READY, STATUS,
	// MAINPID, WATCHDOG, RELOADING, STOPPING, …), preserved verbatim.
	State map[string]string
	// Status is the free-text STATUS= line, empty when absent.
	Status string
	// MainPID is the PID advertised by MAINPID=, or 0 when absent.
	MainPID int
	// SenderPID is the sending process's PID as verified by the kernel via
	// SO_PASSCRED, not a value the sender can forge; 0 when credentials were
	// unavailable.
	SenderPID int
	// SenderUID is the sending process's user, as verified by the kernel with
	// SenderPID; it means nothing when SenderPID is 0.
	SenderUID int
	// SenderGID is the sending process's group, as verified by the kernel with
	// SenderPID; it means nothing when SenderPID is 0.
	SenderGID int
	// Descriptors are the descriptors the datagram brought — the ones it carried,
	// a pidfd on its sender —, which the caller owns; nil when it brought none.
	Descriptors *DescriptorsValue
}

// DescriptorsValue are the descriptors one sd_notify datagram brought
// (NotificationValue.Descriptors): the caller owns every one and closes those
// it does not keep. Descriptors that came with no assignment calling for them
// are closed by the Listener, as systemd does.
type DescriptorsValue struct {
	// Files are the descriptors the datagram carried (SCM_RIGHTS): with
	// FDSTORE=1 the ones to store, with BARRIER=1 the barrier, which the caller
	// closes once every earlier datagram is processed.
	Files []*os.File
	// MainPIDFD is the pidfd a MAINPIDFD=1 datagram carried: its process is the
	// new main process, and MainPID its PID; nil when none.
	MainPIDFD *os.File
	// SenderPIDFD is a pidfd on the sending process (SCM_PIDFD, Linux 6.5 and
	// later): the sender itself, beyond the reach of a PID reuse; nil where the
	// kernel attaches none.
	SenderPIDFD *os.File
}

// Ready reports whether the datagram carried READY=1.
func (n NotificationValue) Ready() bool {
	//: a nil State indexes safely to "" — an absent field is simply not ready.
	return n.State["READY"] == "1"
}

// Reloading reports whether the datagram carried RELOADING=1.
func (n NotificationValue) Reloading() bool {
	//: a nil State indexes safely to "" — an absent field means not reloading.
	return n.State["RELOADING"] == "1"
}

// Stopping reports whether the datagram carried STOPPING=1.
func (n NotificationValue) Stopping() bool {
	//: a nil State indexes safely to "" — an absent field means not stopping.
	return n.State["STOPPING"] == "1"
}

// Watchdog reports whether the datagram carried WATCHDOG=1 (a keep-alive ping).
func (n NotificationValue) Watchdog() bool {
	//: a nil State indexes safely to "" — an absent field means no ping.
	return n.State["WATCHDOG"] == "1"
}
