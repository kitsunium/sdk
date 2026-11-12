//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"encoding/binary"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The datagram lane's part of the io_uring ABI, and its sizes (#852).
const (
	// opSendmsg is IORING_OP_SENDMSG.
	opSendmsg uint8 = 9
	// opRecvmsg is IORING_OP_RECVMSG; multishot since 6.0.
	opRecvmsg uint8 = 10
	// recvmsgOutBytes is struct io_uring_recvmsg_out: namelen, controllen,
	// payloadlen and flags, four u32 at the head of each buffer.
	recvmsgOutBytes int = 16
	// packetNameRoom is the room each buffer keeps for the sender's address,
	// a sockaddr_in6 (28 bytes) rounded up.
	packetNameRoom int = 32
	// packetCtrlRoom is the room each buffer keeps for control messages:
	// UDP_GRO's int, SO_RXQ_OVFL's u32 and an in6_pktinfo fit with room left.
	packetCtrlRoom int = 128
	// packetPayloadRoom is the payload room of each buffer: a GRO train is at
	// most 64 KiB.
	packetPayloadRoom int = 1 << 16
	// packetBufs is how many buffers a lane gives the kernel, a power of two.
	packetBufs uint32 = 128
	// packetEntries sizes a lane's ring.
	packetEntries uint32 = 256
	// packetReplies is the most replies a lane has queued or in flight.
	packetReplies int = 1024
	// packetOutBytes is the reply payload arena of a lane.
	packetOutBytes int = 1 << 20
	// packetRecordBytes is one SENDMSG's record in the reply arena: its
	// msghdr, its iovec, the peer's sockaddr, then the control data — the
	// source's IP_PKTINFO or IPV6_PKTINFO (40 at most) and the UDP_SEGMENT
	// cmsg (24).
	packetRecordBytes int = 192
	// gsoMaxSegments is the most datagrams one UDP_SEGMENT send carries.
	gsoMaxSegments int = 64
	// gsoMaxBytes is the most payload one UDP_SEGMENT send carries.
	gsoMaxBytes int = 65507
	// packetDrainMax bounds the datagrams a handover reads after its
	// receive ended.
	packetDrainMax int = 1 << 16
	// packetProbeWait bounds the self-test's wait for its datagram.
	packetProbeWait int64 = 200_000_000
	// packetTick is the lane's longest wait, a backstop to its eventfd.
	packetTick int64 = 100_000_000
	// packetSendWaits bounds a handover's waits for its replies' completions.
	packetSendWaits int = 50
	// packetQuiesceWait is one of quiesce's waits, packetSendWaits of them at
	// most: a cancel and a send complete in microseconds.
	packetQuiesceWait int64 = 10_000_000
)

// The kinds of a lane's user_data.
const (
	pudRecv uint64 = 1 << 56
	pudSend uint64 = 2 << 56
	pudWake uint64 = 3 << 56
	// pudProbe is the self-test's receive.
	pudProbe uint64 = 4 << 56
	// pudCancel is a cancel's own completion.
	pudCancel uint64 = 5 << 56
	// pudKind masks the kind.
	pudKind uint64 = 0xFF << 56
)

// The socket-level constants the lane reads and sets, Linux's uapi values.
const (
	solUDP     int    = 17
	udpGRO     int    = 104
	udpSegment int    = 103
	soRxqOvfl  int    = 40
	msgTrunc   uint32 = 0x20
	msgCtrunc  uint32 = 0x08
	afInet     uint16 = 2
	afInet6    uint16 = 10
	// cmsgHdrBytes is struct cmsghdr on a 64-bit architecture.
	cmsgHdrBytes int = 16
	// segmentCmsgBytes is CMSG_SPACE of UDP_SEGMENT's u16.
	segmentCmsgBytes uint64 = 24
	// sockaddrIn4Bytes and sockaddrIn6Bytes are sockaddr_in and sockaddr_in6.
	sockaddrIn4Bytes uint32 = 16
	sockaddrIn6Bytes uint32 = 28
	// cmsgAlign is CMSG_ALIGN's unit on a 64-bit architecture.
	cmsgAlign int = 8
	// cmsgIntBytes is the int or u32 UDP_GRO and SO_RXQ_OVFL carry.
	cmsgIntBytes int = 4
	// probeFinalTries bounds the self-test's waits for its two final completions.
	probeFinalTries int = 4
)

// The lane's flags.
const (
	// laneArmed is a RECVMSG multishot in the kernel.
	laneArmed uint8 = 1 << iota
	// laneWoke is the wake eventfd's multishot poll in the kernel.
	laneWoke
	// laneHanding is a handover under way.
	laneHanding
	// laneGSOOff is UDP_SEGMENT refused by the device: one datagram a send.
	laneGSOOff
)

// pendingReply is one reply Reply queued: its peer, the source it leaves
// from, and its bytes in the payload arena.
type pendingReply struct {
	to  netip.AddrPort
	src replySource
	off uint32
	n   uint32
}

