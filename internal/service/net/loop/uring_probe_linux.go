//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"bytes"
	"syscall"
	"time"
)

// peerPeekBytes is the release check's read buffer: any read will do, the
// check only waits for the end of the stream.
const peerPeekBytes int = 64

// selfTestPayload is what the loopback self-test sends and expects back
// through a provided buffer.
var selfTestPayload = []byte("kitsunium net/loop io_uring self-test")

// selfTest is the probe's last stage: on a ring of its own, a multishot
// accept into a direct descriptor on a loopback listener, a connection, and
// a multishot recv that must deliver the payload through the buffer ring.
// Any failure, or a second without the completions, falls back to epoll.
//
// It then runs the release check (releases): whether the loop's close, a
// shutdown first, tells the peer at once on a direct descriptor, and whether
// a close alone would leave the socket pinned. It reports the first as
// direct and the second as deferred.
func (ul *uLoop) selfTest(flags uint32) (direct, deferred bool, err error) {
	//: injected.
	if ul.hooks.failStage == "self_test" {
		//: named.
		return false, false, probeError("self_test", syscall.EIO)
	}
	rg, err := selfTestRing(flags)
	//: no ring, buffers or table.
	if err != nil {
		//: named.
		return false, false, err
	}
	defer rg.close()
	lfd, port, err := loopbackListener()
	//: no loopback.
	if err != nil {
		//: named.
		return false, false, probeError("self_test listen", errnoOfErr(err))
	}
	defer closeFDs([]int{lfd})
	prepAccept(rg.get(), lfd, udPack(udAccept, 0, 0), true)
	rg.submit()
	cfd, err := selfTestClient(port, selfTestPayload)
	//: no client, no connection, or no payload sent.
	if err != nil {
		//: named.
		return false, false, err
	}
	defer closeFDs([]int{cfd})
	run := &selfTestRun{u: rg, index: -1}
	run.exchange()
	//: named by what was missing.
	if err := run.verdict(); err != nil {
		//: failed.
		return false, false, err
	}
	//: passed; closing the ring closes the direct descriptors.
	direct, deferred = run.releases(port)
	//: the release check's verdict.
	return direct, deferred, nil
}

// releases is the release check. The payload's connection stays open, its
// multishot recv armed first: the oldest request of the table, as a
// keep-alive that idles is. Before 6.13 every file closed after it is put
// only once that recv completes (io_uring/rsrc.c, io_rsrc_node_ref_zero puts
// nodes in order), which an idle recv never does.
//
// A second connection is closed the way the loop closes: shutdown(SHUT_RDWR),
// the cancel hard-linked behind it, then close_direct. Its peer must read the
// end within 200 ms, whatever the put does: that is direct. A third is closed
// without the shutdown, cancel then close: its peer reads no end where the
// put waits for the oldest recv, which is deferred, and the rings sweep. A
// check that cannot run judges the loop unable, which only costs capacity:
// connections live in the process table.
func (st *selfTestRun) releases(port int) (direct, deferred bool) {
	shut, ok := st.victim(port, true)
	//: the loop's own close does not tell the peer: process descriptors.
	if !ok || !shut {
		//: process descriptors.
		return false, false
	}
	plain, ok := st.victim(port, false)
	//: the put cannot be judged: assume it waits, so the rings sweep.
	if !ok {
		//: direct, swept.
		return true, true
	}
	//: direct; swept when a close alone pinned the socket.
	return true, !plain
}

// victim connects a client, waits for its accept and its armed recv, closes
// the accepted direct descriptor (after a shutdown when shut is set) and
// reports whether the client read the end within 200 ms. ok is false when
// the connection or its close never completed.
func (st *selfTestRun) victim(port int, shut bool) (sawEnd, ok bool) {
	cfd, err := selfTestClient(port, nil)
	//: no connection.
	if err != nil {
		//: not judged.
		return false, false
	}
	defer closeFDs([]int{cfd})
	want := len(st.accepted) + 1
	st.pump(func() bool { return len(st.accepted) >= want })
	//: never accepted.
	if len(st.accepted) < want {
		//: not judged.
		return false, false
	}
	index := uint32(st.accepted[want-1])
	//: the chain's two SQEs in a row, never split by a submit.
	if !st.u.reserve(2) {
		//: not judged.
		return false, false
	}
	st.done = 0
	ops := 1
	//: the loop's close: a shutdown, the cancel hard-linked behind it.
	if shut {
		s := st.u.get()
		prepShutdownFixed(s, index, syscall.SHUT_RDWR, udPack(udShutdown, 0, 0))
		s.flags |= sqeIOHardlink
		ops = 2
	}
	prepCancelFixed(st.u.get(), index, udPack(udCancel, 0, 0))
	st.pump(func() bool { return st.done >= ops })
	st.closed = false
	prepCloseDirect(st.u.get(), index, udPack(udClose, 0, 0))
	st.pump(func() bool { return st.closed })
	//: the close never completed.
	if !st.closed {
		//: not judged.
		return false, false
	}
	//: the peer reads the end, or it never comes.
	return peerSeesEnd(cfd), true
}

