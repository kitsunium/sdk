//go:build linux

package server

import (
	stdnet "net"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// nativeLoopBackends is true where the leaf's io_uring and epoll exist.
const nativeLoopBackends bool = true

// startLoopEngine starts a loop group on Linux on the backend plan names —
// min(code, environment), ADR 0188 —: the leaf's native backends, handed
// duplicated descriptors of the listeners server bound (ADR 0186 §D3), or the
// portable backend when the plan says so or, when the code asked for auto, when
// no native backend starts — reported, never silent. A backend the plan rules
// out is never probed.
//
// IFACE-OPAQUE: which engine runs is decided here at Start — the leaf's
// *loop.Group or the portable backend — and server only drives the lifecycle
// half both share, so the caller holds the interface and never the type.
func startLoopEngine(group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
	//: the portable backend, as the code or the environment asked.
	if plan.backend == corenet.LoopBackendPortable {
		//: on the netpoller.
		return startPortable(group, lns, ""), nil
	}
	handoff, err := dupListeners(lns)
	//: a descriptor that cannot be duplicated.
	if err != nil {
		//: refused.
		return nil, err
	}
	value := group.loop
	value.Backend = plan.backend
	g, err := loop.Start(loop.Config{
		Group: group.name, Value: value, MaxConns: group.limits.MaxConns,
		IdleTimeout: loopIdle(group), Handler: group.loopChain, Listeners: handoff,
	})
	//: started natively.
	if err == nil {
		//: serving.
		return g, nil
	}
	//: auto falls back, and says so; an explicit backend is refused.
	if plan.fallback && !errs.HasCode(err, corenet.CodeLoopNoDescriptors) {
		//: degraded, by name.
		return startPortable(group, lns, "native loop backend unavailable: "+err.Error()), nil
	}
	//: refused.
	return nil, err
}

// dupListeners duplicates each listener's descriptor, close-on-exec and
// non-blocking: the one hand-off type the leaf takes.
func dupListeners(lns []stdnet.Listener) ([]loop.Listener, error) {
	out := make([]loop.Listener, 0, len(lns))
	//: one duplicate each; server keeps its own listener for Close.
	for _, ln := range lns {
		sc, ok := ln.(syscall.Conn)
		//: every listener server binds or adopts is a socket.
		if !ok {
			closeHandoff(out)
			//: refused.
			return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{}, errs.String("cause", "listener has no descriptor"))
		}
		rc, err := sc.SyscallConn()
		//: no raw access.
		if err != nil {
			closeHandoff(out)
			//: refused.
			return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{}, errs.String("cause", err.Error()))
		}
		var nfd int
		var derr error
		cerr := rc.Control(func(fd uintptr) {
			r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
			//: the kernel's answer.
			if errno != 0 {
				derr = errno
				return
			}
			nfd = int(r)
		})
		//: either failure refuses.
		if cerr != nil || derr != nil {
			closeHandoff(out)
			//: refused.
			return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{}, errs.String("cause", "dup failed"))
		}
		//: the loop never blocks on a descriptor.
		if err := syscall.SetNonblock(nfd, true); err != nil {
			swallowErr(syscall.Close(nfd))
			closeHandoff(out)
			//: refused.
			return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{}, errs.String("cause", err.Error()))
		}
		out = append(out, loop.Listener{FD: nfd})
	}
	//: handed off.
	return out, nil
}

// closeHandoff closes descriptors duplicated before a failure.
func closeHandoff(ls []loop.Listener) {
	//: each one.
	for _, l := range ls {
		swallowErr(syscall.Close(l.FD))
	}
}