// packetLane is one lane's state, owned by its thread.
type packetLane struct {
	// state is laneServe, laneHandover or laneStop; done is closed once the
	// thread returned, by doneOnce; gsoSends counts the UDP_SEGMENT sends,
	// for the suite.
	state    atomic.Int32
	done     chan struct{}
	doneOnce sync.Once
	gsoSends atomic.Uint64
	cfg      PacketConfig
	// failStage fails the probe at the named stage: the suite's injection,
	// empty in service.
	failStage string
	u         *uring
	// efd is the lane's wake eventfd, -1 once closed; wakeMu orders a wake
	// against its close.
	efd    int
	wakeMu sync.Mutex
	// family is the socket's address family.
	family uint16
	// hdr is the multishot RECVMSG's msghdr: the room of the name and the
	// control data, the kernel's layout of each buffer.
	hdr syscall.Msghdr
	// flags are laneArmed, laneWoke, laneHanding and laneGSOOff.
	flags uint8
	// recycle are the buffers this batch's completions used.
	recycle []uint16
	// out is the reply payload arena, outOff its next free byte; rec the
	// SENDMSG records, recNext the next free one, segs each one's datagrams
	// and gso whether it is a UDP_SEGMENT send.
	out, rec []byte
	outOff   uint32
	recNext  int
	segs     []uint16
	gso      []bool
	pinned   []bool
	pend     []pendingReply
	inflight int
	// drainBuf is the handover's read buffer, outside the Go heap; arena
	// is the one mapping out, rec and drainBuf are cut from.
	drainBuf, arena []byte
	// The batch's counts, published once a batch; handed counts the
	// datagrams handed on since the last OnBatchEnd.
	total, oversized, truncated, failures, gsoOver, drops, handed uint64
	lastOvfl                                                      uint32
	// onCQE is the method value reap calls, made once.
	onCQE func(c *cqe)
	tick  kernelTimespec
	// cur is the destination of the message being delivered; seen the
	// batch's senders and the destinations they reached, a ring of seenLen
	// entries whose next slot is seenNext (#972).
	cur      replySource
	seen     [packetSeenMax]seenPeer
	seenLen  int
	seenNext int
}

// StartPacket starts one datagram lane on io_uring (#852): a thread locked to
// it probes the kernel — the ring, RECVMSG and SENDMSG in REGISTER_PROBE, the
// provided-buffer ring, then a loopback multishot RECVMSG self-test — and only
// then serves cfg.FD. A failed stage is LoopBackendUnavailable naming it, the
// descriptor closed, and server serves the socket on its batched reader; off
// the io_uring architectures and off Linux it is LoopBackendUnavailable at the
// stage "arch", before anything is tried.
//
// Goroutine lifecycle: one per lane, locked to its OS thread, started here;
// it returns once Stop is asked or a handover drained the socket, and closes
// Done last.
//
// IFACE-OPAQUE: the lane's state is the loop thread's alone; server drives
// its life cycle and nothing else.
func StartPacket(cfg *PacketConfig) (PacketLane, error) {
	//: no injected failure in service.
	return startPacket(cfg, "")
}

// startPacket is StartPacket with the suite's injection: failStage names the
// probe stage that fails, empty for none.
//
// Goroutine lifecycle: StartPacket's — one per lane, locked to its OS thread,
// started here; it returns once Stop is asked or a handover drained the
// socket, and closes Done last.
//
// IFACE-OPAQUE: StartPacket's lane, the same value.
func startPacket(cfg *PacketConfig, failStage string) (PacketLane, error) {
	lp := &packetLane{cfg: *cfg, failStage: failStage, efd: -1, done: make(chan struct{})}
	ready := make(chan error, 1)
	go lp.run(ready)
	//: the probe's verdict, from the lane's own thread.
	if err := <-ready; err != nil {
		<-lp.done
		//: refused, the descriptor closed.
		return nil, err
	}
	//: serving.
	return lp, nil
}

// has reports flag f.
func (lp *packetLane) has(f uint8) bool {
	//: set.
	return lp.flags&f != 0
}

// set sets flag f to on.
func (lp *packetLane) set(f uint8, on bool) {
	//: by value.
	if on {
		lp.flags |= f
	} else {
		lp.flags &^= f
	}
}

// Handover implements PacketLane.
func (lp *packetLane) Handover() {
	//: a lane already stopping keeps its course.
	if lp.state.CompareAndSwap(laneServe, laneHandover) {
		lp.wake()
	}
}

// Stop implements PacketLane.
func (lp *packetLane) Stop() {
	lp.state.Store(laneStop)
	lp.wake()
	<-lp.done
}

// Done implements PacketLane.
func (lp *packetLane) Done() <-chan struct{} {
	//: closed by the lane's thread, last.
	return lp.done
}

// run is the lane's thread: the probe, then the batches until a stop or
// the end of a handover.
//
// Goroutine lifecycle: one per lane, started by StartPacket, locked to its
// OS thread; it returns once Stop is asked, or a handover drained the socket,
// and closes the lane's done last.
func (lp *packetLane) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer lp.doneOnce.Do(func() { close(lp.done) })
	defer lp.release()
	//: refused at a stage: the caller serves the socket elsewhere.
	if stage, why := lp.setup(); stage != "" {
		ready <- errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
			errs.String("group", lp.cfg.Group), errs.String("stage", stage), errs.String("reason", why))
		//: refused.
		return
	}
	ready <- nil
	lp.serve()
}

// setup opens the ring, probes it and self-tests it, then arms the socket's
// receive and the wake poll; it returns the failed stage and why.
func (lp *packetLane) setup() (stage, why string) {
	lp.onCQE = lp.complete
	lp.tick = kernelTimespec{nsec: packetTick}
	//: the ring, its ops and its buffers.
	if stage, why := lp.openRing(); stage != "" {
		//: refused.
		return stage, why
	}
	lp.hdr.Namelen = uint32(packetNameRoom)
	lp.hdr.Controllen = uint64(packetCtrlRoom)
	//: the self-test.
	if why := lp.selfTest(); why != "" || lp.failStage == "recvmsg" {
		//: refused.
		return "recvmsg", why + lp.failStage
	}
	//: the memory the replies and the handover use.
	if why := lp.mapArenas(); why != "" {
		//: refused.
		return "arena", why
	}
	//: the socket and the wake.
	if stage, why := lp.armSocket(); stage != "" {
		//: refused.
		return stage, why
	}
	//: serving.
	return "", ""
}

