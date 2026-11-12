//go:build linux

package server

import (
	"cmp"
	"errors"
	stdnet "net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	// udpGRO is UDP_GRO, the socket option that lets the kernel hand a reader
	// several datagrams of one flow as one message, and the type of the control
	// message carrying their size (include/uapi/linux/udp.h; Linux 5.0). The
	// standard library's syscall package does not define it.
	udpGRO = 104
	// groMessageSize is the largest message a GRO read can return: a coalesced
	// message is bounded by the 16-bit IP length it would be sent with, so a
	// slot this large never cuts one (MSG_TRUNC) for want of room.
	groMessageSize int = 65535
	// groSegmentBytes is the size of the UDP_GRO control message's payload: the
	// segment size, a C int.
	groSegmentBytes int = 4
	// soRxqOvfl is SO_RXQ_OVFL, the socket option that has the kernel attach to
	// each message read the number of datagrams it dropped for want of room in
	// the receive buffer, as a SOL_SOCKET control message of the same type
	// (include/uapi/asm-generic/socket.h; Linux 2.6.33).
	soRxqOvfl = 40
	// rxqBytes is the size of that message's payload: a 32-bit count.
	rxqBytes int = 4
	// groControlSpace is the control room a GRO read gives each message: the
	// UDP_GRO message's 24 bytes, SO_RXQ_OVFL's 24 (#973), and room beside them
	// for those a caller may have asked of the socket itself — IP_PKTINFO (32),
	// IPV6_PKTINFO (40), a timestamp (32 to 64) —, so a foreign control message
	// never cuts the one the reader needs (MSG_CTRUNC). 64 slots of it are 16 KiB.
	groControlSpace int = 256
)

// multiReader reads several datagrams per syscall using recvmmsg(2).
//
// It reuses one message array for the life of the socket: the iovecs, name and
// control buffers are wired to the slot buffers once, so a read costs no
// allocation at all — which is the entire reason for the batched path. The
// kernel writes back the lengths it used into the same headers, so every
// capacity is restored before each call (prepare), never only at wiring.
type multiReader struct {
	// pc is the socket, kept for the fallback path and for addresses.
	pc stdnet.PacketConn
	// raw exposes the descriptor under the runtime's netpoller.
	raw syscall.RawConn
	// headers is the reusable mmsghdr array handed to the kernel.
	headers []mmsghdr
	// iovecs points each header at its slot's buffer.
	iovecs []syscall.Iovec
	// names receives each datagram's sender address.
	names []syscall.RawSockaddrAny
	// oob receives each message's control data, controlSpace bytes a slot.
	oob []byte
	// controlSpace is the room one message's control data has: groControlSpace
	// under GRO, the one case a read asks for a control message, and none
	// without it — a control message the caller asked of the socket is then
	// not read, and its MSG_CTRUNC means nothing to the reader.
	controlSpace int
	// rxq records that SO_RXQ_OVFL was switched on at construction, so each
	// message carries the kernel's drop count and controlSpace holds its room.
	rxq bool
	// drops is the shared counter the kernel's drops are added to, by what each
	// message's count grew since the last: the kernel's counter is 32 bits and
	// wraps, and this one does not. lastOvfl is the last count this socket saw.
	drops    *atomic.Uint64
	lastOvfl uint32
	// dstFamily is the family of the destination message the socket asked for
	// (#972), AF_INET or AF_INET6, zero when it asked for none: fill reads the
	// message and controlSpace holds its room.
	dstFamily int
	// wired records the slot count the arrays were built for.
	wired int
	// gro records that UDP_GRO was switched on at construction, so slots must
	// hold a whole coalesced message.
	gro bool
	// handing is set by handover: the next reads drain what the socket holds
	// without waiting, then end the loop.
	handing atomic.Bool
	// stopping is set by stopGRO before it switches GRO off: a read that
	// returns from then on may hold a coalesced message without its segment
	// size, so it is dropped and the loop ended — the socket is being closed.
	stopping atomic.Bool
	// received and errno are what the last recvmmsg returned. They are fields,
	// and recv and drainOnce the functions the descriptor runs, bound once at
	// wiring, so a read moves nothing to the heap.
	received  int
	errno     syscall.Errno
	recv      func(fd uintptr) bool
	drainOnce func(fd uintptr)
}

