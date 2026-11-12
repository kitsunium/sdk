//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package server

import (
	stdnet "net"
	"slices"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
	"github.com/kitsunium/sdk/internal/service/proc/memlimit"
)

// startNativeEngine starts a loop group on the backend plan names —
// min(code, environment), ADR 0188 —: the leaf's native backends, handed
// duplicated descriptors of the listeners server bound (ADR 0186 §D3), or the
// portable backend when the plan says so or, when the code asked for auto, when
// no native backend starts — reported, never silent. A backend the plan rules
// out is never probed.
func startNativeEngine(group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
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
	//: each shard goes to its loop; a shared listener to every loop.
	for i := range min(len(handoff), len(group.loopOwners)) {
		handoff[i].Loop = group.loopOwners[i]
	}
	value := group.loop
	value.Backend = plan.backend
	g, err := loop.Start(loop.Config{
		Group: group.name, Value: value, MaxConns: group.limits.MaxConns,
		IdleTimeout: loopIdle(group), Handler: group.loopChain, Busy: group.loopBusy, Listeners: handoff,
		ReadTimeout: group.timeouts.Read.Duration(), KeepAlive: group.timeouts.KeepAlive.Duration(),
		UserTimeout: resolveUserTimeout(group),
		Disabled:    plan.disabled, DisabledBy: plan.disabledBy, LoopsSource: group.loopsSource,
		Memory: loopMemory(),
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

// loopMemory reads the cgroup allowance a loop group's slab budget is derived
// from, without installing a limit (#809); a test replaces it.
var loopMemory = memlimit.Derive

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

// probeNoDelay reads TCP_NODELAY back from every TCP listener the leaf set it
// on, whose accepted sockets inherit it: on when all carry it, absent with the
// first refusal otherwise, disabled when the operator switched it off, and
// unprobed on a group with no TCP listener, whose family has no such option.
func probeNoDelay(lns []stdnet.Listener, plan loopPlan) corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityTCPNoDelay, Class: classKernel, Status: statusOn}
	//: switched off: the leaf never set it.
	if slices.Contains(plan.disabled, corenet.CapabilityTCPNoDelay) {
		row.Status, row.Reason = statusDisabled, plan.disabledBy
		//: disabled.
		return row
	}
	//: no TCP listener: nothing to read, and nothing missing.
	if !slices.ContainsFunc(lns, isTCPListener) {
		row.Status, row.Reason = statusUnprobed, unixSocketReason
		//: unprobed.
		return row
	}
	//: each TCP listener, read back.
	for _, ln := range lns {
		//: a Unix listener has no such option.
		if !isTCPListener(ln) {
			continue
		}
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
		cerr := rc.Control(func(fd uintptr) { nfd, derr = dupHandoff(fd) })
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
