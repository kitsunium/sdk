//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"runtime"
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// soReusePort is SO_REUSEPORT, 0x200 in the <sys/socket.h> of darwin and the
// BSDs, a different value from Linux's: absent from package syscall there,
// cited here as ADR 0018 asks.
const soReusePort int = 0x200

// linuxOnly is the reason a row of Linux's alone reads on kqueue.
const linuxOnly string = "Linux's alone: not on "

// listenerRows sets TCP_NODELAY and the keepalive on every TCP listener of
// cfg before any accept — darwin and the BSDs copy both to each socket a
// listener accepts —, reads them back, and returns the descriptors in order
// with the rows the listener options own on kqueue. The options Linux alone
// has (TCP_USER_TIMEOUT, TCP_DEFER_ACCEPT, SO_INCOMING_CPU) and
// TCP_NOTSENT_LOWAT, which kqueue does not build, read absent: a class
// A-code one the code opted into is missing, the fault State reports
// (ADR 0189, ADR 0222).
func listenerRows(cfg *Config, sw capSwitches) (fds []int, rows []listenerRow) {
	ka := listenerRow{name: corenet.CapabilityTCPKeepAlive, status: statusUnprobed, reason: "no TCP listener"}
	//: each listener of the group.
	for _, l := range cfg.Listeners {
		fds = append(fds, l.FD)
		//: only TCP has these options.
		if !tcpListener(l.FD) {
			continue
		}
		noDelay(l.FD, sw)
		ka = keepAliveRow(ka, l.FD, cfg.KeepAlive, sw)
	}
	other := "kqueue: " + linuxOnly + runtime.GOOS
	rows = append(rows, ka,
		listenerRow{name: corenet.CapabilityTCPUserTimeout, status: statusAbsent, reason: other},
		optInRow(corenet.CapabilityTCPDeferAccept, sw, other),
		optInRow(corenet.CapabilityTCPNotsentLowat, sw, "kqueue: not built"),
		shardRow(cfg.Listeners),
		optInRow(corenet.CapabilitySOIncomingCPU, sw, other),
	)
	//: the group's descriptors and the rows.
	return fds, rows
}

// keepAliveRow sets the keepalive on the TCP listener fd unless the operator
// switched it off or the group set it negative, and folds the kernel's answer
// into row: the first refusal wins, a later success never clears it.
func keepAliveRow(row listenerRow, fd int, d time.Duration, sw capSwitches) listenerRow {
	//: by what applies first.
	switch {
	//: never set — set keeps the operator's disabled —, or an earlier
	//: listener refused it, the first refusal winning.
	case sw.disabled(corenet.CapabilityTCPKeepAlive), row.status == statusAbsent:
		//: as it was.
		return row
	//: negative is the standard library's "off".
	case d < 0:
		//: present, unused.
		return listenerRow{name: row.name, status: statusAvailable, reason: "KeepAlive is negative"}
	}
	//: refused by the kernel.
	if err := setKeepAlive(fd, d); err != nil {
		//: absent, with the kernel's answer.
		return listenerRow{name: row.name, status: statusAbsent, reason: "setsockopt SO_KEEPALIVE: " + err.Error()}
	}
	//: in effect.
	return listenerRow{name: row.name, status: statusOn}
}

// optInRow is a class A-code row kqueue cannot honour: unprobed when the code
// did not opt into it, absent — missing, the code asked — when it did.
func optInRow(c corenet.Capability, sw capSwitches, reason string) listenerRow {
	//: never opted into, or switched off (set keeps disabled).
	if !sw.wants(c) {
		//: unprobed.
		return listenerRow{name: c, status: statusUnprobed, reason: "not opted into"}
	}
	//: asked for, and not built here.
	return listenerRow{name: c, status: statusAbsent, reason: reason}
}

// setKeepAlive sets SO_KEEPALIVE with the idle time and the interval of d
// (zero is keepAliveDefault) and keepAliveProbes probes where the platform
// names them (keepAliveParams), then reads SO_KEEPALIVE back.
func setKeepAlive(fd int, d time.Duration) error {
	//: unset is the standard library's default.
	if d == 0 {
		d = keepAliveDefault
	}
	//: the option first.
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE, 1); err != nil {
		//: the kernel's answer.
		return err
	}
	//: its parameters, then the option read back.
	return firstErr(keepAliveParams(fd, max(ceilUnits(d, time.Second), 1)),
		expectOpt(fd, syscall.SOL_SOCKET, syscall.SO_KEEPALIVE, 1))
}
