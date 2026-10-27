//go:build linux

package server

import (
	"cmp"
	stdnet "net"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	// udpSegment is UDP_SEGMENT: as a socket option it answers whether the
	// kernel has UDP GSO (Linux 4.18), and as a control message on one send it
	// cuts the send into datagrams of its value (include/uapi/linux/udp.h). The
	// standard library's syscall package does not define it.
	udpSegment = 103
	// udpMaxSegments is the most datagrams one GSO send may carry: the kernel's
	// UDP_MAX_SEGMENTS since 4.18, the floor every kernel the SDK runs on keeps.
	udpMaxSegments int = 64
	// udpHeaderBytes is the UDP header every datagram carries.
	udpHeaderBytes int = 8
	// ipv4HeaderBytes is IPv4's header, without options.
	ipv4HeaderBytes int = 20
	// ipv6HeaderBytes is IPv6's fixed header, without extension headers.
	ipv6HeaderBytes int = 40
	// ipMaxLength is the 16-bit IP length a GSO send is built as before the
	// device cuts it: its header, the UDP header and every segment fit in it,
	// or the kernel refuses the send with EMSGSIZE.
	ipMaxLength int = 65535
	// ethernetMTU bounds the segment a reply is coalesced at: a segment past the
	// path's MTU is refused (EINVAL), where the same reply sent alone would be
	// fragmented, so a larger reply leaves alone. A path with a smaller MTU
	// refuses the send, and its replies are sent again one by one.
	ethernetMTU int = 1500
	// segmentSizeBytes is UDP_SEGMENT's control payload: a C uint16.
	segmentSizeBytes int = 2
	// gsoInvalidLimit is how many GSO sends in a row the kernel may refuse with
	// EINVAL before GSO is switched off for the socket: a refusal that no
	// smaller segment cures — SO_NO_CHECK, a device that takes no GSO — would
	// otherwise cost one failed sendmmsg a batch for the socket's life.
	gsoInvalidLimit int = 4
)

// gsoSegments is how many datagrams of size bytes one GSO send of a socket of
// family may carry: the kernel's udpMaxSegments, and the 16-bit IP length the
// send is built as — 54 of the L3 load's datagrams on IPv4 and on IPv6.
func gsoSegments(family, size int) int {
	header := ipv4HeaderBytes
	//: IPv6's fixed header is twice IPv4's.
	if family == syscall.AF_INET6 {
		header = ipv6HeaderBytes
	}
	//: an empty datagram cannot be cut out of a send.
	if size <= 0 {
		//: one at a time.
		return 1
	}
	//: whichever bound comes first.
	return min(udpMaxSegments, (ipMaxLength-header-udpHeaderBytes)/size)
}

// maxSegment is the largest datagram a socket of family coalesces: one that
// fits an Ethernet frame with its headers.
func maxSegment(family int) int {
	//: IPv6's fixed header is twice IPv4's.
	if family == syscall.AF_INET6 {
		//: 1452 bytes.
		return ethernetMTU - ipv6HeaderBytes - udpHeaderBytes
	}
	//: 1472 bytes.
	return ethernetMTU - ipv4HeaderBytes - udpHeaderBytes
}

// pendingReply is one queued reply: where its bytes sit in the arena and the
// peer it goes to.
type pendingReply struct {
	// off and size place the reply's bytes in the arena.
	off, size int
	// name is the peer's sockaddr, sockaddr_in or sockaddr_in6, zeroed past
	// its length so two replies to one peer compare equal.
	name syscall.RawSockaddrInet6
	// namelen is the length of name the kernel reads.
	namelen uint32
}

