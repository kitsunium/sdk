//go:build linux

package loop

import (
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// backendEpoll is the backend startEpoll reports.
const backendEpoll corenet.LoopBackend = corenet.LoopBackendEpoll

// The io_uring accept watchdog's bounds. They live in a file every Linux
// architecture builds, not beside the watchdog, because the conformance suite
// reads them on every Linux architecture, io_uring's wave 1 or not.
const (
	// watchdogTicks is how long an accept may look stalled — armed, the
	// listener's queue not empty, no accept completion on any ring — before
	// the watchdog re-arms it.
	watchdogTicks int64 = 100
	// watchdogFires is how many re-arms the watchdog makes before the ring
	// drains.
	watchdogFires int = 3
)

// startNative starts g on the backend cfg asks for. auto takes io_uring when
// the staged probe passes and epoll otherwise, the reason in State and on the
// start line (the batchDegradation precedent); an explicit io_uring the probe
// refused is LoopBackendUnavailable. The portable backend is server's, never
// the leaf's (ADR 0186 §D3).
func startNative(g *Group, cfg *Config) error {
	//: by the backend asked for.
	switch cfg.Value.Backend {
	//: the best one the probe reaches.
	case corenet.LoopBackendAuto:
		err := startIOURing(g, cfg, cfg.hooks)
		//: io_uring runs.
		if err == nil {
			//: started.
			return nil
		}
		g.stage = "epoll"
		g.reason = "io_uring unavailable: " + err.Error()
		//: epoll, wave 1's floor on every Linux architecture.
		return startEpoll(g, cfg)
	//: io_uring, as asked: refused by the probe's reason when it fails.
	case corenet.LoopBackendIOURing:
		//: started, or refused.
		return startIOURing(g, cfg, cfg.hooks)
	//: epoll, as asked.
	case corenet.LoopBackendEpoll:
		g.stage = "epoll"
		//: started.
		return startEpoll(g, cfg)
	//: the portable backend is server's.
	default:
		//: refused by name.
		return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
			errs.String("group", cfg.Group), errs.String("backend", cfg.Value.Backend.String()),
			errs.String("reason", "not a backend of the leaf"))
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