// newMultiReader builds a batched reader over pc.
func newMultiReader(pc stdnet.PacketConn, sc rawConnProvider) (reader *multiReader, err error) {
	raw, rerr := sc.SyscallConn()
	//: without the raw descriptor there is nothing to batch on.
	if rerr != nil {
		//: without the descriptor there is nothing to batch on.
		return nil, rerr
	}
	//: the arrays are sized lazily, on the first read, when the slot count is
	//: known, and the control room once GRO is decided.
	return &multiReader{pc: pc, raw: raw}, nil
}

// enableGRO switches UDP_GRO on and reports whether the kernel took it: a
// socket that is not UDP, or a kernel before 5.0, refuses, and the reader then
// reads one datagram a message as before. It runs before the first read, so the
// slots are sized for what it decided.
func (r *multiReader) enableGRO() bool {
	r.gro = r.setGRO(true) == nil
	//: whatever the kernel decided.
	return r.gro
}

// enableRxqOvfl switches SO_RXQ_OVFL on and reports whether the kernel took it.
// From then on every message read carries the socket's drop count, which fill
// adds to drops. It runs before the first read, so the control room is sized
// for it.
func (r *multiReader) enableRxqOvfl(drops *atomic.Uint64) error {
	var serr error
	cerr := r.raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soRxqOvfl, 1)
	})
	//: the socket's failure first, then the kernel's answer.
	if err := cmp.Or(cerr, serr); err != nil {
		return err
	}
	r.rxq, r.drops = true, drops
	return nil
}

// setGRO sets UDP_GRO on the socket.
func (r *multiReader) setGRO(on bool) error {
	value := 0
	//: the option is a C int boolean.
	if on {
		value = 1
	}
	var serr error
	cerr := r.raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpGRO, value)
	})
	//: the socket's failure first, then the kernel's answer.
	return cmp.Or(cerr, serr)
}

// handover prepares the socket for whoever reads it after this loop — a
// successor that inherits it, which may not split coalesced messages: the loop
// drains what the socket holds without waiting, GRO still on, until EAGAIN,
// then switches GRO off and ends (ADR 0192). The order is the kernel's:
// udp_recvmsg writes the UDP_GRO control message only while the option is on,
// so a message coalesced before GRO went off and read after it would arrive
// without its segment size, one datagram the length of all of them. It reports
// whether a drain was started; a reader that never had GRO on has nothing to
// hand over.
func (r *multiReader) handover() bool {
	//: nothing was ever coalesced on this socket.
	if !r.gro {
		//: no drain to wait for.
		return false
	}
	r.handing.Store(true)
	//: a read parked on the netpoller returns at once on an expired deadline,
	//: and the next read drains instead of waiting.
	swallowErr(r.pc.SetReadDeadline(time.Unix(1, 0)))
	//: the read loop drains and ends.
	return true
}

// stopGRO switches GRO off without a drain: Close's, which waits for nothing,
// so a successor at least finds nothing coalesced from here on.
func (r *multiReader) stopGRO() {
	//: only a socket that coalesced has GRO to switch off.
	if r.gro {
		r.switchGROOff()
	}
}

// switchGROOff marks the reader stopping, then switches GRO off: in that
// order, so no read completing during the switch is split wrongly.
func (r *multiReader) switchGROOff() {
	r.stopping.Store(true)
	swallowErr(r.setGRO(false))
}