// addr is the peer e was queued for, as WriteTo takes it: the fallback of a
// socket whose sendmmsg is refused, off the batched path.
func (e *pendingReply) addr() stdnet.Addr {
	//: the IPv4 sockaddr encode wrote.
	if e.name.Family == syscall.AF_INET {
		sa4 := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&e.name))
		//: its address and port.
		return &stdnet.UDPAddr{IP: stdnet.IPv4(sa4.Addr[0], sa4.Addr[1], sa4.Addr[2], sa4.Addr[3]), Port: hostPort(sa4.Port)}
	}
	//: an IPv6 one, unzoned: encode leaves zoned peers to WriteTo.
	ip := make(stdnet.IP, stdnet.IPv6len)
	copy(ip, e.name.Addr[:])
	//: its address and port.
	return &stdnet.UDPAddr{IP: ip, Port: hostPort(e.name.Port)}
}

// replySpan is one message of a send: count consecutive queued replies from
// first, one GSO send when count is more than one.
type replySpan struct {
	// first is the index of the span's first reply in the queue.
	first int
	// count is how many replies it carries.
	count int
}

// multiWriter sends the replies of one read batch with sendmmsg(2), every run of
// consecutive replies to one peer of one size — the last may be shorter — as
// one UDP GSO message (ADR 0192).
//
// Its queue, arena and message arrays are allocated once, at construction, so
// a reply costs no allocation once the loop is running. A mutex guards them: a
// handler that kept its Packet may reply from another goroutine, and such a
// reply, outside any batch, is written at once.
type multiWriter struct {
	// pc is the socket, written directly outside a batch.
	pc stdnet.PacketConn
	// raw exposes the descriptor under the runtime's netpoller.
	raw syscall.RawConn
	// family is the socket's, AF_INET or AF_INET6: it decides the sockaddr a
	// reply is addressed with and the headers a GSO send is sized for.
	family int
	// counters is where failed replies and refused GSO sends are counted.
	counters *datagramCounters
	// onGSOOff is called once GSO is switched off on this socket.
	onGSOOff func()
	// send is sendmmsg; a test swaps it to answer what a device would.
	send func(fd uintptr, headers []mmsghdr) (sent int, errno syscall.Errno)
	// gso is on until the device refuses a GSO send for want of checksum
	// offload (EIO), and stays off for the socket's life.
	gso atomic.Bool
	// mu guards everything below.
	mu sync.Mutex
	// open is true between begin and flush, while replies are queued.
	open bool
	// arena holds the queued replies' bytes, used of it so far.
	arena []byte
	used  int
	// queue is the replies queued in this batch.
	queue []pendingReply
	// headers, iovecs, oob and spans describe one send's messages, indexed in
	// lockstep.
	headers []mmsghdr
	iovecs  []syscall.Iovec
	oob     []byte
	spans   []replySpan
	// controlSpace is CMSG_SPACE of the UDP_SEGMENT size, one message's room.
	controlSpace int
	// plainUntil is the queue index below which replies leave one by one:
	// those of a span a path with a smaller MTU refused (EINVAL).
	plainUntil int
	// segmentCeiling, when not zero, is the largest segment a GSO send may
	// carry on this socket from now on: one below the smallest the kernel
	// refused with EINVAL, so a path with a smaller MTU costs one refused
	// send, not one a batch. It only falls, for the socket's life.
	segmentCeiling int
	// invalidRun counts the GSO sends refused with EINVAL since the last GSO
	// send that left; at gsoInvalidLimit, GSO is switched off.
	invalidRun int
	// noMmsg is set once sendmmsg itself is refused — ENOSYS or EPERM, under
	// a seccomp filter that allows sendto — while a WriteTo of the same reply
	// leaves: every batch then leaves one WriteTo a reply.
	noMmsg bool
	// sent, sendErrno, from and to are one sendmmsg's arguments and results,
	// and write the function the descriptor runs, bound once at construction,
	// so a send moves nothing to the heap.
	sent      int
	sendErrno syscall.Errno
	from, to  int
	write     func(fd uintptr) bool
}

