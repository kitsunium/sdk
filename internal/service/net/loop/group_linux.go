//go:build linux

package loop

import (
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// backendEpoll is the backend startEpoll reports.
const backendEpoll corenet.LoopBackend = corenet.LoopBackendEpoll

// startNative starts g on the backend cfg asks for. The portable backend is
// server's, never the leaf's (ADR 0186 §D3).
func startNative(g *Group, cfg *Config) error {
	//: by the backend asked for.
	switch cfg.Value.Backend {
	//: auto takes the best one the probe reaches, and epoll is wave 1's
	//: floor on every Linux architecture; epoll is also taken as asked.
	case corenet.LoopBackendAuto, corenet.LoopBackendEpoll:
		g.stage = "epoll"
		//: started.
		return startEpoll(g, cfg)
	//: not built yet on this branch, or the portable backend.
	default:
		//: refused by name.
		return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
			errs.String("group", cfg.Group), errs.String("backend", cfg.Value.Backend.String()),
			errs.String("reason", "not a backend of the leaf on this platform"))
	}
}

// descriptorLimit is the effective RLIMIT_NOFILE soft limit, zero when it
// cannot be read.
func descriptorLimit() uint64 {
	var lim syscall.Rlimit
	//: unreadable counts as none.
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		//: none.
		return 0
	}
	//: the soft limit, the one the kernel enforces.
	return lim.Cur
}

// mmapAnon maps n bytes of private anonymous memory, outside the Go heap.
func mmapAnon(n int) ([]byte, error) {
	//: read-write, private, anonymous.
	return syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
}

// closeFDs closes every valid descriptor of fds.
func closeFDs(fds []int) {
	//: a negative entry was never opened.
	for _, fd := range fds {
		//: valid only.
		if fd >= 0 {
			swallowErr(syscall.Close(fd))
		}
	}
}

// closeListeners closes the hand-off descriptors of a group that never started.
func closeListeners(ls []Listener) {
	//: the group owned them from the call on.
	for _, l := range ls {
		swallowErr(syscall.Close(l.FD))
	}
}

// unavailable wraps a setup failure as LoopBackendUnavailable naming the step.
func unavailable(step string, err error) error {
	//: the step and the kernel's answer.
	return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
		errs.String("stage", step), errs.String("reason", err.Error()))
}
