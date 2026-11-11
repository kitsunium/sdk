//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// sendZCProbeWait bounds each of the probe's two waits: its send's CQE,
	// then its notification.
	sendZCProbeWait time.Duration = 200 * time.Millisecond
	// sendZCProbeTries bounds the probe's waits for its two CQEs.
	sendZCProbeTries int = 4
	// sendZCProbeCQEs is the most CQEs one of its reaps takes: the send's
	// and the notification.
	sendZCProbeCQEs int = 2
)

// probeSendZC decides whether a flight of at least sz.sendZCMin bytes leaves
// by IORING_OP_SEND_ZC (#969): the group opted into uring-send-zc (class
// A-code), the operator did not switch it off, REGISTER_PROBE reports the
// opcode, and on a ring of its own one zero-copy send of a datagram on
// loopback completes, with F_MORE, then its notification. A refusal reports
// the capability absent, missing when opted into. It runs on the loop's
// thread, after the rings are built.
func (ul *uLoop) probeSendZC() {
	//: the operator's switch: never probed.
	if ul.sw.disabled(corenet.CapabilityURingSendZC) {
		//: disabled.
		return
	}
	reason := ul.sendZCRefusal()
	//: present and asked for.
	if reason == "" && ul.sw.wants(corenet.CapabilityURingSendZC) {
		ul.uses.sendZCMin = ul.lp.sz.sendZCMin
	}
	ul.rep.found(ul.sw, corenet.CapabilityURingSendZC, reason == "", reason)
}

// sendZCRefusal runs the probe and says why zero-copy sends are refused, ""
// when the send and its notification came back.
func (ul *uLoop) sendZCRefusal() string {
	//: a test plays a kernel that refuses.
	if ul.hooks.failStage == "send_zc" {
		//: as an old kernel would.
		return "send zc refused: " + syscall.EINVAL.Error()
	}
	u, step, errno := openURing(probeEntries, 0)
	//: no ring for the probe.
	if errno != 0 {
		//: named.
		return "send zc probe " + step + ": " + errno.Error()
	}
	defer u.close()
	ok, perr := u.supports(opSendZC)
	//: before 6.0.
	if perr != 0 || !ok {
		//: absent.
		return "REGISTER_PROBE: SEND_ZC unsupported"
	}
	//: one datagram and its notification.
	return sendZCRound(u)
}

// sendZCRound sends three bytes zero-copy from a connected UDP socket to a
// loopback one on u and reads the send's CQE, then its notification.
func sendZCRound(u *uring) string {
	rx, tx, why := loopbackPair()
	//: no sockets.
	if why != "" {
		//: named.
		return "send zc probe " + why
	}
	defer closePair(rx, tx)
	sa, err := syscall.Getsockname(rx)
	//: no address to send to.
	if err != nil {
		//: named.
		return "send zc probe getsockname: " + err.Error()
	}
	//: connected, so the send names no address.
	if err := syscall.Connect(tx, sa); err != nil {
		//: named.
		return "send zc probe connect: " + err.Error()
	}
	s := u.get()
	//: an empty probe ring always has room.
	if s == nil {
		//: busy.
		return "send zc probe: no SQE"
	}
	payload := []byte("kit")
	prepSendZC(s, 0, payload, udSend)
	onProcessFD(s)
	s.fd = int32(tx)
	sent, notified, res := sendZCReap(u)
	//: by what came back.
	switch {
	case res < 0:
		//: refused.
		return "send zc refused: " + syscall.Errno(-res).Error()
	case !sent || res != int32(len(payload)):
		//: never completed.
		return "send zc: no completion with F_MORE within the bound"
	case !notified:
		//: the pages never came back.
		return "send zc: no notification within the bound"
	}
	//: both CQEs.
	return ""
}

// sendZCReap waits, bounded, for a zero-copy send's CQE and its
// notification: whether each came, and the send's result.
func sendZCReap(u *uring) (sent, notified bool, sendRes int32) {
	ts := kernelTimespec{nsec: int64(sendZCProbeWait)}
	//: bounded.
	for i := 0; !notified && i < sendZCProbeTries; i++ {
		u.wait(&ts, 1)
		u.reap(sendZCProbeCQEs, func(c *cqe) {
			//: the notification, or the send's own CQE.
			if c.flags&cqeFNotif != 0 {
				notified = true
				return
			}
			sent, sendRes = c.flags&cqeFMore != 0, c.res
		})
		//: refused: no notification follows.
		if sendRes < 0 {
			break
		}
	}
	//: as seen.
	return sent, notified, sendRes
}

// zeroCopyDone handles what a zero-copy send's CQEs owe beyond a plain
// send's (#969). The notification — the flight's pages back — is one
// operation less and one pending send less; once the last came for a flight
// sent whole, the flight is released and the connection flushed again; it is
// reported true. A send's own CQE without F_MORE, whose notification will
// never come, takes that count back and is reported false: it is handled as
// a plain send's.
func (rr *uRing) zeroCopyDone(slot int32, c *cqe) bool {
	cc := &rr.r.conns[slot]
	//: the send's own CQE.
	if c.flags&cqeFNotif == 0 {
		//: no notification to wait for.
		if c.flags&cqeFMore == 0 {
			cc.ops--
			cc.zcPend--
		}
		//: handled as a send's.
		return false
	}
	cc.ops--
	cc.zcPend--
	alive := cc.state == stOpen || cc.state == stClosing
	//: the flight was sent whole and held for this.
	if cc.zcPend == 0 && cc.flags&fSending != 0 && cc.flight.off >= cc.flight.len {
		rr.releaseFlight(slot, alive)
	}
	rr.maybeClose(slot)
	//: settled.
	return true
}