// newMultiWriter builds a batched sink over pc. It fails on a socket that is
// neither IPv4 nor IPv6 UDP — a Unix datagram socket has no GSO, and its peer
// no sockaddr this sink writes —, which the caller serves with portableSink.
func newMultiWriter(pc stdnet.PacketConn, sc rawConnProvider, counters *datagramCounters, onGSOOff func()) (*multiWriter, error) {
	raw, rerr := sc.SyscallConn()
	//: without the raw descriptor there is nothing to batch on.
	if rerr != nil {
		//: the caller writes one datagram a call instead.
		return nil, rerr
	}
	var family int
	var gso bool
	var serr error
	cerr := raw.Control(func(fd uintptr) {
		sa, err := syscall.Getsockname(int(fd))
		serr = err
		//: the family decides the sockaddr every reply is written with.
		switch sa.(type) {
		//: IPv4.
		case *syscall.SockaddrInet4:
			family = syscall.AF_INET
		//: IPv6, IPv4-mapped peers included.
		case *syscall.SockaddrInet6:
			family = syscall.AF_INET6
		}
		_, gerr := syscall.GetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpSegment)
		gso = gerr == nil
	})
	//: the socket is closed, or its name unreadable.
	if cerr != nil || serr != nil {
		//: whichever failed.
		return nil, cmp.Or(cerr, serr)
	}
	//: neither family this sink addresses.
	if family == 0 {
		//: the portable sink serves it.
		return nil, syscall.EAFNOSUPPORT
	}
	w := &multiWriter{
		pc: pc, raw: raw, family: family, counters: counters, onGSOOff: onGSOOff, send: sendmmsg,
		arena: make([]byte, replyArena), queue: make([]pendingReply, 0, replyQueue),
		headers: make([]mmsghdr, replyQueue), iovecs: make([]syscall.Iovec, replyQueue),
		spans: make([]replySpan, replyQueue), controlSpace: syscall.CmsgSpace(segmentSizeBytes),
	}
	w.oob = make([]byte, replyQueue*w.controlSpace)
	w.gso.Store(gso)
	//: a full send buffer waits for room, as WriteTo would.
	w.write = func(fd uintptr) bool {
		w.sent, w.sendErrno = w.send(fd, w.headers[w.from:w.to])
		//: anything but a full buffer ends the wait.
		return w.sendErrno != syscall.EAGAIN
	}
	//: ready for the read loop's first batch.
	return w, nil
}

// sendmmsg is the sendmmsg(2) the sink drives: how many of headers left, or the
// errno of the first that did not.
func sendmmsg(fd uintptr, headers []mmsghdr) (sent int, errno syscall.Errno) {
	result, _, callErrno := syscall.Syscall6(sysSendmmsg, fd,
		uintptr(unsafe.Pointer(&headers[0])), uintptr(len(headers)), 0, 0, 0)
	//: the first message failed: nothing left.
	if callErrno != 0 {
		//: its errno.
		return 0, callErrno
	}
	//: the count the kernel sent, which may be fewer than asked.
	return int(result), 0
}

// begin implements datagramSink: replies are queued from here.
func (w *multiWriter) begin() {
	w.mu.Lock()
	w.open = true
	w.mu.Unlock()
}

// reply implements datagramSink. Inside a batch it copies b into the arena and
// reports it sent; flush sends it, and a failure there is counted in State,
// since the handler has returned. Outside one, or for a peer this sink cannot
// address, it is one WriteTo.
func (w *multiWriter) reply(b []byte, to stdnet.Addr) (n int, err error) {
	w.mu.Lock()
	var entry pendingReply
	//: no batch open, a reply larger than the arena, or a peer without a
	//: sockaddr this socket writes: at once, as the port always did.
	if !w.open || len(b) > len(w.arena) || !w.encode(to, &entry) {
		w.mu.Unlock()
		//: the write's own answer.
		return w.pc.WriteTo(b, to)
	}
	//: the queue or the arena is full: send what is queued to make room.
	if len(w.queue) == cap(w.queue) || w.used+len(b) > len(w.arena) {
		w.flushLocked()
	}
	entry.off, entry.size = w.used, len(b)
	copy(w.arena[w.used:], b)
	w.used += len(b)
	w.queue = append(w.queue, entry)
	w.mu.Unlock()
	//: queued: it leaves at the batch's end.
	return len(b), nil
}

