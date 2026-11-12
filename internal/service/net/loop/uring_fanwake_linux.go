//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	// fanWakeTick bounds the primary's plain wait of a loop with secondary
	// rings on a kernel whose wake is slow (#703): the extra latency of a
	// secondary's exchange is bounded by it instead of by tickEvery.
	fanWakeTick time.Duration = time.Millisecond
	// fanWakeDelay is how long after the primary's wait starts the probe's
	// writer writes.
	fanWakeDelay time.Duration = 2 * time.Millisecond
	// fanWakeBound is the probe's longest wait.
	fanWakeBound time.Duration = 50 * time.Millisecond
	// fanWakeSlow is the delay past which the wake counts as slow: half the
	// tick, which a prompt wake (tens of microseconds) never nears.
	fanWakeSlow time.Duration = tickEvery / 2
	// fanUDEvent tags the primary's poll of the eventfd.
	fanUDEvent uint64 = 1
	// fanUDPipe tags the secondary's poll of the pipe.
	fanUDPipe uint64 = 2
	// fanMissedLimit is how many plain waits in a row may end on their
	// timeout with a secondary's completions pending before the loop bounds
	// its wait (watchFan): on a kernel that wakes, a completion lands in the
	// last microseconds of a wait of several milliseconds once in hundreds,
	// never three times in a row.
	fanMissedLimit int = 3
)

var (
	// fanWakeOnce runs the probe once a process: the kernel answers the same
	// for every loop.
	fanWakeOnce sync.Once
	// fanProbed is the start's probe's own verdict, written once under
	// fanWakeOnce: the process's (secondaryWake) reads slow past it when a
	// loop's waits found the wake slow (watchFan).
	fanProbed string
)

// slowFanWake reports whether this kernel leaves a secondary ring's
// completion to the primary's timeout (#703), probing it once.
func slowFanWake() bool {
	fanWakeOnce.Do(func() {
		fanProbed = probeFanWake(true)
		secondaryWake.Store(fanProbed)
	})
	//: the stored verdict.
	return secondaryWakeVerdict() == wakeSlow
}

// probeFanWake reproduces a loop of two rings on a thread of its own: a
// primary polling the secondaries' eventfd, a secondary whose CQ notifies
// it and which polls a pipe, the pipe written from another thread once the
// primary waits. It times the primary's wake from the write and returns the
// verdict; a probe that cannot be set up says prompt, the loop unchanged.
// Without register the secondary notifies nothing: the suite's negative
// control, which must read slow.
//
// Goroutine lifecycle: one writer, started here and joined before return.
func probeFanWake(register bool) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rings, fds, ok := fanWakeRig(register)
	defer func() {
		for _, u := range rings {
			u.close()
		}
		closeFDs(fds)
	}()
	//: no rig: nothing measured, nothing changed.
	if !ok {
		//: as if prompt.
		return wakePrompt
	}
	wrote := make(chan time.Time, 1)
	go func() {
		time.Sleep(fanWakeDelay)
		at := time.Now()
		_, err := syscall.Write(fds[2], []byte{1})
		swallowErr(err)
		wrote <- at
	}()
	woke := fanWakeWait(rings[0])
	at := <-wrote
	//: past half a tick, or never.
	if woke.IsZero() || woke.Sub(at) >= fanWakeSlow {
		//: slow.
		return wakeSlow
	}
	//: prompt.
	return wakePrompt
}

