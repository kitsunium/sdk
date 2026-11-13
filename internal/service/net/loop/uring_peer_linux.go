//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"slices"
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The socket command a direct descriptor's peer is read with (#974), declared
// in-tree for the same reason as the rest of the ABI (ADR 0018): Linux
// include/uapi/linux/io_uring.h, 6.7.
const (
	// opURingCmd is IORING_OP_URING_CMD (5.19): a command of the file's own
	// driver, a socket's since 6.7.
	opURingCmd uint8 = 46
	// sockOpGetsockopt is SOCKET_URING_OP_GETSOCKOPT (6.7): getsockopt on the
	// command's socket, SOL_SOCKET only, its result the option's length.
	sockOpGetsockopt uint32 = 2
	// ucredLen is sizeof(struct ucred): what a SO_PEERCRED read returns.
	ucredLen int32 = int32(unsafe.Sizeof(syscall.Ucred{}))
	// opFilesUpdate is IORING_OP_FILES_UPDATE (5.6): with its offset
	// fileIndexAlloc (5.19) it installs process descriptors into the direct
	// table, at free entries the kernel picks and writes back.
	opFilesUpdate uint8 = 20
)

// peerReading is a loop's way of reading a Unix connection's peer on direct
// descriptors (#974): cmd whether the socket command does it
// (uring-sock-getsockopt validated by the self-test), unix whether each
// listener is a Unix one, closing set as closeAll starts — a peer read then
// lets its slot go unopened —, why the command's absence as the self-test
// found it, and probe what its probe read, on the heap where the kernel may
// write.
type peerReading struct {
	cmd     bool
	closing bool
	unix    []bool
	why     string
	probe   syscall.Ucred
	// probeFD is the probe's Unix socket as the install reads it, then the
	// direct entry the kernel wrote back in its place.
	probeFD int32
}

// peerCell is a direct descriptor's peer as the accept read it: the kernel
// writes cred while the command runs, and ok says the read succeeded.
type peerCell struct {
	cred syscall.Ucred
	ok   bool
}

// prepPeerCred fills s as SOCKET_URING_OP_GETSOCKOPT of SO_PEERCRED on direct
// descriptor index, written to cred. The command's fields share the SQE's
// unions: cmd_op the low half of off, level and optname the two halves of
// addr, optlen file_index and optval addr3 — little-endian on every
// architecture this file builds on.
func prepPeerCred(s *sqe, index uint32, cred *syscall.Ucred, ud uint64) {
	s.opcode = opURingCmd
	s.fd = fixedFd(index)
	s.flags = sqeFixedFile
	s.off = uint64(sockOpGetsockopt)
	s.addr = uint64(syscall.SOL_SOCKET) | uint64(syscall.SO_PEERCRED)<<32
	s.fileIdx = uint32(ucredLen)
	s.addr3 = uint64(uintptr(unsafe.Pointer(cred)))
	s.userData = ud
}

// prepInstall fills s as IORING_OP_FILES_UPDATE of the one process
// descriptor at *fd into a free direct entry, which the kernel writes back
// to *fd.
func prepInstall(s *sqe, fd *int32, ud uint64) {
	s.opcode = opFilesUpdate
	s.fd = -1
	s.addr = uint64(uintptr(unsafe.Pointer(fd)))
	s.len = 1
	s.off = uint64(fileIndexAlloc)
	s.userData = ud
}

// unixListeners reports, per descriptor of fds, whether it is a Unix
// listener.
func unixListeners(fds []int) []bool {
	out := make([]bool, len(fds))
	//: each listener.
	for i := range fds {
		out[i] = unixListener(fds[i : i+1])
	}
	//: one flag a listener.
	return out
}

// peerCmd reports whether a Unix connection accepted on listener li has its
// peer read by the socket command: the group runs on direct descriptors and
// the kernel has the command (uring-sock-getsockopt).
func (ul *uLoop) peerCmd(li int) bool {
	//: direct, the command validated, a Unix listener.
	return ul.direct && ul.peers.cmd && li < len(ul.peers.unix) && ul.peers.unix[li]
}

// askPeer reads the peer of the Unix connection just accepted on slot with
// the socket command; the connection opens when the command completes
// (onPeer), so OnOpen and every byte come after the peer is known. No room
// for the command opens it at once, its peer unknown and counted.
func (rr *uRing) askPeer(slot int32) {
	c := &rr.r.conns[slot]
	index := uint32(c.fd)
	cell := &rr.creds[index]
	*cell = peerCell{}
	s := rr.u.get()
	//: no room: served, its peer unknown.
	if s == nil {
		rr.r.st.retries++
		rr.armRecv(slot)
		rr.r.opened(slot)
		//: opened.
		return
	}
	prepPeerCred(s, index, &cell.cred, udPack(udPeer, c.gen, uint32(slot)))
	c.ops++
	c.state = stPeer
}

// onPeer records the peer the command read and opens the connection, or,
// when the loop is closing, lets the slot go without opening it.
func (rr *uRing) onPeer(slot int32, gen uint32, c *cqe) {
	//: see onRecv.
	if !rr.owns(slot, gen) || rr.r.conns[slot].state != stPeer {
		//: dropped.
		return
	}
	cc := &rr.r.conns[slot]
	cc.ops--
	cell := &rr.creds[cc.fd]
	cell.ok = c.res == ucredLen && cell.cred.Pid > 0
	//: the loop stops: never opened, so no OnClose either.
	if rr.ul.peers.closing {
		cc.state = stDead
		rr.maybeClose(slot)
		//: released.
		return
	}
	cc.state = stOpen
	rr.armRecv(slot)
	rr.r.opened(slot)
}

// directPeer is the peer askPeer read for slot's direct descriptor.
func (rr *uRing) directPeer(r *ring, slot int32) (corenet.LoopPeerValue, bool) {
	index := r.conns[slot].fd
	//: read at accept, on a Unix connection only.
	if int(index) >= len(rr.creds) || !rr.creds[index].ok {
		//: unknown.
		return corenet.LoopPeerValue{}, false
	}
	u := rr.creds[index].cred
	//: named by the kernel.
	return corenet.LoopPeerValue{UID: u.Uid, GID: u.Gid, PID: uint32(u.Pid), Verified: true}, true
}

// indexTables are a ring's tables by direct-descriptor index: slotOf, every
// entry -1, and the peer cells, only where a Unix listener's peers are read
// on direct descriptors.
func (ul *uLoop) indexTables(table uint32) ([]int32, []peerCell) {
	slotOf := slices.Repeat([]int32{-1}, ul.slotSpace(table))
	//: no Unix peer read on direct descriptors: no cell.
	if !ul.direct || !ul.peers.cmd {
		//: the slots alone.
		return slotOf, nil
	}
	//: a cell an index.
	return slotOf, slices.Repeat([]peerCell{{}}, len(slotOf))
}

// openAccepted opens the connection accepted on listener li into slot: a
// Unix one on a direct descriptor once its peer is read, any other at once,
// the index's last peer forgotten.
func (rr *uRing) openAccepted(li int, index uint32, slot int32) {
	//: a Unix connection on a direct descriptor: opened once its peer is read.
	if rr.ul.peerCmd(li) {
		rr.askPeer(slot)
		//: asked.
		return
	}
	//: the index's last peer is not this connection's.
	if int(index) < len(rr.creds) {
		rr.creds[index] = peerCell{}
	}
	rr.armRecv(slot)
	rr.r.opened(slot)
}
