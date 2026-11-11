//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"encoding/binary"
	"net/netip"
	"syscall"
	"unsafe"
)

// The destination a datagram reached (#972), read from its control data and
// written back into the reply's SENDMSG: a wildcard socket answers from the
// address its peer wrote to, not from the kernel's choice.
const (
	// ipprotoIP and ipPktinfo name IP_PKTINFO's cmsg (include/uapi/linux/in.h).
	ipprotoIP int = 0
	ipPktinfo int = 8
	// ipprotoIPv6 and ipv6Pktinfo name IPV6_PKTINFO's (include/uapi/linux/in6.h).
	ipprotoIPv6 int = 41
	ipv6Pktinfo int = 50
	// inPktinfoBytes is struct in_pktinfo: ifindex, ipi_spec_dst, ipi_addr.
	inPktinfoBytes int = 12
	// in6PktinfoBytes is struct in6_pktinfo: ipi6_addr, ipi6_ifindex.
	in6PktinfoBytes int = 20
	// packetSeenMax is the most senders a lane remembers within one batch, the
	// newest kept: one a buffer the kernel can fill before OnBatchEnd.
	packetSeenMax int = 128
	// ipv4Bytes and ipv6Bytes are an address's length in each family.
	ipv4Bytes uint8 = 4
	ipv6Bytes uint8 = 16
	// recordControlOff is where a SENDMSG record's control data starts: after
	// its msghdr (56), its iovec (16) and the peer's sockaddr (32).
	recordControlOff int = 104
)

// ipv4Broadcast is 255.255.255.255, mapped on an IPv6 socket for a broadcast
// destination: a reply cannot leave from it.
var ipv4Broadcast = netip.AddrFrom4([ipv4Bytes]byte{255, 255, 255, 255})

// replySource is the address a reply leaves from: n bytes of addr, four on an
// IPv4 socket and sixteen on an IPv6 one; n zero leaves the kernel's choice.
type replySource struct {
	addr [16]byte
	n    uint8
}

// seenPeer is one sender of the batch and the destination it reached.
type seenPeer struct {
	from netip.AddrPort
	src  replySource
}

// pktinfoSource reads one IP_PKTINFO or IPV6_PKTINFO payload into the source a
// reply to its sender leaves from, as server's pinWriter reads it: on IPv4 the
// kernel's ipi_spec_dst — the interface's address for a broadcast or a
// multicast destination —, on IPv6 the destination itself unless it is a
// group, a mapped broadcast or nothing. ok is false for any other message.
func pktinfoSource(level, typ int, data []byte) (src replySource, ok bool) {
	//: by kind.
	switch {
	case level == ipprotoIP && typ == ipPktinfo && len(data) >= inPktinfoBytes:
		//: in_pktinfo.
		return pktinfo4(data), true
	case level == ipprotoIPv6 && typ == ipv6Pktinfo && len(data) >= in6PktinfoBytes:
		//: in6_pktinfo.
		return pktinfo6(data), true
	default:
		//: not a destination.
		return src, false
	}
}

// pktinfo4 reads an in_pktinfo's ipi_spec_dst; an all-zero one names no
// source.
func pktinfo4(data []byte) replySource {
	var src replySource
	spec := [ipv4Bytes]byte(data[ipv4Bytes : 2*ipv4Bytes])
	//: the kernel's answer, or none.
	if spec != [ipv4Bytes]byte{} {
		copy(src.addr[:ipv4Bytes], spec[:])
		src.n = ipv4Bytes
	}
	//: read.
	return src
}

// pktinfo6 reads an in6_pktinfo's address: a reply cannot leave from a group,
// a mapped broadcast or nothing.
func pktinfo6(data []byte) replySource {
	var src replySource
	a := netip.AddrFrom16([ipv6Bytes]byte(data[:ipv6Bytes]))
	//: a unicast destination.
	if !a.IsMulticast() && !a.IsUnspecified() && a.Unmap() != ipv4Broadcast {
		src.addr, src.n = a.As16(), ipv6Bytes
	}
	//: read.
	return src
}

