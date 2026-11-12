//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// backendKqueue is the backend startKqueue reports.
const backendKqueue corenet.LoopBackend = corenet.LoopBackendKqueue

// startNative starts g on kqueue, the one native backend of darwin and the
// BSDs (ADR 0222): auto and kqueue run the staged probe, then the loops; a
// failure is LoopBackendUnavailable naming the stage, which server turns into
// the portable backend, reported, when the code asked for auto. io_uring and
// epoll are Linux's mechanics, refused with the shared UnsupportedPlatform;
// the portable backend is server's, never the leaf's (ADR 0186 §D3).
func startNative(g *Group, cfg *Config) error {
	//: records are not served as a stream.
	if err := refuseSeqpacket(cfg); err != nil {
		//: refused by name, whatever the backend.
		return err
	}
	//: by the backend asked for.
	switch cfg.Value.Backend {
	//: kqueue, as asked or as the best one here.
	case corenet.LoopBackendAuto, corenet.LoopBackendKqueue:
		//: probed, then started, or refused by the stage that failed.
		return startKqueue(g, cfg)
	//: Linux's mechanics.
	case corenet.LoopBackendIOURing, corenet.LoopBackendEpoll:
		//: the shared sentinel, wrapped, never redeclared (ADR 0018 §(a)).
		return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", cfg.Group), errs.String("backend", cfg.Value.Backend.String()))
	//: the portable backend is server's.
	default:
		//: refused by name.
		return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
			errs.String("group", cfg.Group), errs.String("backend", cfg.Value.Backend.String()),
			errs.String("reason", "not a backend of the leaf"))
	}
}

// noDelay sets TCP_NODELAY on a listener, unless the operator switched
// tcp-nodelay off. The BSDs do not promise an accepted socket inherits it,
// so admit sets it again on each one (setAccepted).
func noDelay(fd int, sw capSwitches) {
	//: switched off: left as the kernel has it.
	if sw.disabled(corenet.CapabilityTCPNoDelay) {
		//: nothing.
		return
	}
	swallowErr(syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1))
}

// descriptorLimit is the effective RLIMIT_NOFILE soft limit, zero when it
// cannot be read. Its type is signed on FreeBSD and DragonFly, where
// RLIM_INFINITY is the largest positive value; a negative one reads as none.
func descriptorLimit() uint64 {
	var lim syscall.Rlimit
	//: unreadable counts as none.
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		//: none.
		return 0
	}
	//: the soft limit, the one the kernel enforces.
	return nonNegative(lim.Cur)
}

// nonNegative is v as a count, zero when v is negative: Rlimit's fields are
// signed on FreeBSD and DragonFly, unsigned on darwin, OpenBSD and NetBSD.
func nonNegative[T int64 | uint64](v T) uint64 {
	//: max is the identity where T is unsigned.
	return uint64(max(v, 0))
}

// mmapAnon maps n bytes of private anonymous memory, outside the Go heap.
func mmapAnon(n int) ([]byte, error) {
	//: read-write, private, anonymous (MAP_ANON, the BSDs' spelling).
	return syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
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

// pipeCloexec opens a pipe whose two ends are close-on-exec and non-blocking.
// darwin has no pipe2, so the flags are set under the fork lock, as the
// standard library does there: a fork in between never inherits either end.
func pipeCloexec() (readEnd, writeEnd int, err error) {
	var p [2]int
	syscall.ForkLock.RLock()
	err = syscall.Pipe(p[:])
	//: marked before the lock is released.
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	//: no pipe.
	if err != nil {
		//: the kernel's answer.
		return -1, -1, err
	}
	//: the loop never blocks on either end.
	if err := firstErr(syscall.SetNonblock(p[0], true), syscall.SetNonblock(p[1], true)); err != nil {
		closeFDs(p[:])
		//: the kernel's answer.
		return -1, -1, err
	}
	//: both ends.
	return p[0], p[1], nil
}

// firstErr is the first non-nil error of errs.
func firstErr(list ...error) error {
	//: in order.
	for _, err := range list {
		//: the first failure.
		if err != nil {
			//: as returned.
			return err
		}
	}
	//: none failed.
	return nil
}