// openRing opens the lane's ring — COOP_TASKRUN and SINGLE_ISSUER where the
// kernel takes them —, asks REGISTER_PROBE for the lane's ops and registers
// the provided buffers; it returns the failed stage and why.
func (lp *packetLane) openRing() (stage, why string) {
	ring, step, errno := openURing(packetEntries, setupCoopTaskrun|setupSingleIssuer)
	//: an older kernel refuses the flags: the plain ring.
	if errno == syscall.EINVAL {
		ring, step, errno = openURing(packetEntries, 0)
	}
	//: no ring.
	if errno != 0 || lp.failStage == "setup" {
		//: the step and the kernel's answer.
		return "setup", step + ": " + errno.Error()
	}
	lp.u = ring
	ok, perr := ring.supports(opRecvmsg, opSendmsg, opAsyncCancel, opPollAdd)
	//: an op missing.
	if perr != 0 || !ok || lp.failStage == "probe" {
		//: named.
		return "probe", "REGISTER_PROBE: RECVMSG, SENDMSG, ASYNC_CANCEL or POLL_ADD unsupported"
	}
	size := uint32(recvmsgOutBytes + packetNameRoom + packetCtrlRoom + max(packetPayloadRoom, lp.cfg.Ceiling+1))
	//: the buffer ring.
	if errno := ring.setupBuffers(packetBufs, size, packetBufs, false); errno != 0 || lp.failStage == "buf_ring" {
		//: refused.
		return "buf_ring", "IORING_REGISTER_PBUF_RING: " + errno.Error()
	}
	//: opened.
	return "", ""
}

// armSocket reads the socket's family, opens the wake eventfd, asks for UDP
// GRO and arms the receive and the wake; it returns the failed stage and why.
func (lp *packetLane) armSocket() (stage, why string) {
	family, ferr := socketFamily(lp.cfg.FD)
	//: not a datagram socket the lane serves.
	if ferr != "" {
		//: refused.
		return "socket", ferr
	}
	lp.family = family
	efd, _, e := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	//: no wake.
	if e != 0 {
		//: refused.
		return "eventfd", e.Error()
	}
	lp.efd = int(efd)
	//: GRO on: a refusal leaves one datagram a message, which the lane reads as well.
	swallowErr(syscall.SetsockoptInt(lp.cfg.FD, solUDP, udpGRO, 1))
	lp.armRecv()
	lp.armWake()
	lp.u.submit()
	//: armed.
	return "", ""
}