// encode writes to's sockaddr for this socket's family into entry, and reports
// whether it could: a UDP address of the socket's family, or an IPv4 one on an
// IPv6 socket, as a mapped address; a zoned IPv6 peer is left to WriteTo, which
// resolves its zone.
func (w *multiWriter) encode(to stdnet.Addr, entry *pendingReply) bool {
	udp, ok := to.(*stdnet.UDPAddr)
	//: only UDP peers, unzoned.
	if !ok || udp == nil || udp.Zone != "" {
		//: WriteTo's.
		return false
	}
	port := (*[2]byte)(unsafe.Pointer(&entry.name.Port))
	port[0], port[1] = byte(udp.Port>>portByteShift), byte(udp.Port)
	//: an IPv6 socket writes every peer as sockaddr_in6.
	if w.family == syscall.AF_INET6 {
		ip := udp.IP.To16()
		//: not an IP address at all.
		if ip == nil {
			//: WriteTo's.
			return false
		}
		entry.name.Family = syscall.AF_INET6
		copy(entry.name.Addr[:], ip)
		entry.namelen = syscall.SizeofSockaddrInet6
		//: addressed.
		return true
	}
	ip := udp.IP.To4()
	//: an IPv6 peer on an IPv4 socket cannot be written.
	if ip == nil {
		//: WriteTo's, which reports it.
		return false
	}
	sa4 := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&entry.name))
	sa4.Family = syscall.AF_INET
	copy(sa4.Addr[:], ip)
	entry.namelen = syscall.SizeofSockaddrInet4
	//: addressed.
	return true
}

// flush implements datagramSink: it sends what the batch queued and closes it.
func (w *multiWriter) flush() {
	w.mu.Lock()
	w.flushLocked()
	w.open = false
	w.mu.Unlock()
}

// flushLocked sends the queue and empties it. mu is held.
func (w *multiWriter) flushLocked() {
	//: sendmmsg refused for good: one WriteTo a reply.
	if w.noMmsg {
		w.writeEach(0, len(w.queue))
	}
	//: built and sent again from wherever a send asks to resume.
	for start := 0; start < len(w.queue) && !w.noMmsg; {
		start = w.sendAll(w.build(start))
	}
	w.queue = w.queue[:0]
	w.used = 0
	w.plainUntil = 0
}

// build lays the queue from start out as messages, as many as it takes, and
// returns their count: each run of consecutive replies to one peer, of one size
// with possibly a shorter last, at most gsoSegments of them and no larger than
// maxSegment, is one GSO message; any other reply is a message alone.
func (w *multiWriter) build(start int) int {
	gso := w.gso.Load()
	limitSize := maxSegment(w.family)
	//: a path refused larger segments: none past them from now on.
	if w.segmentCeiling > 0 {
		limitSize = min(limitSize, w.segmentCeiling)
	}
	messages := 0
	//: one message per run.
	for e := start; e < len(w.queue); messages++ {
		first := &w.queue[e]
		run := 1
		//: a run only where GSO is on and the reply may be a segment.
		if gso && e >= w.plainUntil && first.size > 0 && first.size <= limitSize {
			run = w.runFrom(e)
		}
		last := &w.queue[e+run-1]
		w.layout(messages, e, run, last.off+last.size-first.off)
		e += run
	}
	//: the messages laid out.
	return messages
}

// runFrom returns how many replies from e one GSO message carries.
func (w *multiWriter) runFrom(e int) int {
	first := &w.queue[e]
	limit := gsoSegments(w.family, first.size)
	run := 1
	//: the next reply joins while it goes to the same peer, no larger.
	for run < limit && e+run < len(w.queue) {
		next := &w.queue[e+run]
		//: another peer, or a larger datagram, starts a new message.
		if next.name != first.name || next.namelen != first.namelen || next.size > first.size || next.size == 0 {
			break
		}
		run++
		//: a shorter datagram can only be the last of a GSO send.
		if next.size < first.size {
			break
		}
	}
	//: at least the first.
	return run
}

