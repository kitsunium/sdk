// Package sdnotify — the notifier (child) side: sending sd_notify datagrams.
package sdnotify

import (
	"net"
	"os"
	"strconv"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Notify sends an sd_notify datagram carrying the given NAME=value state to
// $NOTIFY_SOCKET. When $NOTIFY_SOCKET is unset it is a no-op returning nil, per
// libsystemd; a send failure when the socket is set returns NotifyFailed. An
// empty state map under a set socket still opens and closes the connection,
// matching libsystemd's "ping" behaviour.
func Notify(state map[string]string) (err error) {
	//: an unset NOTIFY_SOCKET disables notification entirely — a silent no-op.
	raw, ok := os.LookupEnv(envNotifySocket)
	//: nothing to do, and reporting an error here would break unsupervised runs.
	if !ok || raw == "" {
		//: success with no side effect.
		return nil
	}
	//: resolve the abstract-namespace marker before dialling.
	addr := &net.UnixAddr{Name: resolveAddr(raw), Net: "unixgram"}
	//: connect a datagram socket to the supervisor's notify endpoint.
	conn, err := net.DialUnix("unixgram", nil, addr)
	//: a failure to reach the configured socket is a real error.
	if err != nil {
		//: wrap the dial fault as NotifyFailed.
		return wrapNotify(err, raw)
	}
	//: release the socket on return; surface a close fault only on the happy path.
	defer func() {
		//: close always runs; capture its error only when none preceded it.
		if closeErr := conn.Close(); closeErr != nil && err == nil {
			//: a clean send followed by a failed close still reports NotifyFailed.
			err = wrapNotify(closeErr, raw)
		}
	}()
	//: encode the state map into the newline-separated datagram body.
	payload := encodePayload(state)
	//: a single Write delivers the whole datagram.
	_, err = conn.Write([]byte(payload))
	//: a write fault means the supervisor did not receive the notification.
	if err != nil {
		//: wrap the write fault as NotifyFailed.
		return wrapNotify(err, raw)
	}
	//: the datagram was delivered (the deferred close may still set err).
	return nil
}

// wrapNotify restates the NotifyFailed sentinel fields around a stdlib/syscall
// cause, attaching the socket value for diagnostics.
func wrapNotify(cause error, socket string) error {
	//: copy the exact sentinel strings so the wrapped error matches CodeNotifyFailed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeNotifyFailed,
		Reason:   "NOTIFY_FAILED",
		Public:   "Could not send the sd_notify datagram",
		Private:  "service/proc/sdnotify: writing to $NOTIFY_SOCKET failed",
		ExitCode: exitOSErr,
	}, errs.String("socket", socket))
}

// Ready notifies the supervisor that startup is complete (READY=1).
func Ready() error {
	//: READY=1 is the canonical startup-complete signal.
	return Notify(map[string]string{"READY": "1"})
}

// Reloading notifies the supervisor that a configuration reload has begun
// (RELOADING=1).
func Reloading() error {
	//: RELOADING=1 marks the start of a reload cycle.
	return Notify(map[string]string{"RELOADING": "1"})
}

// Stopping notifies the supervisor that shutdown has begun (STOPPING=1).
func Stopping() error {
	//: STOPPING=1 announces graceful shutdown.
	return Notify(map[string]string{"STOPPING": "1"})
}

// Status publishes a single-line free-text status (STATUS=msg).
func Status(msg string) error {
	//: STATUS carries human-readable progress; the value is sent verbatim.
	return Notify(map[string]string{"STATUS": msg})
}

// Watchdog sends a watchdog keep-alive ping (WATCHDOG=1).
func Watchdog() error {
	//: WATCHDOG=1 resets the supervisor's watchdog timer.
	return Notify(map[string]string{"WATCHDOG": "1"})
}

// MainPID advertises the main process PID to the supervisor (MAINPID=pid).
func MainPID(pid int) error {
	//: MAINPID lets the supervisor track a PID other than the notifier's own.
	return Notify(map[string]string{"MAINPID": strconv.Itoa(pid)})
}