// slotSize implements datagramSource: a whole coalesced message under GRO, the
// ceiling otherwise. Under GRO a socket's read batch therefore holds BatchSize
// slots of 64 KiB: 1 MiB at the default 16, 64 MiB at a BatchSize of 1024,
// allocated once for the socket's life (ADR 0192 §D2).
func (r *multiReader) slotSize(ceiling int) int {
	//: one datagram a message: the probe byte past the ceiling is enough.
	if !r.gro {
		//: the ceiling, as before GRO.
		return ceiling
	}
	//: a coalesced message must fit whole, or it is cut and dropped.
	return max(ceiling, groMessageSize)
}

// wire points the message array at the caller's slots. It runs once per slot
// count, not once per read.
func (r *multiReader) wire(slots []datagram) {
	//: already wired for this batch size — nothing to redo.
	if r.wired == len(slots) {
		//: already wired for this batch size.
		return
	}
	count := len(slots)
	//: GRO's room, unless a test set its own; the drop count's and the
	//: destination's alone without GRO, none with neither.
	if r.controlSpace <= 0 {
		r.controlSpace = r.neededControl()
	}
	//: all four are indexed in lockstep with slots, never appended to.
	r.headers = make([]mmsghdr, count)
	r.iovecs = make([]syscall.Iovec, count)
	r.names = make([]syscall.RawSockaddrAny, count)
	r.oob = make([]byte, count*r.controlSpace)
	//: each header borrows its slot's buffer for the socket's whole lifetime.
	for i := range slots {
		r.iovecs[i].Base = &slots[i].buf[0]
		setKernelLen(&r.iovecs[i].Len, len(slots[i].buf))
		r.headers[i].hdr.Name = (*byte)(unsafe.Pointer(&r.names[i]))
		r.headers[i].hdr.Iov = &r.iovecs[i]
		setKernelLen(&r.headers[i].hdr.Iovlen, 1)
		//: no control room: the kernel writes no control data.
		if r.controlSpace > 0 {
			r.headers[i].hdr.Control = &r.oob[i*r.controlSpace]
		}
	}
	r.bind()
	r.wired = count
	r.prepare()
}

// neededControl is the control room one message needs: GRO's, which holds the
// rest beside it, or the drop count's and the destination's alone.
func (r *multiReader) neededControl() int {
	//: GRO's room holds every other control message too.
	if r.gro {
		//: 256 bytes a message.
		return groControlSpace
	}
	space := 0
	//: the kernel's drop count, when it was asked for.
	if r.rxq {
		space += syscall.CmsgSpace(rxqBytes)
	}
	//: the destination, in the larger of its two forms.
	if r.dstFamily != 0 {
		space += syscall.CmsgSpace(in6PktinfoBytes)
	}
	//: what the asked-for messages take.
	return space
}

// bind builds, once, the functions the descriptor runs: recvmmsg parked on the
// netpoller, and the drain's, which never waits. They write into the reader's
// fields, so neither moves a variable to the heap on each read.
func (r *multiReader) bind() {
	//: already bound for this reader.
	if r.recv != nil {
		//: bound.
		return
	}
	//: driving the syscall inside RawConn.Read is what keeps the goroutine
	//: parked on the netpoller: returning false on EAGAIN hands control back to
	//: the runtime exactly as a blocking ReadFrom would.
	r.recv = func(fd uintptr) bool {
		result, _, callErrno := syscall.Syscall6(syscall.SYS_RECVMMSG, fd,
			uintptr(unsafe.Pointer(&r.headers[0])), uintptr(len(r.headers)),
			syscall.MSG_WAITFORONE, 0, 0)
		//: not ready yet — let the netpoller wake us when it is.
		if callErrno == syscall.EAGAIN {
			//: not ready — hand control back to the netpoller.
			return false
		}
		r.received, r.errno = int(result), callErrno
		//: the syscall completed, successfully or not.
		return true
	}
	r.drainOnce = func(fd uintptr) {
		result, _, callErrno := syscall.Syscall6(syscall.SYS_RECVMMSG, fd,
			uintptr(unsafe.Pointer(&r.headers[0])), uintptr(len(r.headers)),
			syscall.MSG_DONTWAIT, 0, 0)
		r.received, r.errno = int(result), callErrno
	}
}