// layout writes message m: count replies from first, total bytes long, one
// iovec over their contiguous bytes, and UDP_SEGMENT when there are several.
func (w *multiWriter) layout(m, first, count, total int) {
	entry := &w.queue[first]
	hdr := &w.headers[m].hdr
	hdr.Name = (*byte)(unsafe.Pointer(&entry.name))
	hdr.Namelen = entry.namelen
	// An empty reply still needs a valid base: the arena's first byte.
	w.iovecs[m].Base = &w.arena[min(entry.off, len(w.arena)-1)]
	setKernelLen(&w.iovecs[m].Len, total)
	hdr.Iov = &w.iovecs[m]
	setKernelLen(&hdr.Iovlen, 1)
	hdr.Flags = 0
	w.spans[m] = replySpan{first: first, count: count}
	//: one datagram: no segment size to send.
	if count == 1 {
		hdr.Control = nil
		setKernelLen(&hdr.Controllen, 0)
		return
	}
	control := w.oob[m*w.controlSpace : (m+1)*w.controlSpace]
	cmsg := (*syscall.Cmsghdr)(unsafe.Pointer(&control[0]))
	cmsg.Level = syscall.IPPROTO_UDP
	cmsg.Type = udpSegment
	cmsg.SetLen(syscall.CmsgLen(segmentSizeBytes))
	*(*uint16)(unsafe.Pointer(&control[syscall.CmsgLen(0)])) = uint16(entry.size)
	hdr.Control = &control[0]
	setKernelLen(&hdr.Controllen, w.controlSpace)
}

// sendAll sends messages [0, messages) built from the queue, resuming from the
// count each sendmmsg returns, and returns the queue index the next build
// starts from: the queue's end once everything left or was counted, or the
// first reply of a span to send again without GSO.
func (w *multiWriter) sendAll(messages int) int {
	//: from the first message the kernel has not taken yet.
	for k := 0; k < messages; {
		sent, err := w.sendFrom(k, messages)
		errno, refused := err.(syscall.Errno)
		//: the socket is closed: nothing more leaves.
		if err != nil && !refused {
			w.fail(k, messages)
			//: every reply left is counted.
			return len(w.queue)
		}
		//: the kernel took sent messages: resume after them.
		if err == nil && sent > 0 {
			w.left(k, k+sent)
			k += sent
			continue
		}
		//: message k was refused: rebuilt from a span, or counted and passed.
		if resume, again := w.refused(k, messages, errno); again {
			//: rebuilt from this span.
			return resume
		}
		k++
	}
	//: the whole queue went or was counted.
	return len(w.queue)
}

// refused judges message k's refusal with errno, messages laid out, and
// reports whether the queue is to be rebuilt from resume.
func (w *multiWriter) refused(k, messages int, errno syscall.Errno) (resume int, again bool) {
	span := w.spans[k]
	//: who refused.
	switch {
	//: sendmmsg itself refused: the rest leaves one WriteTo a reply.
	case errno == syscall.ENOSYS || errno == syscall.EPERM:
		w.unavailable(k, messages)
		//: every reply left or was counted: nothing to rebuild.
		return len(w.queue), true
	//: a GSO send, whose refusals each mean something of their own.
	case span.count > 1:
		//: judged by what GSO's errnos mean.
		return w.gsoRefused(span, errno)
	//: any other refusal drops this message's reply, counted.
	default:
		w.counters.replyFailures.Add(uint64(span.count))
		//: passed.
		return 0, false
	}
}