// mapArenas maps the reply arena, its records and the handover's buffer,
// outside the Go heap: the kernel reads them after Reply returned.
func (lp *packetLane) mapArenas() string {
	out, err := syscall.Mmap(-1, 0, packetOutBytes+packetReplies*packetRecordBytes+recvmsgOutBytes+packetNameRoom+packetCtrlRoom+packetPayloadRoom,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	//: no memory.
	if err != nil {
		//: named.
		return "mmap: " + err.Error()
	}
	lp.arena = out
	lp.out = out[:packetOutBytes:packetOutBytes]
	lp.rec = out[packetOutBytes : packetOutBytes+packetReplies*packetRecordBytes : packetOutBytes+packetReplies*packetRecordBytes]
	lp.drainBuf = out[packetOutBytes+packetReplies*packetRecordBytes:]
	lp.segs = make([]uint16, packetReplies)
	lp.gso = make([]bool, packetReplies)
	lp.pinned = make([]bool, packetReplies)
	lp.pend = make([]pendingReply, 0, packetReplies)
	lp.recycle = make([]uint16, 0, packetBufs)
	//: mapped.
	return ""
}

// selfTest arms a multishot RECVMSG on a loopback socket of its own, sends
// it one datagram and reads it back from a provided buffer: the header, the
// sender's family and the payload. It returns why it failed.
func (lp *packetLane) selfTest() string {
	rx, tx, why := loopbackPair()
	//: no sockets.
	if why != "" {
		//: named.
		return why
	}
	defer closePair(rx, tx)
	got, why := lp.probeOne(rx, tx)
	//: nothing came back.
	if why != "" {
		//: named.
		return why
	}
	//: the datagram, its buffer and the promise of more.
	if got.userData != pudProbe || got.res <= 0 || got.flags&cqeFBuffer == 0 || got.flags&cqeFMore == 0 {
		//: named.
		return "multishot RECVMSG: no datagram with a buffer and F_MORE within 200 ms"
	}
	bid := uint16(got.flags >> cqeBufferShift)
	name, _, payload, _ := splitRecvmsgOut(lp.u.buf(bid)[:got.res])
	lp.u.addBuf(bid)
	lp.u.publishBufs()
	from := sockaddrPort(name)
	//: the header as laid out.
	if string(payload) != "kit" || !from.IsValid() || !from.Addr().Is4() {
		//: named.
		return "multishot RECVMSG: the buffer's header, address or payload did not read back"
	}
	//: passed.
	return ""
}

// probeOne arms the self-test's receive on rx, sends it three bytes from tx,
// takes the first completion, then cancels the receive and reaps its end.
func (lp *packetLane) probeOne(rx, tx int) (cqe, string) {
	ring := lp.u
	prepRecvmsg(ring.get(), rx, &lp.hdr, pudProbe)
	ring.submit()
	addr, err := syscall.Getsockname(rx)
	//: the receiver's address.
	if err != nil {
		//: named.
		return cqe{}, "getsockname: " + err.Error()
	}
	//: one datagram of three bytes.
	if err := syscall.Sendto(tx, []byte("kit"), 0, addr); err != nil {
		//: named.
		return cqe{}, "sendto: " + err.Error()
	}
	ts := kernelTimespec{nsec: packetProbeWait}
	ring.wait(&ts, 1)
	var got cqe
	ring.reap(1, func(c *cqe) { got = *c })
	prepCancelUserData(ring.get(), pudProbe, pudCancel)
	ring.submit()
	lp.awaitFinals(&ts)
	//: the first completion.
	return got, ""
}

// awaitFinals reaps the self-test's cancel and its receive's last
// completion, within probeFinalTries waits.
func (lp *packetLane) awaitFinals(ts *kernelTimespec) {
	finals := 0
	count := func(c *cqe) {
		//: a completion that ends a request.
		if c.userData == pudCancel || c.flags&cqeFMore == 0 {
			finals++
		}
	}
	//: bounded.
	for i := 0; finals < 2 && i < probeFinalTries; i++ {
		lp.u.wait(ts, 1)
		lp.reapCQ(count)
	}
}

// loopbackPair opens two UDP sockets on 127.0.0.1, the first bound.
func loopbackPair() (rx, tx int, why string) {
	rx, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	//: no socket.
	if err != nil {
		//: named.
		return -1, -1, "socket: " + err.Error()
	}
	//: bound to a port of the kernel's.
	if err := syscall.Bind(rx, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		swallowErr(syscall.Close(rx))
		//: named.
		return -1, -1, "bind: " + err.Error()
	}
	tx, err = syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	//: no sender.
	if err != nil {
		swallowErr(syscall.Close(rx))
		//: named.
		return -1, -1, "socket: " + err.Error()
	}
	//: both.
	return rx, tx, ""
}

// closePair closes the self-test's two sockets.
func closePair(rx, tx int) {
	swallowErr(syscall.Close(rx))
	swallowErr(syscall.Close(tx))
}

// socketFamily is fd's address family, AF_INET or AF_INET6.
func socketFamily(fd int) (uint16, string) {
	sa, err := syscall.Getsockname(fd)
	//: by the kernel's answer.
	switch {
	case err != nil:
		//: named.
		return 0, "getsockname: " + err.Error()
	case isInet4(sa):
		//: IPv4.
		return afInet, ""
	case isInet6(sa):
		//: IPv6.
		return afInet6, ""
	default:
		//: a Unix datagram socket.
		return 0, "not a UDP socket"
	}
}

// isInet4 reports an IPv4 socket address.
func isInet4(sa syscall.Sockaddr) bool {
	_, ok := sa.(*syscall.SockaddrInet4)
	//: by type.
	return ok
}

// isInet6 reports an IPv6 socket address.
func isInet6(sa syscall.Sockaddr) bool {
	_, ok := sa.(*syscall.SockaddrInet6)
	//: by type.
	return ok
}

// prepRecvmsg fills s as a multishot RECVMSG on a process descriptor into the
// lane's buffer group: each completion's buffer starts with an
// io_uring_recvmsg_out, then hdr's name and control rooms, then the payload.
func prepRecvmsg(s *sqe, fd int, hdr *syscall.Msghdr, ud uint64) {
	s.opcode = opRecvmsg
	s.fd = int32(fd)
	s.addr = uint64(uintptr(unsafe.Pointer(hdr)))
	s.len = 1
	s.ioprio = recvMultishot
	s.flags = sqeBufferSelect
	s.bufIndex = bufGroup
	s.userData = ud
}

// prepSendmsg fills s as a SENDMSG of the record at hdr.
func prepSendmsg(s *sqe, fd int, hdr unsafe.Pointer, ud uint64) {
	s.opcode = opSendmsg
	s.fd = int32(fd)
	s.addr = uint64(uintptr(hdr))
	s.len = 1
	s.userData = ud
}

// armRecv arms the socket's multishot RECVMSG.
func (lp *packetLane) armRecv() {
	s := lp.u.get()
	//: a ring that takes nothing: the next batch tries again.
	if s == nil {
		//: unarmed.
		return
	}
	prepRecvmsg(s, lp.cfg.FD, &lp.hdr, pudRecv)
	lp.set(laneArmed, true)
}

// armWake arms the multishot poll on the wake eventfd.
func (lp *packetLane) armWake() {
	s := lp.u.get()
	//: a ring that takes nothing: the tick still bounds the wait.
	if s == nil {
		//: unarmed.
		return
	}
	prepPollMulti(s, lp.efd, pudWake)
	lp.set(laneWoke, true)
}

// wake writes the lane's eventfd; any goroutine may call it.
func (lp *packetLane) wake() {
	lp.wakeMu.Lock()
	defer lp.wakeMu.Unlock()
	//: a lane that stopped has nothing to wake.
	if lp.efd < 0 {
		//: nothing.
		return
	}
	one := new(uint64(1))
	_, err := syscall.Write(lp.efd, (*[8]byte)(unsafe.Pointer(one))[:])
	//: a full counter wakes the lane as well.
	swallowErr(err)
}

// serve runs the batches until a stop, or the end of a handover.
func (lp *packetLane) serve() {
	//: one batch a wake.
	for {
		st := lp.state.Load()
		//: now.
		if st == laneStop {
			//: the ring's close cancels the rest.
			return
		}
		//: the drain starts once.
		if st == laneHandover && !lp.has(laneHanding) {
			lp.beginHandover()
		}
		//: the receive ended: read the rest, send, stop.
		if lp.has(laneHanding) && !lp.has(laneArmed) {
			lp.finishHandover()
			//: handed over.
			return
		}
		lp.u.wait(&lp.tick, 1)
		lp.batch()
	}
}

// batch reaps every completion, ends the batch for the handler, sends its
// replies and gives the buffers back.
func (lp *packetLane) batch() {
	u := lp.u
	//: the overflowed completions, into the CQ.
	if u.overflowed() {
		u.flushOverflow()
	}
	//: every completion there.
	for u.ready() > 0 {
		lp.reapCQ(lp.onCQE)
	}
	lp.endBatch()
	//: the receive ended (ENOBUFS, an error): armed again over the buffers just returned.
	if !lp.has(laneArmed) && !lp.has(laneHanding) {
		lp.armRecv()
	}
	//: the wake's poll ended.
	if !lp.has(laneWoke) {
		lp.armWake()
	}
	u.submit()
}

// endBatch calls OnBatchEnd after a batch that handed a datagram on, queues
// its replies, returns its buffers and publishes its counts.
func (lp *packetLane) endBatch() {
	//: the handler's flush point.
	if lp.handed > 0 {
		lp.handed = 0
		lp.cfg.Handler.OnBatchEnd(lp)
	}
	lp.flush()
	lp.forget()
	//: each buffer used, back to the kernel, once.
	for _, bid := range lp.recycle {
		lp.u.addBuf(bid)
	}
	lp.recycle = lp.recycle[:0]
	lp.u.publishBufs()
	lp.publish()
}

// complete handles one completion.
func (lp *packetLane) complete(c *cqe) {
	//: by kind.
	switch c.userData & pudKind {
	case pudRecv:
		lp.received(c)
	case pudSend:
		lp.sent(c)
	case pudWake:
		lp.set(laneWoke, c.flags&cqeFMore != 0)
		var b [8]byte
		_, err := syscall.Read(lp.efd, b[:])
		//: EAGAIN: another wake read it.
		swallowErr(err)
	default:
		//: a cancel's own completion, or a probe's late one: nothing to do.
	}
}

// received handles a RECVMSG completion: its buffer's datagrams, handed on.
func (lp *packetLane) received(c *cqe) {
	//: the multishot ended: armed again after the batch, unless handing over.
	if c.flags&cqeFMore == 0 {
		lp.set(laneArmed, false)
	}
	//: an error or no buffer: nothing delivered (ENOBUFS re-arms).
	if c.res < 0 || c.flags&cqeFBuffer == 0 {
		//: nothing.
		return
	}
	bid := uint16(c.flags >> cqeBufferShift)
	lp.recycle = append(lp.recycle, bid)
	name, ctrl, payload, flags := splitRecvmsgOut(lp.u.buf(bid)[:c.res])
	lp.deliver(name, ctrl, payload, flags)
}

// splitRecvmsgOut reads one multishot RECVMSG buffer: its name, its control
// data, its payload and the message's flags.
func splitRecvmsgOut(b []byte) (name, ctrl, payload []byte, flags uint32) {
	//: shorter than its own header: nothing the kernel laid out.
	if len(b) < recvmsgOutBytes+packetNameRoom+packetCtrlRoom {
		//: a cut message.
		return nil, nil, nil, msgCtrunc
	}
	namelen := int(binary.LittleEndian.Uint32(b[0:4]))
	ctrllen := int(binary.LittleEndian.Uint32(b[4:8]))
	flags = binary.LittleEndian.Uint32(b[12:16])
	n0 := recvmsgOutBytes
	c0 := n0 + packetNameRoom
	p0 := c0 + packetCtrlRoom
	//: each part, within its room.
	return b[n0 : n0+min(namelen, packetNameRoom)], b[c0 : c0+min(ctrllen, packetCtrlRoom)], b[p0:], flags
}

// deliver hands each datagram of one message to the handler: a coalesced
// one split at UDP GRO's segment size, a cut one counted and dropped, a
// datagram past the ceiling counted and dropped.
func (lp *packetLane) deliver(name, ctrl, payload []byte, flags uint32) {
	seg := lp.control(ctrl)
	//: the kernel cut the control data, or a coalesced train: nothing whole.
	if flags&msgCtrunc != 0 || (flags&msgTrunc != 0 && seg > 0) {
		lp.truncated++
		//: dropped.
		return
	}
	//: one datagram past the buffer: past every ceiling.
	if flags&msgTrunc != 0 {
		lp.total++
		lp.oversized++
		//: dropped.
		return
	}
	from := sockaddrPort(name)
	lp.remember(from, lp.cur)
	//: one datagram, or the segments of a train.
	if seg <= 0 || seg >= len(payload) {
		lp.one(from, payload)
		//: served.
		return
	}
	//: segment by segment, the last possibly shorter.
	for off := 0; off < len(payload); off += seg {
		lp.one(from, payload[off:min(off+seg, len(payload))])
	}
}

// one hands one datagram on, or counts it past the ceiling.
func (lp *packetLane) one(from netip.AddrPort, d []byte) {
	lp.total++
	//: past the group's ceiling: dropped.
	if len(d) > lp.cfg.Ceiling {
		lp.oversized++
		//: dropped.
		return
	}
	lp.handed++
	lp.cfg.Handler.OnPacket(lp, from, d)
}

// control walks a message's control data: UDP GRO's segment size, returned,
// SO_RXQ_OVFL's count, added to the kernel's drops, and the destination the
// message reached, into lp.cur.
func (lp *packetLane) control(ctrl []byte) int {
	seg := 0
	lp.cur = replySource{}
	//: one cmsghdr at a time, aligned to eight.
	for len(ctrl) >= cmsgHdrBytes {
		clen := int(binary.LittleEndian.Uint64(ctrl[0:8]))
		//: a length the buffer does not hold: the walk ends.
		if clen < cmsgHdrBytes || clen > len(ctrl) {
			break
		}
		seg = lp.cmsg(ctrl[:clen], seg)
		ctrl = ctrl[min((clen+cmsgAlign-1)&^(cmsgAlign-1), len(ctrl)):]
	}
	//: zero without GRO.
	return seg
}

// cmsg reads one control message: UDP_GRO's segment size, returned in place
// of seg, or SO_RXQ_OVFL's count, whose growth joins the drops.
func (lp *packetLane) cmsg(m []byte, seg int) int {
	level := int(int32(binary.LittleEndian.Uint32(m[8:12])))
	typ := int(int32(binary.LittleEndian.Uint32(m[12:16])))
	data := m[cmsgHdrBytes:]
	//: the destination the datagram reached (#972).
	if src, ok := pktinfoSource(level, typ, data); ok {
		lp.cur = src
		//: unchanged.
		return seg
	}
	//: both others carry a 32-bit integer.
	if len(data) < cmsgIntBytes {
		//: unread.
		return seg
	}
	v := binary.LittleEndian.Uint32(data)
	//: by kind.
	switch {
	case level == solUDP && typ == udpGRO:
		//: the train's segment size.
		return int(int32(v))
	case level == syscall.SOL_SOCKET && typ == soRxqOvfl:
		lp.drops += uint64(v - lp.lastOvfl)
		lp.lastOvfl = v
	default:
		//: another message: not this lane's.
	}
	//: unchanged.
	return seg
}

// sockaddrPort reads a sockaddr_in or sockaddr_in6 into an AddrPort; the
// zero AddrPort for anything else.
func sockaddrPort(name []byte) netip.AddrPort {
	//: too short for a family.
	if len(name) < 2 {
		//: none.
		return netip.AddrPort{}
	}
	family := binary.LittleEndian.Uint16(name[0:2])
	//: by family.
	switch {
	case family == afInet && len(name) >= 8:
		port := binary.BigEndian.Uint16(name[2:4])
		//: IPv4.
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(name[4:8])), port)
	case family == afInet6 && len(name) >= 24:
		port := binary.BigEndian.Uint16(name[2:4])
		//: IPv6, a mapped IPv4 address as the socket saw it.
		return netip.AddrPortFrom(netip.AddrFrom16([16]byte(name[8:24])), port)
	default:
		//: none.
		return netip.AddrPort{}
	}
}