// prepare restores every header's capacities before a call: the kernel wrote
// back into Namelen and Controllen the lengths the last message used, and a
// message that carried no control data left Controllen at zero, so the next
// coalesced message would find no room for its segment size (MSG_CTRUNC) and
// be dropped.
func (r *multiReader) prepare() {
	//: every header, whether or not the last call filled it.
	for i := range r.headers {
		r.headers[i].hdr.Namelen = syscall.SizeofSockaddrAny
		setKernelLen(&r.headers[i].hdr.Controllen, r.controlSpace)
		r.headers[i].hdr.Flags = 0
	}
}

// readBatch implements datagramSource using one recvmmsg call.
func (r *multiReader) readBatch(slots []datagram) (n int, err error) {
	r.wire(slots)
	//: handed over: drain what is queued, never wait.
	if r.handing.Load() {
		//: the drain's own read.
		return r.drain(slots)
	}
	r.prepare()
	r.received, r.errno = 0, 0
	ctrlErr := r.raw.Read(r.recv)
	received, errno := r.received, r.errno
	//: handover's deadline woke the parked read: drain instead.
	if ctrlErr != nil && r.handing.Load() && errors.Is(ctrlErr, os.ErrDeadlineExceeded) {
		//: the drain's own read.
		return r.drain(slots)
	}
	//: the socket was closed, or the runtime refused the operation.
	if ctrlErr != nil {
		//: the socket was closed, or the runtime refused the operation.
		return 0, ctrlErr
	}
	//: a syscall error is reported as itself so the loop can classify it.
	if errno != 0 {
		//: report the raw errno so the loop can classify it.
		return 0, errno
	}
	//: GRO was being switched off under the read: its messages may have lost
	//: their segment size, and the socket is closing.
	if r.stopping.Load() {
		//: dropped; the loop ends as on a closed socket.
		return 0, stdnet.ErrClosed
	}
	r.fill(slots, received)
	//: every filled slot is ready for the handler.
	return received, nil
}

// drain reads what the socket holds without waiting. EAGAIN means the queue is
// empty: the socket is handed over, and the loop is told so as a closed socket
// is, net.ErrClosed, which ends it.
func (r *multiReader) drain(slots []datagram) (n int, err error) {
	r.prepare()
	r.received, r.errno = 0, 0
	ctrlErr := r.raw.Control(r.drainOnce)
	received, errno := r.received, r.errno
	//: closed under the drain: the budget ran out first.
	if ctrlErr != nil {
		//: the loop ends on it.
		return 0, ctrlErr
	}
	//: nothing left: GRO off, so nothing is coalesced for the successor, who
	//: reads from here.
	if errno == syscall.EAGAIN {
		swallowErr(r.setGRO(false))
		//: ends the loop exactly as a closed socket does.
		return 0, stdnet.ErrClosed
	}
	//: any other failure is the loop's to classify, as on a normal read.
	if errno != 0 {
		//: the raw errno.
		return 0, errno
	}
	//: the handover's budget ran out and GRO was switched off under the
	//: drain: its messages may have lost their segment size.
	if r.stopping.Load() {
		//: dropped; the loop ends as on a closed socket.
		return 0, stdnet.ErrClosed
	}
	r.fill(slots, received)
	//: the drained messages, served like any others.
	return received, nil
}

