// Package sdnotify — the notifier (child) side: sending sd_notify datagrams.
package sdnotify

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Notify sends an sd_notify datagram carrying the given NAME=value state to
// $NOTIFY_SOCKET. When $NOTIFY_SOCKET is unset it is a no-op returning nil, per
// libsystemd; a send failure when the socket is set returns NotifyFailed. An
// empty state map under a set socket still opens and closes the connection,
// matching libsystemd's "ping" behaviour.
//
// It waits as long as the supervisor makes it wait: a unixgram write blocks
// once the receiving buffer is full, and this call carries no deadline. A
// caller whose own work is bounded wants [NotifyContext].
func Notify(state map[string]string) error {
	//: an unbounded context arms nothing — identical to the original send.
	return NotifyContext(context.Background(), state)
}

// NotifyContext is [Notify] bounded by ctx: the datagram's write observes ctx's
// deadline, and its cancellation.
//
// It exists because the write is the only unbounded step in the call. The
// socket belongs to the supervisor, so a supervisor that stops reading — paused,
// stopped, or simply slow — blocks the writer with nothing the process can do
// about it. Where that write sits under a lock a health probe also takes, one
// stuck supervisor stops every later probe from answering: the readiness the
// datagram was meant to announce becomes unobservable because announcing it
// hung.
//
// ctx bounds the WRITE, not the dial: a unixgram dial takes no round trip.
func NotifyContext(ctx context.Context, state map[string]string) (err error) {
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
	//: encode the state map into the newline-separated datagram body, rejecting
	//: any name/value that would forge extra fields via the line delimiters.
	payload, err := encodePayload(state)
	//: a field-injecting name/value is InvalidNotification, not a send fault.
	if err != nil {
		//: propagate the typed InvalidNotification produced by encodePayload.
		return err
	}
	//: the write is the only step ctx can bound, and the only one that waits.
	return writeDatagram(ctx, conn, payload, raw)
}

// writeDatagram sends payload over conn under ctx's bound, and names a failure
// after the socket it could not reach.
func writeDatagram(ctx context.Context, conn writeDeadliner, payload, socket string) error {
	//: make the write observe ctx before performing it.
	stop, err := boundWrite(ctx, conn)
	//: the only failure here is a socket already closed, and it is the write's
	//: fault to report — but it is reported, not swallowed.
	if err != nil {
		//: wrap it as NotifyFailed like any other send fault.
		return wrapNotify(err, socket)
	}
	//: release the watcher as soon as the write is over.
	if stop != nil {
		//: nothing was armed when it is nil.
		defer stop()
	}
	//: a single Write delivers the whole datagram.
	_, err = conn.Write([]byte(payload))
	//: a write fault means the supervisor did not receive the notification.
	if err != nil {
		//: the caller's own reason travels WITH the fault when there is one.
		//: The bound is applied as a write deadline, so a cancellation and an
		//: expiry both surface as os.ErrDeadlineExceeded and a caller could not
		//: tell "I gave up" from "the supervisor was too slow" — joining ctx's
		//: error puts both in the chain, where errors.Is answers either way.
		return wrapNotify(errors.Join(err, ctx.Err()), socket)
	}
	//: the datagram was delivered (the caller's deferred close may still fail).
	return nil
}

// writeDeadliner is everything the send needs of a connection: a bound and the
// write it bounds. Taking it rather than *net.UnixConn is also what makes the
// two testable without a socket.
type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
	Write(b []byte) (n int, err error)
}

// boundWrite makes conn's write observe ctx, and returns the watcher's stop
// func — nil when ctx can never be done, which is the plain [Notify] case and
// arms nothing at all.
//
// A deadline already known is set directly because it is exact and costs no
// goroutine; the AfterFunc is what covers a cancellation with no deadline, and
// it is the only mechanism that reaches a write already in progress — a
// net.Conn has no other way to be interrupted.
func boundWrite(ctx context.Context, conn writeDeadliner) (stop func() bool, err error) {
	//: a context that can never be done bounds nothing.
	if ctx.Done() == nil {
		//: nothing armed, nothing to release.
		return nil, nil
	}
	//: an explicit deadline is handed to the kernel as one.
	if deadline, ok := ctx.Deadline(); ok {
		//: a refusal means the socket is already closed; report it rather than
		//: proceeding to a write whose failure would name something else.
		if err := conn.SetWriteDeadline(deadline); err != nil {
			//: the caller wraps it as a send fault.
			return nil, err
		}
	}
	//: and cancellation arrives as a deadline in the past, which unblocks a
	//: write already waiting on the supervisor's buffer.
	return context.AfterFunc(ctx, func() {
		//: nothing can be returned from here, and nothing needs to be: a
		//: refusal means the write is already returning with its own fault.
		if err := conn.SetWriteDeadline(time.Now()); err != nil {
			//: the write reports what went wrong.
			return
		}
	}), nil
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

// ReadyContext is [Ready] bounded by ctx — see [NotifyContext].
func ReadyContext(ctx context.Context) error {
	//: READY=1 is the canonical startup-complete signal.
	return NotifyContext(ctx, map[string]string{"READY": "1"})
}

// StatusContext is [Status] bounded by ctx — see [NotifyContext].
func StatusContext(ctx context.Context, msg string) error {
	//: STATUS carries human-readable progress; the value is sent verbatim.
	return NotifyContext(ctx, map[string]string{"STATUS": msg})
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