// remember records that from reached src, unless the previous sender of the
// batch is the same with the same destination; past packetSeenMax the oldest
// is overwritten.
func (lp *packetLane) remember(from netip.AddrPort, src replySource) {
	//: no destination: nothing to answer from.
	if src.n == 0 {
		//: the kernel's choice.
		return
	}
	//: a train, or a peer sending twice: already the newest.
	if lp.seenLen > 0 {
		last := &lp.seen[(lp.seenNext+packetSeenMax-1)%packetSeenMax]
		//: unchanged.
		if last.from == from && last.src == src {
			return
		}
	}
	lp.seen[lp.seenNext] = seenPeer{from: from, src: src}
	lp.seenNext = (lp.seenNext + 1) % packetSeenMax
	lp.seenLen = min(lp.seenLen+1, packetSeenMax)
}

// sourceFor is the source a reply to to leaves from: the destination of the
// newest datagram to has sent in this batch, an IPv4-mapped peer matching its
// IPv4 form; the zero source, the kernel's choice, for a peer that sent none.
func (lp *packetLane) sourceFor(to netip.AddrPort) replySource {
	want := to.Addr().Unmap()
	//: newest first.
	for i := 1; i <= lp.seenLen; i++ {
		p := &lp.seen[(lp.seenNext+packetSeenMax-i)%packetSeenMax]
		//: the same peer.
		if p.from.Port() == to.Port() && p.from.Addr().Unmap() == want {
			//: its destination.
			return p.src
		}
	}
	//: unseen.
	return replySource{}
}

// forget empties the batch's senders, once its replies are queued.
func (lp *packetLane) forget() {
	lp.seenLen, lp.seenNext = 0, 0
}

// writePktinfo writes the cmsg that sends from src on a socket of family into
// c, and returns the bytes it took: zero when src names nothing this family
// sends from. The interface index stays zero, so the routing table still
// decides the way out, as server's pinWriter leaves it.
func writePktinfo(c []byte, family uint16, src replySource) int {
	//: by the socket's family.
	switch {
	case family == afInet && src.n == ipv4Bytes:
		binary.LittleEndian.PutUint64(c[0:8], uint64(cmsgHdrBytes+inPktinfoBytes))
		binary.LittleEndian.PutUint32(c[8:12], uint32(ipprotoIP))
		binary.LittleEndian.PutUint32(c[12:16], uint32(ipPktinfo))
		copy(c[cmsgHdrBytes+int(ipv4Bytes):], src.addr[:ipv4Bytes])
		//: CMSG_SPACE(12).
		return cmsgSpace(inPktinfoBytes)
	case family == afInet6 && src.n == ipv6Bytes:
		binary.LittleEndian.PutUint64(c[0:8], uint64(cmsgHdrBytes+in6PktinfoBytes))
		binary.LittleEndian.PutUint32(c[8:12], uint32(ipprotoIPv6))
		binary.LittleEndian.PutUint32(c[12:16], uint32(ipv6Pktinfo))
		copy(c[cmsgHdrBytes:], src.addr[:])
		//: CMSG_SPACE(20).
		return cmsgSpace(in6PktinfoBytes)
	default:
		//: nothing to name.
		return 0
	}
}

// cmsgSpace is CMSG_SPACE(n): the header and n bytes, aligned to eight.
func cmsgSpace(n int) int {
	//: aligned.
	return cmsgHdrBytes + (n+cmsgAlign-1)&^(cmsgAlign-1)
}

// unpin sends record k again without its source: the kernel refused the source
// (EINVAL — a broadcast address on a dual-stack socket), and the reply leaves
// from the kernel's choice rather than not at all, as server's pinWriter
// resends it. It reports whether the record was queued again.
func (lp *packetLane) unpin(k int) bool {
	r := lp.rec[k*packetRecordBytes : (k+1)*packetRecordBytes : (k+1)*packetRecordBytes]
	hdr := (*syscall.Msghdr)(unsafe.Pointer(&r[0]))
	ctl := r[recordControlOff:]
	used := int(hdr.Controllen)
	first := cmsgSpace(int(binary.LittleEndian.Uint64(ctl[0:8])) - cmsgHdrBytes)
	s := lp.u.get()
	//: no SQE: the caller counts it failed.
	if s == nil || first > used {
		//: not queued.
		return false
	}
	copy(ctl, ctl[first:used])
	hdr.Controllen = uint64(used - first)
	//: the UDP_SEGMENT cmsg alone, or no control data at all.
	if hdr.Controllen == 0 {
		hdr.Control = nil
	}
	prepSendmsg(s, lp.cfg.FD, unsafe.Pointer(hdr), pudSend|uint64(k))
	lp.pinned[k] = false
	//: in flight again.
	return true
}