// fill copies the per-message results back into the caller's slots: the length,
// the sender, the segment size a coalesced message carries, and whether the
// kernel cut the message (msg_flags).
//
// Without GRO the reader asks for no control data and reads none: a control
// message the caller asked of the socket — IP_PKTINFO, a timestamp — finds no
// room and the kernel sets MSG_CTRUNC, which costs this reader nothing, so it
// never cuts a message. Under GRO a message is cut for MSG_CTRUNC only when the
// UDP_GRO message itself did not survive: its segment size may then be lost.
func (r *multiReader) fill(slots []datagram, received int) {
	//: only the slots the kernel actually filled carry meaningful data.
	for i := range received {
		hdr := &r.headers[i].hdr
		slot := &slots[i]
		slot.n = int(r.headers[i].length)
		slot.seg = 0
		slot.local = localAddress{}
		//: the control messages, read as the kernel laid them out.
		if r.controlSpace > 0 {
			r.readControl(i, slot)
		}
		//: control data cut short before the segment size: the message
		//: cannot be split, nothing more to read from it.
		slot.cut = r.gro && slot.seg == 0 && hdr.Flags&syscall.MSG_CTRUNC != 0
		//: cut short: no sender to hand on.
		if slot.cut {
			slot.addr = nil
			continue
		}
		//: a message past its buffer is cut. Without GRO it is one datagram past
		//: the ceiling, which the probe byte already shows; with it, it may be
		//: several whole datagrams and a part, which cannot be told apart.
		slot.cut = hdr.Flags&syscall.MSG_TRUNC != 0 && (r.gro || slot.seg > 0)
		slot.addr = sockaddrToAddr(&r.names[i])
	}
}

// readControl reads message i's control data: GRO's segment size into slot, and
// the kernel's drop count when it was asked for.
func (r *multiReader) readControl(i int, slot *datagram) {
	used := min(int(r.headers[i].hdr.Controllen), r.controlSpace)
	control := r.oob[i*r.controlSpace : i*r.controlSpace+used]
	slot.seg = groSegment(control)
	//: the drop count, when it was asked for.
	if r.rxq {
		r.countDrops(control)
	}
	//: the datagram's own destination, when it was asked for.
	if r.dstFamily != 0 {
		slot.local = pktinfoOf(control, r.dstFamily)
	}
}

// countDrops adds to the shared counter what the kernel's drop count grew by
// since the last message of this socket, read from control.
func (r *multiReader) countDrops(control []byte) {
	count, ok := rxqOverflow(control)
	//: a message with no count: nothing new is known.
	if !ok {
		return
	}
	//: modular, so the kernel's 32-bit wrap costs nothing.
	r.drops.Add(uint64(count - r.lastOvfl))
	r.lastOvfl = count
}

// rxqOverflow returns the SO_RXQ_OVFL count control carries, and whether it
// carries one.
func rxqOverflow(control []byte) (count uint32, ok bool) {
	payload := controlMessage(control, syscall.SOL_SOCKET, soRxqOvfl, rxqBytes)
	//: no count on this message.
	if payload == nil {
		//: nothing new is known.
		return 0, false
	}
	//: the count, a u32 in host order.
	return *(*uint32)(unsafe.Pointer(&payload[0])), true
}

// controlMessage returns the payload of the first control message of level and
// typ at least size bytes long, or nil. The buffer is walked as the kernel laid
// it out, never assumed: a length that does not cover its own header, or runs
// past the buffer, ends the walk.
func controlMessage(control []byte, level, typ int32, size int) []byte {
	header := syscall.CmsgLen(0)
	//: every message of the buffer, until one matches.
	for off := 0; off+header <= len(control); {
		cmsg := (*syscall.Cmsghdr)(unsafe.Pointer(&control[off]))
		length := int(cmsg.Len)
		//: malformed: the walk ends rather than reading past the buffer.
		if length < header || off+length > len(control) {
			//: none found.
			return nil
		}
		//: the message asked for, large enough to read.
		if cmsg.Level == level && cmsg.Type == typ && length >= header+size {
			//: its payload.
			return control[off+header : off+length]
		}
		off += syscall.CmsgSpace(length - header)
	}
	//: no such message.
	return nil
}

// groSegment returns the segment size a UDP_GRO control message reports in
// control, or zero when it carries none: the message is then one datagram.
func groSegment(control []byte) int {
	payload := controlMessage(control, syscall.IPPROTO_UDP, udpGRO, groSegmentBytes)
	//: no UDP_GRO message: one datagram.
	if payload == nil {
		//: one datagram.
		return 0
	}
	//: the size every datagram of the message has but the last, a C int.
	return int(*(*int32)(unsafe.Pointer(&payload[0])))
}