// Reply implements corenet.PacketLoopIO: p copied into the reply arena, sent
// after the batch, from the destination to reached in it (#972, flush).
func (lp *packetLane) Reply(to netip.AddrPort, p []byte) error {
	//: no record or no room: refused, nothing copied.
	if lp.recNext+len(lp.pend) >= packetReplies || int(lp.outOff)+len(p) > len(lp.out) {
		//: the handler decides.
		return corenet.LoopOutputFull
	}
	copy(lp.out[lp.outOff:], p)
	lp.pend = append(lp.pend, pendingReply{to: to, off: lp.outOff, n: uint32(len(p))})
	lp.outOff += uint32(len(p))
	//: queued.
	return nil
}

// Loop implements corenet.PacketLoopIO: the lane's index.
func (lp *packetLane) Loop() int {
	//: fixed at Start.
	return lp.cfg.Lane
}

// flush turns the queued replies into SENDMSG SQEs: a run to one peer of one
// size — the last possibly shorter — as one UDP_SEGMENT send, at most
// gsoMaxSegments datagrams and gsoMaxBytes of payload (ADR 0192 §D5).
func (lp *packetLane) flush() {
	pend := lp.pend
	//: each reply's source, while the batch's senders are known (#972).
	for i := range pend {
		pend[i].src = lp.sourceFor(pend[i].to)
	}
	//: run by run.
	for i := 0; i < len(pend); {
		j := lp.runEnd(pend, i)
		lp.send(pend[i:j])
		i = j
	}
	lp.pend = lp.pend[:0]
	lp.settle()
}

