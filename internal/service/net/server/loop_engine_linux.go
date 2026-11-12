//go:build linux

package server

import (
	stdnet "net"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// nativePoller is epoll: Linux's readiness poller, with io_uring above it.
const nativePoller corenet.Capability = corenet.CapabilityEpoll

// startLoopEngine starts a loop group on Linux on the backend plan names —
// min(code, environment), ADR 0188 —: the leaf's io_uring or epoll, or the
// portable backend (startNativeEngine). kqueue is darwin's and the BSDs'
// (ADR 0222), refused here with the shared UnsupportedPlatform.
//
// IFACE-OPAQUE: which engine runs is decided here at Start — the leaf's
// *loop.Group or the portable backend — and server only drives the lifecycle
// half both share, so the caller holds the interface and never the type.
func startLoopEngine(group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
	//: kqueue is darwin's and the BSDs'.
	if group.loop.Backend == corenet.LoopBackendKqueue {
		//: the shared sentinel, wrapped, never redeclared (ADR 0018 §(a)).
		return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", group.name), errs.String("backend", group.loop.Backend.String()))
	}
	//: io_uring, epoll or portable, as planned.
	return startNativeEngine(group, lns, plan)
}

// dupHandoff duplicates fd, close-on-exec from its birth (F_DUPFD_CLOEXEC).
func dupHandoff(fd uintptr) (int, error) {
	r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
	//: the kernel's answer.
	if errno != 0 {
		//: refused.
		return -1, errno
	}
	//: the duplicate.
	return int(r), nil
}
