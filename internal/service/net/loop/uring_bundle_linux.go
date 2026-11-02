//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// bundleProbeBufs is the bundle probe's buffers: two, so a bundle that
	// spans them shows.
	bundleProbeBufs uint32 = 2
	// bundleProbeSize is the bytes of one of them.
	bundleProbeSize uint32 = 64
	// bundleProbeBytes is what the probe sends: more than one buffer holds.
	bundleProbeBytes int = 100
	// bundleProbeWait bounds the probe's wait for its completion.
	bundleProbeWait time.Duration = 200 * time.Millisecond
)

// probeBundle decides whether the loop's recvs are bundles
// (IORING_RECVSEND_BUNDLE): the group opted into uring-recv-bundle (class
// A-code), the operator did not switch it off, the kernel advertises IORING_FEAT_RECVSEND_BUNDLE, and on a ring of
// its own one bundle recv of bundleProbeBytes fills both of two buffers set
// up as the serving rings' are. A kernel that refuses the flag (EINVAL) or
// fills one buffer only reports the capability absent: through 6.14 a
// buffer ring consumed incrementally hands a bundle one buffer
// (io_ring_buffers_peek's nr_avail = 1 under IOBL_INC, io_uring/kbuf.c
// v6.12 to v6.14, gone in v6.15), which a bundle gains nothing from (ADR
// 0205). It runs on the loop's thread, after the rings are built.
func (ul *uLoop) probeBundle() {
	//: the operator's switch: never probed.
	if ul.sw.disabled(corenet.CapabilityURingRecvBundle) {
		//: disabled.
		return
	}
	reason := ul.bundleRefusal()
	//: present and asked for: every recv a bundle.
	if reason == "" && ul.sw.wants(corenet.CapabilityURingRecvBundle) {
		ul.uses.recvFlags |= recvBundle
	}
	ul.rep.found(ul.sw, corenet.CapabilityURingRecvBundle, reason == "", reason)
}

// bundleRefusal runs the bundle probe and says why bundles are refused, ""
// when one spanned both buffers.
func (ul *uLoop) bundleRefusal() string {
	//: a test plays a kernel that refuses.
	if ul.hooks.failStage == "recv_bundle" {
		//: as an old kernel would.
		return "recv bundle refused: " + syscall.EINVAL.Error()
	}
	u, step, errno := openURing(probeEntries, 0)
	//: no ring for the probe.
	if errno != 0 {
		//: named.
		return "bundle probe " + step + ": " + errno.Error()
	}
	defer u.close()
	//: not advertised.
	if u.params.features&featRecvSendBundle == 0 {
		//: absent.
		return "IORING_FEAT_RECVSEND_BUNDLE not advertised"
	}
	//: the buffers as the serving rings consume theirs.
	if errno := u.setupBuffers(bundleProbeBufs, bundleProbeSize, bundleProbeBufs, ul.rings[0].u.bufInc); errno != 0 {
		//: named.
		return "bundle probe buf_ring: " + errno.Error()
	}
	res, errno := bundleRecv(u)
	//: by what the recv did.
	switch {
	//: refused or never completed.
	case errno != 0:
		//: named.
		return "recv bundle refused: " + errno.Error()
	//: one buffer only: incremental consumption before 6.15.
	case res < bundleProbeBytes:
		//: absent.
		return "a bundle filled one buffer of a ring consumed incrementally (before 6.15)"
	}
	//: both buffers in one completion.
	return ""
}

// bundleRecv sends bundleProbeBytes over a socket pair and receives them on
// u with one single-shot bundle recv, returning its result or its errno.
func bundleRecv(u *uring) (int, syscall.Errno) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	//: no pair.
	if err != nil {
		//: named.
		return 0, errnoOfErr(err)
	}
	defer closeFDs(fds[:])
	//: the bytes wait in the socket before the recv is issued.
	if _, err := syscall.Write(fds[0], make([]byte, bundleProbeBytes)); err != nil {
		//: named.
		return 0, errnoOfErr(err)
	}
	s := u.get()
	//: an empty probe ring always has room.
	if s == nil {
		//: busy.
		return 0, syscall.EBUSY
	}
	s.opcode = opRecv
	s.fd = int32(fds[1])
	s.flags = sqeBufferSelect
	s.ioprio = recvBundle
	s.bufIndex = bufGroup
	u.wait(new(kernelTimespec{nsec: int64(bundleProbeWait)}), 1)
	res, got := int32(0), false
	u.reap(1, func(c *cqe) { res, got = c.res, true })
	//: by the completion.
	switch {
	//: none within the bound.
	case !got:
		//: timed out.
		return 0, syscall.ETIME
	//: refused.
	case res < 0:
		//: the kernel's answer.
		return 0, syscall.Errno(-res)
	}
	//: received.
	return int(res), 0
}
