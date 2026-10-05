//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc/systemd/notify .

// Package notify is the public facade for the systemd sd_notify(3)
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
//		if err := notify.Ready(); err != nil {
//			log.Fatal(err)
//		}
//		// optionally publish progress:
//		_ = notify.Status("accepting connections")
//		// if a watchdog is configured, ping within the interval:
//		if d, ok := notify.WatchdogInterval(); ok {
//			t := time.NewTicker(d / 2)
//			defer t.Stop()
//			for range t.C {
//				_ = notify.Watchdog()
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
//	l, path, err := notify.Listen()
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
package notify