// fanWakeRig builds the probe's two rings, its eventfd and its pipe:
// rings[0] the primary, rings[1] the secondary; fds the eventfd, then the
// pipe's read and write ends.
func fanWakeRig(register bool) (rings []*uring, fds []int, ok bool) {
	efd, _, e := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	//: no eventfd.
	if e != 0 {
		//: no rig.
		return nil, nil, false
	}
	fds = append(fds, int(efd))
	var p [2]int
	//: no pipe.
	if err := syscall.Pipe2(p[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		//: no rig.
		return nil, fds, false
	}
	fds = append(fds, p[0], p[1])
	//: the two rings as the loop sets them up by default.
	for range 2 {
		u, _, errno := openURing(probeEntries, setupCoopTaskrun|setupSingleIssuer)
		//: an older kernel refuses the flags.
		if errno == syscall.EINVAL {
			u, _, errno = openURing(probeEntries, 0)
		}
		//: no ring.
		if errno != 0 {
			//: no rig.
			return rings, fds, false
		}
		rings = append(rings, u)
	}
	//: the secondary's CQ notifies the eventfd, as registerFan does.
	if register && uringRegister(rings[1].fd, regEventFD, unsafe.Pointer(new(int32(efd))), 1) != 0 {
		//: no rig.
		return rings, fds, false
	}
	sa, sb := rings[0].get(), rings[1].get()
	//: an empty ring always has room.
	if sa == nil || sb == nil {
		//: no rig.
		return rings, fds, false
	}
	prepPollMulti(sa, int(efd), fanUDEvent)
	prepPollMulti(sb, p[0], fanUDPipe)
	rings[1].submit()
	rings[0].submit()
	//: armed.
	return rings, fds, true
}

// fanWakeWait waits on the primary, bounded by fanWakeBound, for the
// eventfd's poll, and returns when it came; zero when it never did.
func fanWakeWait(u *uring) time.Time {
	deadline := time.Now().Add(fanWakeBound)
	//: until the poll's CQE, or the bound.
	for left := time.Until(deadline); left > 0; left = time.Until(deadline) {
		u.wait(new(kernelTimespec{nsec: int64(left)}), 1)
		//: the secondary's completion woke the primary.
		if fanEventReaped(u) {
			//: now.
			return time.Now()
		}
	}
	//: never.
	return time.Time{}
}

// fanEventReaped reaps u and reports whether the eventfd's poll completed.
func fanEventReaped(u *uring) bool {
	got := false
	u.reap(int(probeEntries), func(c *cqe) { got = got || c.userData == fanUDEvent })
	//: as reaped.
	return got
}

// fanTickFor is the primary's plain wait of a loop with rings rings: the
// tick, or fanWakeTick where the kernel's wake is slow and a secondary
// exists.
func fanTickFor(rings int) kernelTimespec {
	//: one ring, or a kernel that wakes.
	if rings < 2 || !slowFanWake() {
		//: the tick.
		return kernelTimespec{nsec: int64(tickEvery)}
	}
	//: bounded.
	return kernelTimespec{nsec: int64(fanWakeTick)}
}

// watchFan is #703's detector in service, on the loop's own rings: the
// start's probe builds two rings of its own, and a kernel may wake its rig
// and not the loop's (6.14 in #1336's CI: probe prompt, every secondary round
// trip a tick). A plain wait of fanWakeSlow or more after which the batch
// reaped a secondary's completions is a wake the kernel did not give: the
// completion waited for the primary's timeout, its task work run only as the
// thread left the wait. fanMissedLimit of them in a row, with no plain wait in
// between that a secondary's completion ended sooner, bound the loop's wait
// to fanWakeTick, and the process's verdict reads slow, so the loops started
// next begin bounded. The plain wait that follows an empty grouped window is
// judged too: a loop whose last batch reaped four completions groups its next
// wait, and that window's plain tail waits the tick as a plain wait does. The
// completions are the ones reapRings counted, so a completion the kernel
// posts as the batch reaps counts as one already there. It reads the batch's
// start the loop already took: no system call (ADR 0202). A batch that did
// not wait, and a loop already bounded or with no secondary, are not judged.
func (ul *uLoop) watchFan(plain bool) {
	lp := ul.lp
	//: nothing to judge, judged already, or no secondary completion: this
	//: wait says nothing of the wake.
	if len(ul.rings) < 2 || ul.fan.nsec <= int64(fanWakeTick) || !plain || lp.ended == 0 || ul.hooks.fanUnwatched || ul.fanReaped == 0 {
		//: as it is.
		return
	}
	//: a secondary's completion ended the wait promptly: the kernel wakes.
	if lp.started-lp.ended < fanWakeSlow {
		ul.fanMissed = 0
		//: prompt.
		return
	}
	ul.fanMissed++
	//: not yet a pattern.
	if ul.fanMissed < fanMissedLimit {
		//: watched.
		return
	}
	ul.fan = kernelTimespec{nsec: int64(fanWakeTick)}
	secondaryWake.Store(wakeSlow)
}

// plainWait is this batch's plain wait: fan, the bound fanTickFor chose,
// only while a secondary ring holds something the kernel may complete — a
// slot, its close included, or an accept armed —; the tick otherwise. A loop
// whose secondaries serve nothing, the rings filling in order (ADR 0203),
// so wakes no more often on a kernel whose wake is slow than on another
// (#703).
func (ul *uLoop) plainWait(fan kernelTimespec) kernelTimespec {
	//: each secondary.
	for _, rr := range ul.rings[1:] {
		//: a completion may come from it: bounded.
		if rr.used() > 0 || armedAny(rr.armed) {
			//: as chosen.
			return fan
		}
	}
	//: nothing to come from a secondary.
	return kernelTimespec{nsec: max(fan.nsec, int64(tickEvery))}
}

// armedAny reports whether any listener has an accept in flight.
func armedAny(armed []int) bool {
	//: each listener.
	for _, n := range armed {
		//: one in flight.
		if n > 0 {
			//: armed.
			return true
		}
	}
	//: none.
	return false
}