// runEnd is the end of the UDP_SEGMENT run starting at i.
func (lp *packetLane) runEnd(pend []pendingReply, i int) int {
	size := pend[i].n
	//: GSO off, or a zero-length datagram: alone.
	if lp.has(laneGSOOff) || size == 0 {
		//: one.
		return i + 1
	}
	total := int(size)
	j := i + 1
	//: as long as the next one extends the run.
	for ; j < len(pend) && j-i < gsoMaxSegments && extendsRun(pend[i], pend[j-1], pend[j], total); j++ {
		total += int(pend[j].n)
	}
	//: past the run's last.
	return j
}

// extendsRun reports whether r, after prev, extends the run first opened: one
// peer and one source, contiguous bytes, every datagram but the last of the run's size, the
// payload within gsoMaxBytes.
func extendsRun(first, prev, r pendingReply, total int) bool {
	//: each condition of ADR 0192 §D5.
	return r.to == first.to && r.src == first.src && r.off == prev.off+prev.n && prev.n == first.n && r.n <= first.n && total+int(r.n) <= gsoMaxBytes
}

// send writes one SENDMSG record for run and queues its SQE; a peer the
// socket cannot address, or a ring that takes nothing, counts the run as
// failed.
func (lp *packetLane) send(run []pendingReply) {
	k := lp.recNext
	r := lp.rec[k*packetRecordBytes : (k+1)*packetRecordBytes : (k+1)*packetRecordBytes]
	clear(r)
	nameLen := encodeSockaddr(r[72:104], run[0].to, lp.family)
	s := lp.u.get()
	//: unaddressable, or no SQE: failed.
	if nameLen == 0 || s == nil {
		lp.failures += uint64(len(run))
		//: counted.
		return
	}
	last := run[len(run)-1]
	hdr := (*syscall.Msghdr)(unsafe.Pointer(&r[0]))
	iov := (*syscall.Iovec)(unsafe.Pointer(&r[56]))
	iov.Base = &lp.out[run[0].off]
	iov.Len = uint64(last.off + last.n - run[0].off)
	//: a zero-length datagram names no byte.
	if iov.Len == 0 {
		iov.Base = nil
	}
	hdr.Name = &r[72]
	hdr.Namelen = nameLen
	hdr.Iov = iov
	hdr.Iovlen = 1
	ctl := r[recordControlOff:]
	used, pinned, gso := lp.writeControl(ctl, run)
	//: any control data.
	if used > 0 {
		hdr.Control = &ctl[0]
		hdr.Controllen = uint64(used)
	}
	prepSendmsg(s, lp.cfg.FD, unsafe.Pointer(hdr), pudSend|uint64(k))
	lp.segs[k], lp.gso[k], lp.pinned[k] = uint16(len(run)), gso, pinned
	lp.recNext++
	lp.inflight++
}

