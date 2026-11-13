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
// send's (#969, #1396): a notification (notified), reported true; a send's
// own CQE without F_MORE, whose notification will never come, takes its
// count back, if one is left to take, and is reported false: it is handled as
// a plain send's, as is every other CQE of the send.
func (rr *uRing) zeroCopyDone(slot int32, c *cqe) bool {
	//: a notification.
	if c.flags&cqeFNotif != 0 {
		rr.notified(slot)
		//: settled.
		return true
	}
	cc := &rr.r.conns[slot]
	//: no notification to wait for, and one counted for it.
	if c.flags&cqeFMore == 0 && cc.zcPend > 0 {
		cc.ops--
		cc.zcPend--
	}
	//: handled as a send's.
	return false
}

// notified takes one notification of slot's back, one operation less: a
// refused send's first (zcVoid), whose send pinned nothing, then a pending
// one's (zcPend), whose pages are back (pagesBack). A notification nothing
// counts — the kernel posting one for a send whose CQE promised none — is
// dropped.
func (rr *uRing) notified(slot int32) {
	cc := &rr.r.conns[slot]
	//: by whose notification it is.
	switch {
	//: a refused send's: the flight never waited for it.
	case cc.zcVoid > 0:
		cc.zcVoid--
	//: a taken send's: its pages are back.
	case cc.zcPend > 0:
		cc.zcPend--
		rr.pagesBack(slot)
	//: nothing counted: no operation of the slot's.
	default:
		//: dropped.
		return
	}
	cc.ops--
	rr.maybeClose(slot)
}

// pagesBack settles slot's flight once a pending notification came: the last
// one for a flight sent whole releases it and flushes the connection again;
// for one held at zcPend's bound, its rest leaves plainly.
func (rr *uRing) pagesBack(slot int32) {
	cc := &rr.r.conns[slot]
	alive := cc.state == stOpen || cc.state == stClosing
	//: by what the flight waited for.
	switch {
	//: notifications still to come, or no flight held.
	case cc.zcPend != 0 || cc.flags&fSending == 0:
	//: the flight was sent whole and held for this.
	case cc.flight.off >= cc.flight.len:
		rr.releaseFlight(slot, alive)
	//: held at zcPend's bound: its rest leaves plainly.
	case cc.zc == zcHold:
		rr.sendPlainly(slot, alive)
	}
}

// zeroCopyHandled handles what a CQE of a zero-copy flight owes beyond a
// plain send's — a notification (zeroCopyDone), or a refusal
// (zeroCopyRefused) — and reports whether nothing more is to be done with it.
func (rr *uRing) zeroCopyHandled(slot int32, c *cqe) bool {
	//: a notification settles it, a refusal resends the flight plainly.
	return rr.zeroCopyDone(slot, c) || (rr.r.conns[slot].zc == zcOn && rr.zeroCopyRefused(slot, c))
}

// zeroCopyRefused handles a zero-copy send's CQE c whose result is a
// refusal, nothing of the flight sent, and reports whether it was one:
// EOPNOTSUPP from a socket whose kernel object lacks SOCK_SUPPORT_ZC — a
// Unix socket, or one accepted from a listener that lacks it, as an MPTCP
// listener does, which Go's net.Listen opens by default on Linux since Go
// 1.24 (net: defaultMPTCPEnabledListen) and server binds plain TCP instead
// for a group that opts in (#969), so here an adopted one —, after which
// every ring of the loop sends every later flight plainly, its listeners
// being the same; or ENOMEM, the pages it would pin past RLIMIT_MEMLOCK,
// which io_uring charges them to (io_uring/notif.c), a load's while its
// flights wait for their peers' ACKs, after which only this flight does. Its
// rest leaves by IORING_OP_SEND at once, whatever notification is pending
// (#1396): the refused send pinned nothing, so its own notification, which
// F_MORE promises, is counted apart (zcVoid) — the slot waits for it, the
// flight does not —, and only a send of it the kernel took (zcPend) holds the
// chunk.
func (rr *uRing) zeroCopyRefused(slot int32, c *cqe) bool {
	//: by the kernel's answer.
	switch syscall.Errno(-c.res) {
	//: the socket: never again on this loop.
	case syscall.EOPNOTSUPP:
		rr.ul.uses.sendZCMin = 0
	//: the memory limit: this flight only.
	case syscall.ENOMEM:
	//: a send's outcome, handled as a plain send's.
	default:
		//: not a refusal.
		return false
	}
	cc := &rr.r.conns[slot]
	cc.ops--
	//: its notification is to come: it holds the slot, not the flight.
	if c.flags&cqeFMore != 0 && cc.zcPend > 0 {
		cc.zcPend--
		cc.zcVoid++
	}
	rr.sendPlainly(slot, cc.state == stOpen || cc.state == stClosing)
	rr.maybeClose(slot)
	//: handled.
	return true
}

// sendPlainly sends the rest of slot's flight by IORING_OP_SEND, every send
// of it. A dead slot sends nothing; its flight is freed with it.
func (rr *uRing) sendPlainly(slot int32, alive bool) {
	cc := &rr.r.conns[slot]
	cc.zc = zcOff
	//: alive: the rest leaves.
	if alive {
		rr.sendFlight(slot)
		//: in flight.
		return
	}
	cc.flags &^= fSending
}
