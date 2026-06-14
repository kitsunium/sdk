//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/sdnotify .

// Package sdnotify is the public facade for the systemd sd_notify(3)
// readiness/watchdog protocol.
//
// It has two sides. A supervised process (the notifier) tells its supervisor
// when it is ready, reloading, stopping, or alive; a supervisor (the listener)
// receives those datagrams with the sender's kernel-verified PID.
//
// # Notifier
//
// The notifier functions write a single AF_UNIX datagram to the socket named by
// the $NOTIFY_SOCKET environment variable. Per libsystemd, when $NOTIFY_SOCKET
// is unset every notifier call is a no-op that returns nil — an unsupervised
// binary stays silent rather than failing:
//
//	func main() {
//		// ... finish startup ...
//		if err := sdnotify.Ready(); err != nil {
//			log.Fatal(err)
//		}
//		// optionally publish progress:
//		_ = sdnotify.Status("accepting connections")
//		// if a watchdog is configured, ping within the interval:
//		if d, ok := sdnotify.WatchdogInterval(); ok {
//			t := time.NewTicker(d / 2)
//			defer t.Stop()
//			for range t.C {
//				_ = sdnotify.Watchdog()
//			}
//		}
//	}
//
// Notify sends an arbitrary state map; Ready, Reloading, Stopping, Status,
// Watchdog, and MainPID are shorthands for the common single-field datagrams.
//
// # Listener (supervisor)
//
// Listen creates a private datagram socket, enables SO_PASSCRED so the kernel
// stamps each datagram with the sender's verified PID, and returns the socket
// path to hand a child via NOTIFY_SOCKET:
//
//	l, path, err := sdnotify.Listen()
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer l.Close()
//	// ... spawn the child with NOTIFY_SOCKET=path in its environment ...
//	n, err := l.Recv()
//	if err != nil {
//		log.Fatal(err)
//	}
//	if n.Ready() {
//		log.Printf("child %d is ready", n.SenderPID)
//	}
//
// # Platform notes
//
// The notifier and WatchdogInterval are portable across every GOOS. The
// listener relies on SO_PASSCRED / SCM_CREDENTIALS, a Linux facility: on every
// other platform Listen returns the UNSUPPORTED_PLATFORM error and never panics.
//
// A $NOTIFY_SOCKET value beginning with '@' selects the abstract socket
// namespace; the '@' is replaced by a NUL byte before binding or connecting.
package sdnotify

import (
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// Listener is the supervisor side of the protocol: it owns the datagram socket a
// supervised child writes readiness to and yields one parsed, credential-verified
// Notification per Recv.
type Listener = coreproc.Listener

// Notification is a parsed sd_notify datagram: the raw State field set plus the
// typed Status, MainPID, and kernel-verified SenderPID, with Ready/Reloading/
// Stopping/Watchdog lifecycle accessors.
type Notification = coreproc.NotificationValue

// Notify sends an sd_notify datagram carrying the given NAME=value state to
// $NOTIFY_SOCKET, or is a no-op returning nil when $NOTIFY_SOCKET is unset.
func Notify(state map[string]string) error {
	//: delegate to the service implementation.
	return svcsdnotify.Notify(state)
}

// Ready notifies the supervisor that startup is complete (READY=1).
func Ready() error {
	//: delegate to the service implementation.
	return svcsdnotify.Ready()
}

// Reloading notifies the supervisor that a configuration reload has begun
// (RELOADING=1).
func Reloading() error {
	//: delegate to the service implementation.
	return svcsdnotify.Reloading()
}

// Stopping notifies the supervisor that shutdown has begun (STOPPING=1).
func Stopping() error {
	//: delegate to the service implementation.
	return svcsdnotify.Stopping()
}

// Status publishes a single-line free-text status (STATUS=msg).
func Status(msg string) error {
	//: delegate to the service implementation.
	return svcsdnotify.Status(msg)
}

// Watchdog sends a watchdog keep-alive ping (WATCHDOG=1).
func Watchdog() error {
	//: delegate to the service implementation.
	return svcsdnotify.Watchdog()
}

// MainPID advertises the main process PID to the supervisor (MAINPID=pid).
func MainPID(pid int) error {
	//: delegate to the service implementation.
	return svcsdnotify.MainPID(pid)
}

// Listen creates a supervisor-side sd_notify socket and returns the Listener plus
// the socketPath a child should be given via NOTIFY_SOCKET. On non-Linux
// platforms it returns the UNSUPPORTED_PLATFORM error.
func Listen() (l Listener, socketPath string, err error) {
	//: delegate to the service implementation.
	return svcsdnotify.Listen()
}

// WatchdogInterval reports the watchdog ping interval the supervisor configured
// via $WATCHDOG_USEC; ok is false when the variable is unset or malformed.
func WatchdogInterval() (d time.Duration, ok bool) {
	//: delegate to the service implementation.
	return svcsdnotify.WatchdogInterval()
}