// writeControl writes run's control data into ctl and returns its length,
// whether it names a source and whether it is a UDP_SEGMENT send: the source
// first (#972), so a refused one is cut off the front (unpin), then the
// segment size of a run.
func (lp *packetLane) writeControl(ctl []byte, run []pendingReply) (used int, pinned, gso bool) {
	used = writePktinfo(ctl, lp.family, run[0].src)
	pinned = used > 0
	gso = len(run) > 1
	//: a run: the segment size in a UDP_SEGMENT cmsg.
	if gso {
		c := ctl[used : used+int(segmentCmsgBytes)]
		binary.LittleEndian.PutUint64(c[0:8], uint64(cmsgHdrBytes+2))
		binary.LittleEndian.PutUint32(c[8:12], uint32(solUDP))
		binary.LittleEndian.PutUint32(c[12:16], uint32(udpSegment))
		binary.LittleEndian.PutUint16(c[16:18], uint16(run[0].n))
		used += int(segmentCmsgBytes)
		lp.gsoSends.Add(1)
	}
	//: the length, and what it holds.
	return used, pinned, gso
}

// encodeSockaddr writes to as the socket's family addresses it into b and
// returns its length, zero when the family cannot address it.
func encodeSockaddr(b []byte, to netip.AddrPort, family uint16) uint32 {
	a := to.Addr()
	//: by the socket's family.
	switch {
	case !to.IsValid():
		//: nobody.
		return 0
	case family == afInet && a.Unmap().Is4():
		binary.LittleEndian.PutUint16(b[0:2], afInet)
		binary.BigEndian.PutUint16(b[2:4], to.Port())
		v4 := a.Unmap().As4()
		copy(b[4:8], v4[:])
		//: sockaddr_in.
		return sockaddrIn4Bytes
	case family == afInet6:
		binary.LittleEndian.PutUint16(b[0:2], afInet6)
		binary.BigEndian.PutUint16(b[2:4], to.Port())
		v6 := a.As16()
		copy(b[8:24], v6[:])
		//: sockaddr_in6, an IPv4 peer mapped.
		return sockaddrIn6Bytes
	default:
		//: an IPv6 peer on an IPv4 socket.
		return 0
	}
}

// sent handles a SENDMSG completion: a refusal counted, GSO switched off
// on EIO, and the arena reset once nothing is in flight.
func (lp *packetLane) sent(c *cqe) {
	k := int(c.userData &^ pudKind)
	//: refused by the kernel; a refused source sent again stays in flight.
	if c.res < 0 && lp.refusedSend(k, syscall.Errno(-c.res)) {
		//: still in flight.
		return
	}
	lp.inflight--
	lp.settle()
}

// refusedSend handles record k's refusal: a source the kernel will not send
// from sent again from its own choice — reported true, still in flight —,
// anything else counted, GSO switched off on EIO.
func (lp *packetLane) refusedSend(k int, errno syscall.Errno) bool {
	//: a source the kernel will not send from: again, from its own choice.
	if errno == syscall.EINVAL && lp.pinned[k] && lp.unpin(k) {
		//: resent.
		return true
	}
	lp.failures += uint64(lp.segs[k])
	//: a UDP_SEGMENT send's own refusals.
	if lp.gso[k] {
		lp.gsoRefused(errno)
	}
	//: counted.
	return false
}

// gsoRefused handles a UDP_SEGMENT send's refusal: EIO, a device without
// checksum offload, sends one datagram a message from now on; EMSGSIZE, a
// sizing bug, is counted and GSO kept.
func (lp *packetLane) gsoRefused(errno syscall.Errno) {
	//: by the kernel's answer.
	switch {
	case errno == syscall.EIO && !lp.has(laneGSOOff):
		lp.set(laneGSOOff, true)
		//: told once.
		if lp.cfg.OnGSOOff != nil {
			lp.cfg.OnGSOOff()
		}
	case errno == syscall.EMSGSIZE:
		lp.gsoOver++
	default:
		//: counted as failed only.
	}
}

// settle resets the reply arena once no reply is queued or in flight.
func (lp *packetLane) settle() {
	//: something still uses it.
	if lp.inflight > 0 || len(lp.pend) > 0 {
		//: kept.
		return
	}
	lp.recNext, lp.outOff = 0, 0
}

// publish adds the batch's counts to server's counters, each only when it
// moved: an atomic add is a barrier.
func (lp *packetLane) publish() {
	c := &lp.cfg.Counters
	addMoved(c.Total, &lp.total)
	addMoved(c.Oversized, &lp.oversized)
	addMoved(c.Truncated, &lp.truncated)
	addMoved(c.ReplyFailures, &lp.failures)
	addMoved(c.GSOOversized, &lp.gsoOver)
	addMoved(c.KernelDrops, &lp.drops)
}

// addMoved adds *n to a and zeroes it, only when it moved.
func addMoved(a *atomic.Uint64, n *uint64) {
	//: moved.
	if *n != 0 {
		a.Add(*n)
		*n = 0
	}
}

