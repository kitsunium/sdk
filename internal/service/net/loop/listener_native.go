//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// keepAliveDefault is the idle time and the interval of a keepalive the
// caller left unset: the standard library's own, which the goroutine
// engine's accepted sockets get from net.Listener.Accept.
const keepAliveDefault time.Duration = 15 * time.Second

// keepAliveProbes is how many unanswered probes end a connection: the
// standard library's own.
const keepAliveProbes int = 9

// listenerRow is what the listener options found for one capability row.
type listenerRow struct {
	// name is the capability.
	name corenet.Capability
	// status and reason are what the row reads.
	status, reason string
}

// tcpListener reports whether fd is a TCP listener: the options of this file
// mean nothing, and are refused by the kernel, on any other family.
func tcpListener(fd int) bool {
	sa, err := syscall.Getsockname(fd)
	//: an unreadable socket is none of ours to configure.
	if err != nil {
		//: not TCP.
		return false
	}
	//: the two families TCP has.
	switch sa.(type) {
	//: IPv4 and IPv6.
	case *syscall.SockaddrInet4, *syscall.SockaddrInet6:
		//: TCP.
		return true
	//: unix and anything else.
	default:
		//: not TCP.
		return false
	}
}

// ceilUnits is d in whole units, rounded up, at least one for a positive d.
func ceilUnits(d, unit time.Duration) int {
	//: nothing to round.
	if d <= 0 {
		//: none.
		return 0
	}
	//: rounded up, so a bound is never cut short.
	return int((d + unit - 1) / unit)
}

// errOr is err, or def when err is nil: a reading that came back wrong has no
// error of its own.
func errOr(err error, def syscall.Errno) error {
	//: the kernel's own answer first.
	if err != nil {
		//: it.
		return err
	}
	//: the reading was wrong.
	return def
}

// expectOpt reads an integer option back and fails unless it is want.
func expectOpt(fd, level, opt, want int) error {
	v, err := syscall.GetsockoptInt(fd, level, opt)
	//: unreadable, or not what was set.
	if err != nil || v != want {
		//: not in effect.
		return errOr(err, syscall.EINVAL)
	}
	//: in effect.
	return nil
}

// shardRow is the reuseport-loops row: on when every listener owned by one
// loop reads SO_REUSEPORT back, missing — a fault, the group asked — when one
// does not, and unprobed for a group with no such listener (#856).
func shardRow(ls []Listener) listenerRow {
	row := listenerRow{name: corenet.CapabilityReuseportLoops, status: statusUnprobed, reason: "no per-loop listener"}
	//: each per-loop listener, read back.
	for _, l := range ls {
		//: shared by every loop.
		if l.Loop == 0 {
			continue
		}
		v, err := syscall.GetsockoptInt(l.FD, syscall.SOL_SOCKET, soReusePort)
		//: not sharded after all.
		if err != nil || v == 0 {
			row.status, row.reason = statusMissing, "SO_REUSEPORT not set"
			//: the fault.
			return row
		}
		row.status, row.reason = statusOn, ""
	}
	//: on, or no such listener.
	return row
}

// apply records rows in rep, the operator's disabled rows kept as they are.
func (r *capReport) apply(rows []listenerRow) {
	//: each row.
	for _, row := range rows {
		r.set(row.name, row.status, row.reason)
	}
}

// refuseSeqpacket refuses a group that listens on a unixpacket socket
// (SOCK_SEQPACKET): both backends read a connection as a byte stream, into a
// buffer that cuts a record longer than itself with no flag to say so, and a
// record that reached OnData cut would be the silent truncation the engine's
// datagram rule forbids. A refusal at Start, not a cut (ADR 0031, #974);
// unix and tcp stay.
func refuseSeqpacket(cfg *Config) error {
	//: each listener of the group.
	for _, l := range cfg.Listeners {
		sa, err := syscall.Getsockname(l.FD)
		//: only a Unix socket can be a unixpacket one.
		if _, unix := sa.(*syscall.SockaddrUnix); err != nil || !unix {
			continue
		}
		//: records, not a stream.
		if typ, err := syscall.GetsockoptInt(l.FD, syscall.SOL_SOCKET, syscall.SO_TYPE); err == nil && typ == syscall.SOCK_SEQPACKET {
			//: refused by name.
			return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
				errs.String("group", cfg.Group), errs.String("network", "unixpacket"))
		}
	}
	//: every listener is a stream.
	return nil
}
