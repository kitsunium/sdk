//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"bytes"
	"syscall"
	"time"
)

// selfTestPayload is what the loopback self-test sends and expects back
// through a provided buffer.
var selfTestPayload = []byte("kitsunium net/loop io_uring self-test")

// selfTest is the probe's last stage: on a ring of its own, a multishot
// accept into a direct descriptor on a loopback listener, a connection, and
// a multishot recv that must deliver the payload through the buffer ring.
// Any failure, or a second without the completions, falls back to epoll.
func (ul *uLoop) selfTest(flags uint32) error {
	//: injected.
	if ul.hooks.failStage == "self_test" {
		//: named.
		return probeError("self_test", syscall.EIO)
	}
	rg, err := selfTestRing(flags)
	//: no ring, buffers or table.
	if err != nil {
		//: named.
		return err
	}
	defer rg.close()
	lfd, port, err := loopbackListener()
	//: no loopback.
	if err != nil {
		//: named.
		return probeError("self_test listen", errnoOfErr(err))
	}
	defer closeFDs([]int{lfd})
	prepAccept(rg.get(), lfd, udPack(udAccept, 0, 0), true)
	rg.submit()
	cfd, err := selfTestClient(port)
	//: no client, no connection, or no payload sent.
	if err != nil {
		//: named.
		return err
	}
	defer closeFDs([]int{cfd})
	run := &selfTestRun{u: rg, index: -1}
	run.exchange()
	//: passed, or named by what was missing; closing the ring closes the
	//: direct descriptor.
	return run.verdict()
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
func selfTestClient(port int) (int, error) {
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
	//: the payload waits in the socket before the recv is armed.
	if _, err := syscall.Write(cfd, selfTestPayload); err != nil {
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
		//: an index.
		if c.res >= 0 && st.index < 0 {
			st.index = c.res
			prepRecv(st.u.get(), uint32(st.index), udPack(udRecv, 0, 0))
		}
	//: bytes in a provided buffer.
	case udRecv:
		//: bytes.
		if c.res > 0 && c.flags&cqeFBuffer != 0 {
			st.got = append(st.got, st.u.buf(uint16(c.flags >> cqeBufferShift))[:c.res]...)
		}
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