// gsoRefused judges a GSO send's refusal with errno, and reports whether the
// queue is to be rebuilt from resume.
func (w *multiWriter) gsoRefused(span replySpan, errno syscall.Errno) (resume int, again bool) {
	//: what the refusal means.
	switch errno {
	//: the device has no checksum offload, which GSO requires: off for the
	//: socket, and the rest leaves one datagram a message.
	case syscall.EIO:
		w.disableGSO()
		//: rebuilt from this span.
		return span.first, true
	//: the segment is past this path's MTU: this span leaves one by one, no
	//: segment this large is sent again, and a refusal no smaller segment
	//: cures switches GSO off.
	case syscall.EINVAL:
		w.plainUntil = span.first + span.count
		w.segmentCeiling = w.queue[span.first].size - 1
		w.invalidRun++
		//: as persistent as the device's EIO.
		if w.invalidRun >= gsoInvalidLimit {
			w.disableGSO()
		}
		//: rebuilt from this span.
		return span.first, true
	//: a GSO message past the IP length is a sizing bug, counted; GSO stays
	//: on.
	case syscall.EMSGSIZE:
		w.counters.gsoOversized.Add(1)
		w.counters.replyFailures.Add(uint64(span.count))
	//: any other refusal drops this message's replies, counted.
	default:
		w.counters.replyFailures.Add(uint64(span.count))
	}
	//: passed.
	return 0, false
}

// sendFrom runs one sendmmsg from message k, parked on the netpoller while the
// socket's send buffer is full: how many messages the kernel took, or the
// errno of the first it refused, or the socket's failure.
func (w *multiWriter) sendFrom(k, messages int) (sent int, err error) {
	w.from, w.to, w.sent, w.sendErrno = k, messages, 0, 0
	cerr := w.raw.Write(w.write)
	//: the socket's failure first.
	if cerr != nil {
		//: closed, typically.
		return 0, cerr
	}
	//: a refusal, as its errno.
	if w.sendErrno != 0 {
		//: message k's.
		return 0, w.sendErrno
	}
	//: what the kernel took.
	return w.sent, nil
}

// left notes that messages [k, end) left: a GSO send among them proves GSO
// works on this socket, and ends a run of EINVAL refusals.
func (w *multiWriter) left(k, end int) {
	//: any GSO message among them.
	for ; k < end; k++ {
		//: one GSO send left.
		if w.spans[k].count > 1 {
			w.invalidRun = 0
			//: the run is over.
			return
		}
	}
}

// unavailable serves messages [k, messages) after sendmmsg itself was refused,
// one WriteTo a reply. A reply that leaves so proves the refusal was the
// syscall's — a seccomp filter that allows sendto, not sendmmsg — and every
// later batch then leaves the same way; one that fails too, a firewall's
// EPERM for instance, is counted, and sendmmsg is tried again next batch.
func (w *multiWriter) unavailable(k, messages int) {
	//: from the first reply of message k to the last of the last message.
	first := w.spans[k].first
	last := w.spans[messages-1].first + w.spans[messages-1].count
	//: one reply left: the syscall, not the path, refused.
	if w.writeEach(first, last) > 0 {
		w.noMmsg = true
	}
}

// writeEach writes queued replies [first, last) one WriteTo each, counts each
// that fails, and returns how many left.
func (w *multiWriter) writeEach(first, last int) int {
	left := 0
	//: each reply to its own peer.
	for i := first; i < last; i++ {
		entry := &w.queue[i]
		//: the write's own answer, counted when it fails.
		if _, err := w.pc.WriteTo(w.arena[entry.off:entry.off+entry.size], entry.addr()); err != nil {
			w.counters.replyFailures.Add(1)
			continue
		}
		left++
	}
	//: how many left.
	return left
}

// fail counts the replies of messages [k, messages) as failed.
func (w *multiWriter) fail(k, messages int) {
	//: each message's replies.
	for ; k < messages; k++ {
		w.counters.replyFailures.Add(uint64(w.spans[k].count))
	}
}

// disableGSO switches GSO off for the socket's life and reports it once.
func (w *multiWriter) disableGSO() {
	//: only the first refusal reports.
	if w.gso.Swap(false) && w.onGSOOff != nil {
		w.onGSOOff()
	}
}