// peerSeesEnd reports whether fd reads end-of-stream within 200 ms.
func peerSeesEnd(fd int) bool {
	var buf [peerPeekBytes]byte
	deadline := time.Now().Add(200 * time.Millisecond)
	//: polled without blocking.
	for time.Now().Before(deadline) {
		n, _, err := syscall.Recvfrom(fd, buf[:], syscall.MSG_DONTWAIT)
		//: the end.
		if n == 0 && err == nil {
			//: released.
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	//: still open.
	return false
}

// selfTestRing opens the self-test's ring with flags, and gives it buffers
// and a table as a serving ring has them. A failure closes it.
func selfTestRing(flags uint32) (*uring, error) {
	rg, step, errno := openURing(probeEntries, flags)
	//: no ring.
	if errno != 0 {
		//: named.
		return nil, probeError("self_test "+step, errno)
	}
	//: the buffers and the table, as a serving ring has them.
	if errno := rg.setupBuffers(8, 4096); errno != 0 {
		rg.close()
		//: named.
		return nil, probeError("self_test buf_ring", errno)
	}
	//: four entries: the payload's connection and the release check's two.
	if errno := rg.registerSparse(4); errno != 0 {
		rg.close()
		//: named.
		return nil, probeError("self_test sparse_files", errno)
	}
	//: ready.
	return rg, nil
}

// selfTestClient connects a blocking client socket to the loopback port and
// writes the payload, which waits in the socket before the recv is armed. A
// failure closes the socket.
func selfTestClient(port int, payload []byte) (int, error) {
	cfd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	//: no client socket.
	if err != nil {
		//: named.
		return -1, probeError("self_test socket", errnoOfErr(err))
	}
	//: blocking connect on loopback: the accept queue takes it at once.
	if err := syscall.Connect(cfd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		closeFDs([]int{cfd})
		//: named.
		return -1, probeError("self_test connect", errnoOfErr(err))
	}
	//: no payload: a connection only.
	if payload == nil {
		//: connected.
		return cfd, nil
	}
	//: the payload waits in the socket before the recv is armed.
	if _, err := syscall.Write(cfd, payload); err != nil {
		closeFDs([]int{cfd})
		//: named.
		return -1, probeError("self_test write", errnoOfErr(err))
	}
	//: connected, the payload sent.
	return cfd, nil
}

// selfTestRun is the self-test's exchange: the ring it runs on, the direct
// descriptor the accept returned, and the bytes the recv delivered.
type selfTestRun struct {
	// u is the self-test's own ring.
	u *uring
	// index is the payload's direct descriptor, -1 before the accept.
	index int32
	// got is what the recv delivered so far.
	got []byte
	// accepted are the release check's direct descriptors, in order.
	accepted []int32
	// done counts the release check's shutdown and cancel completions;
	// closed is its close's.
	done   int
	closed bool
}

// pump reaps the self-test's completions until done reports true or 500 ms
// are up.
func (st *selfTestRun) pump(done func() bool) {
	deadline := time.Now().Add(500 * time.Millisecond)
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until done, or out of time.
	for !done() && time.Now().Before(deadline) {
		st.u.wait(&tick)
		st.u.reap(64, st.onCQE)
	}
}

// exchange reaps the self-test's completions until the payload came back
// whole, or a second is up.
func (st *selfTestRun) exchange() {
	deadline := time.Now().Add(time.Second)
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until the payload came back whole, or the second is up.
	for !bytes.Equal(st.got, selfTestPayload) && time.Now().Before(deadline) {
		st.u.wait(&tick)
		st.u.reap(64, st.onCQE)
	}
}

// onCQE serves one self-test completion: the accept arms the recv on its
// index, the recv appends the bytes of its provided buffer.
func (st *selfTestRun) onCQE(c *cqe) {
	//: by operation.
	switch udOp(c.userData) {
	//: the connection: its recv is armed on the index.
	case udAccept:
		st.onAccept(c.res)
	//: the release check's shutdown, cancel and close.
	case udShutdown, udCancel:
		st.done++
	case udClose:
		st.closed = true
	//: bytes in a provided buffer.
	case udRecv:
		//: bytes.
		if c.res > 0 && c.flags&cqeFBuffer != 0 {
			st.got = append(st.got, st.u.buf(uint16(c.flags >> cqeBufferShift))[:c.res]...)
		}
	}
}

// onAccept arms a recv on every accepted index: the payload's first, then
// the release check's.
func (st *selfTestRun) onAccept(res int32) {
	//: an error: nothing to arm.
	if res < 0 {
		//: nothing.
		return
	}
	//: the payload's connection first.
	if st.index < 0 {
		st.index = res
	} else {
		st.accepted = append(st.accepted, res)
	}
	prepRecv(st.u.get(), uint32(res), udPack(udRecv, 0, uint32(len(st.accepted))))
}

// verdict is nil once the payload came back whole, otherwise the stage that
// never completed.
func (st *selfTestRun) verdict() error {
	//: received whole.
	if bytes.Equal(st.got, selfTestPayload) {
		//: passed.
		return nil
	}
	//: the accept never completed.
	if st.index < 0 {
		//: named.
		return probeError("self_test accept", syscall.ETIMEDOUT)
	}
	//: the recv never delivered.
	return probeError("self_test recv", syscall.ETIMEDOUT)
}

// loopbackListener binds a listening socket on 127.0.0.1, port chosen by the
// kernel.
func loopbackListener() (fd, port int, err error) {
	fd, err = syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	//: no socket.
	if err != nil {
		//: refused.
		return -1, 0, err
	}
	//: bound and listening, or closed.
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		_ = syscall.Close(fd)
		//: refused.
		return -1, 0, err
	}
	//: a queue of a few.
	if err := syscall.Listen(fd, 8); err != nil {
		_ = syscall.Close(fd)
		//: refused.
		return -1, 0, err
	}
	sa, err := syscall.Getsockname(fd)
	//: no name.
	if err != nil {
		_ = syscall.Close(fd)
		//: refused.
		return -1, 0, err
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	//: an AF_INET socket names itself in AF_INET; anything else is refused.
	if !ok {
		_ = syscall.Close(fd)
		//: refused.
		return -1, 0, syscall.EAFNOSUPPORT
	}
	//: the port the kernel chose.
	return fd, in4.Port, nil
}