// beginHandover cancels the socket's receive; the batches go on until its
// final completion.
func (lp *packetLane) beginHandover() {
	lp.set(laneHanding, true)
	//: nothing armed: the drain starts at once.
	if !lp.has(laneArmed) {
		//: nothing to cancel.
		return
	}
	s := lp.u.get()
	//: no SQE: the receive is left to the ring's close, the drain reads the rest.
	if s == nil {
		lp.set(laneArmed, false)
		//: given up.
		return
	}
	prepCancelUserData(s, pudRecv, pudCancel)
}

// finishHandover reads what the socket still holds to EAGAIN, serves it,
// sends every reply, and switches UDP GRO off: nothing coalesced is left to
// a successor that may not split it (ADR 0192 §D4).
func (lp *packetLane) finishHandover() {
	//: bounded, and cut short by a stop.
	for range packetDrainMax {
		//: Stop, EAGAIN, or the socket failed: the rest stays in the socket.
		if lp.state.Load() == laneStop || !lp.readOne() {
			break
		}
	}
	lp.endBatch()
	lp.u.submit()
	//: the replies' completions, bounded.
	for i := 0; lp.inflight > 0 && i < packetSendWaits && lp.state.Load() != laneStop; i++ {
		lp.u.wait(&lp.tick, 1)
		lp.reapCQ(lp.onCQE)
	}
	lp.publish()
	swallowErr(syscall.SetsockoptInt(lp.cfg.FD, solUDP, udpGRO, 0))
}

// quiesce ends, before the ring closes, the requests that hold the socket's
// file: the armed receive is cancelled and the replies in flight complete,
// reaped within packetSendWaits waits of packetQuiesceWait. A request left to
// the ring's close keeps the file until the kernel's exit work runs it down,
// after Close has returned, and the port stays bound until then: a bind of
// the same address right after Close met EADDRINUSE (#1336's CI,
// TestPacketLoop_closeStopsLanes). Nothing is handed to the handler: the
// receive's last datagrams stay in its buffers, the lane stopping.
func (lp *packetLane) quiesce() {
	lp.cancelRecv()
	lp.u.submit()
	ts := kernelTimespec{nsec: packetQuiesceWait}
	//: until nothing holds the socket, bounded.
	for i := 0; (lp.has(laneArmed) || lp.inflight > 0) && i < packetSendWaits; i++ {
		lp.u.wait(&ts, 1)
		lp.reapCQ(lp.quiesceCQE)
	}
}

// cancelRecv queues the cancel of the socket's receive, when one is armed
// and the SQ has room — it has, once a batch ended.
func (lp *packetLane) cancelRecv() {
	//: nothing armed.
	if !lp.has(laneArmed) {
		//: nothing to cancel.
		return
	}
	//: an SQE.
	if s := lp.u.get(); s != nil {
		prepCancelUserData(s, pudRecv, pudCancel)
	}
}

// quiesceCQE is quiesce's completion handler: the receive's last completion
// disarms it, a send's leaves the flight; nothing is handed on, nothing sent
// again.
func (lp *packetLane) quiesceCQE(c *cqe) {
	//: by kind.
	switch c.userData & pudKind {
	case pudRecv:
		//: the receive's last completion.
		if c.flags&cqeFMore == 0 {
			lp.set(laneArmed, false)
		}
	case pudSend:
		lp.inflight--
	}
}

// reapCQ reaps every completion the lane's CQ holds through fn.
func (lp *packetLane) reapCQ(fn func(c *cqe)) {
	lp.u.reap(int(lp.u.cqLen), fn)
}

// readOne reads one message with recvmsg(2) into the handover's buffer and
// delivers it; false at EAGAIN or on an error.
func (lp *packetLane) readOne() bool {
	b := lp.drainBuf
	n0 := recvmsgOutBytes
	c0 := n0 + packetNameRoom
	p0 := c0 + packetCtrlRoom
	var iov syscall.Iovec
	iov.Base = &b[p0]
	iov.SetLen(len(b) - p0)
	hdr := syscall.Msghdr{Name: &b[n0], Namelen: uint32(packetNameRoom), Iov: &iov, Iovlen: 1, Control: &b[c0]}
	hdr.SetControllen(packetCtrlRoom)
	//: EINTR again.
	for {
		n, _, e := syscall.Syscall(syscall.SYS_RECVMSG, uintptr(lp.cfg.FD), uintptr(unsafe.Pointer(&hdr)), syscall.MSG_DONTWAIT)
		//: by outcome.
		switch e {
		case 0:
			lp.deliver(b[n0:n0+min(int(hdr.Namelen), packetNameRoom)], b[c0:c0+min(int(hdr.Controllen), packetCtrlRoom)], b[p0:p0+min(int(n), len(b)-p0)], uint32(hdr.Flags))
			//: one message.
			return true
		case syscall.EINTR:
			continue
		default:
			//: EAGAIN: drained.
			return false
		}
	}
}

// release closes the ring, the eventfd and the socket, and unmaps the
// arenas, once quiesce ended the requests that hold the socket.
func (lp *packetLane) release() {
	//: the requests on the socket end first; the ring's close cancels the
	//: rest.
	if lp.u != nil {
		lp.quiesce()
		lp.u.close()
	}
	lp.wakeMu.Lock()
	//: the wake, never written once closed: its number may be reused.
	if lp.efd >= 0 {
		swallowErr(syscall.Close(lp.efd))
		lp.efd = -1
	}
	lp.wakeMu.Unlock()
	//: the arenas, one mapping.
	if lp.arena != nil {
		swallowErr(syscall.Munmap(lp.arena))
	}
	swallowErr(syscall.Close(lp.cfg.FD))
}
