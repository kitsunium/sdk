// Package proc — the NotificationValue value type: a parsed sd_notify datagram.
package proc

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
