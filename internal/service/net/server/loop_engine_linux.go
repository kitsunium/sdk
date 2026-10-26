//go:build linux

package server

import (
	stdnet "net"
	"slices"
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
		return startPortable(group, lns, "", plan), nil
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
		IdleTimeout: loopIdle(group), Handler: group.loopHandler, Listeners: handoff,
		Disabled: plan.disabled, DisabledBy: plan.disabledBy,
	})
	//: started natively.
	if err == nil {
		//: serving, server's socket probe merged into the report.
		return &nativeEngine{Group: g, noDelay: probeNoDelay(lns, plan)}, nil
	}
	//: auto falls back, and says so; an explicit backend is refused.
	if plan.fallback && !errs.HasCode(err, corenet.CodeLoopNoDescriptors) {
		//: degraded, by name.
		return startPortable(group, lns, "native loop backend unavailable: "+err.Error(), plan), nil
	}
	//: refused.
	return nil, err
}

// nativeEngine is the leaf's group with server's socket probe merged into
// its capability report (ADR 0189).
type nativeEngine struct {
	*loop.Group
	// noDelay is the tcp-nodelay row.
	noDelay corenet.CapabilityValue
}

// State implements loopEngine: the leaf's report, then server's row.
func (e *nativeEngine) State() corenet.LoopStateValue {
	st := e.Group.State()
	st.Capabilities = append(st.Capabilities, e.noDelay)
	//: merged.
	return st
}

// probeNoDelay reads TCP_NODELAY back from every listener the leaf set it on,
// whose accepted sockets inherit it: on when all carry it, absent with the
// first refusal otherwise, disabled when the operator switched it off.
func probeNoDelay(lns []stdnet.Listener, plan loopPlan) corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityTCPNoDelay, Class: classKernel, Status: statusOn}
	//: switched off: the leaf never set it.
	if slices.Contains(plan.disabled, corenet.CapabilityTCPNoDelay) {
		row.Status, row.Reason = statusDisabled, plan.disabledBy
		//: disabled.
		return row
	}
	//: each listener, read back.
	for _, ln := range lns {
		v, err := noDelayOf(ln)
		//: not set.
		if err != nil || v == 0 {
			row.Status, row.Reason = statusAbsent, "getsockopt TCP_NODELAY: not set"
			//: absent.
			return row
		}
	}
	//: on everywhere.
	return row
}

// noDelayOf reads TCP_NODELAY from ln's socket.
func noDelayOf(ln stdnet.Listener) (int, error) {
	sc, ok := ln.(syscall.Conn)
	//: not a socket.
	if !ok {
		//: unknown.
		return 0, syscall.EINVAL
	}
	rc, err := sc.SyscallConn()
	//: no raw access.
	if err != nil {
		//: unknown.
		return 0, err
	}
	var v int
	var gerr error
	cerr := rc.Control(func(fd uintptr) { v, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY) })
	//: the first failure.
	if cerr != nil {
		//: unknown.
		return 0, cerr
	}
	//: the option, or the kernel's refusal.
	return v, gerr
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
