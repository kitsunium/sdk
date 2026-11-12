//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package server

import (
	stdnet "net"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// nativePoller is kqueue: the readiness poller of darwin and the BSDs
// (ADR 0222).
const nativePoller corenet.Capability = corenet.CapabilityKqueue

// startLoopEngine starts a loop group on darwin or a BSD on the backend plan
// names — min(code, environment), ADR 0188 —: the leaf's kqueue, or the
// portable backend (startNativeEngine), auto falling back to it, reported,
// when kqueue's probe refuses. io_uring and epoll are Linux's, refused with
// the shared UnsupportedPlatform whatever the environment.
//
// IFACE-OPAQUE: which engine runs is decided here at Start — the leaf's
// *loop.Group or the portable backend — and server only drives the lifecycle
// half both share, so the caller holds the interface and never the type.
func startLoopEngine(group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
	//: Linux's mechanics.
	if b := group.loop.Backend; b == corenet.LoopBackendIOURing || b == corenet.LoopBackendEpoll {
		//: the shared sentinel, wrapped, never redeclared (ADR 0018 §(a)).
		return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", group.name), errs.String("backend", b.String()))
	}
	//: kqueue or portable, as planned.
	return startNativeEngine(group, lns, plan)
}

// dupHandoff duplicates fd and marks the duplicate close-on-exec under the
// fork lock, so a fork in between never inherits it: OpenBSD takes no raw
// fcntl outside its libc, and Dup is the one call every BSD wraps.
func dupHandoff(fd uintptr) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	nfd, err := syscall.Dup(int(fd))
	//: refused.
	if err != nil {
		//: the kernel's answer.
		return -1, err
	}
	syscall.CloseOnExec(nfd)
	//: the duplicate.
	return nfd, nil
}
