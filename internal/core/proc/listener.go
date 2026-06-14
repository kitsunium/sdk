// Package proc — the Listener port: the supervisor side of sd_notify.
package proc

// Listener is the supervisor side of the sd_notify protocol: it owns the
// AF_UNIX datagram socket a supervised child writes readiness to, and yields one
// parsed, credential-verified NotificationValue per received datagram.
// Implementations live in internal/service/proc/*.
type Listener interface {
	// Recv blocks until the next datagram arrives and returns it parsed, with the
	// sender PID verified by the kernel. It returns a typed error on a malformed
	// datagram or once the listener is closed.
	Recv() (NotificationValue, error)
	// Close releases the socket and unblocks any in-flight Recv.
	Close() error
}
