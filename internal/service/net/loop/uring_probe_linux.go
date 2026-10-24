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
// It then reports whether closing a direct descriptor releases its socket
// while another connection's recv, issued before the close, is still armed.
// Before 6.13 a fixed file removed from the table is put only once every
// request issued under the table's previous resource node has completed, and
// a multishot recv on an idle connection never does: closed sockets would
// stay allocated, their peers never told, for as long as the oldest
// connection lives (rsrc.c's io_rsrc_node, reworked in 6.13). Where that
// holds, the loop serves on process descriptors instead.
func (ul *uLoop) selfTest(flags uint32) (bool, error) {
	//: injected.
	if ul.hooks.failStage == "self_test" {
		//: named.
		return false, probeError("self_test", syscall.EIO)
	}
	rg, err := selfTestRing(flags)
	//: no ring, buffers or table.
	if err != nil {
		//: named.
		return false, err
	}
	defer rg.close()
	lfd, port, err := loopbackListener()
	//: no loopback.
	if err != nil {
		//: named.
		return false, probeError("self_test listen", errnoOfErr(err))
	}
	defer closeFDs([]int{lfd})
	prepAccept(rg.get(), lfd, udPack(udAccept, 0, 0), true)
	rg.submit()
	cfd, err := selfTestClient(port, selfTestPayload)
	//: no client, no connection, or no payload sent.
	if err != nil {
		//: named.
		return false, err
	}
	defer closeFDs([]int{cfd})
	run := &selfTestRun{u: rg, index: -1, second: -1}
	run.exchange()
	//: named by what was missing.
	if err := run.verdict(); err != nil {
		//: failed.
		return false, err
	}
	//: passed; closing the ring closes the direct descriptors.
	return run.releases(port, cfd), nil
}

// releases reports whether a direct descriptor closed with another
// connection's recv still armed lets its peer see the end at once.
func (st *selfTestRun) releases(port, first int) bool {
	other, err := selfTestClient(port, nil)
	//: no second connection: judged unable, which only costs capacity.
	if err != nil {
		//: process descriptors.
		return false
	}
	defer closeFDs([]int{other})
	st.pump(func() bool { return st.second >= 0 })
	//: never accepted.
	if st.second < 0 {
		//: process descriptors.
		return false
	}
	prepCancelFixed(st.u.get(), uint32(st.index), udPack(udCancel, 0, 0))
	st.pump(func() bool { return st.cancelled })
	prepCloseDirect(st.u.get(), uint32(st.index), udPack(udClose, 0, 0))
	st.pump(func() bool { return st.closed })
	//: the peer reads the end, or it never comes.
	return st.closed && peerSeesEnd(first)
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
	//: a two-entry table is enough for one connection.
	if errno := rg.registerSparse(2); errno != 0 {
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
	// index is the accepted direct descriptor, -1 before the accept.
	index int32
	// got is what the recv delivered so far.
	got []byte
	// second is the release check's second connection, -1 before it.
	second int32
	// cancelled and closed are the release check's completions.
	cancelled bool
	closed    bool
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
	//: the release check's cancel and close.
	case udCancel:
		st.cancelled = true
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

// onAccept arms a recv on the first and the second accepted index.
func (st *selfTestRun) onAccept(res int32) {
	//: by order of arrival.
	switch {
	//: an error: nothing to arm.
	case res < 0:
	//: the payload's connection.
	case st.index < 0:
		st.index = res
		prepRecv(st.u.get(), uint32(res), udPack(udRecv, 0, 0))
	//: the release check's idle connection.
	case st.second < 0:
		st.second = res
		prepRecv(st.u.get(), uint32(res), udPack(udRecv, 0, 1))
	}
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
