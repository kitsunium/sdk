//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/internal/bsdabi"
)

// adopted is the handle Attach returns on darwin and the BSDs: a process this
// one did not spawn, held by two kqueue knotes (EVFILT_PROC, NOTE_EXIT), each
// attached by the kernel to the process that had the pid when it was
// registered and to no later one. The first is the one the watcher blocks on
// to observe the end; the second is only ever polled, under checking, so
// that no wait consumes the event a signal must be refused on. No kernel
// handle signals a process these kernels did not let this one fork, so a
// signal goes by pid, sent only once the second knote has proven the process
// alive.
type adopted struct {
	// watchKQ holds the knote the watcher waits on; the runtime closes it
	// once the handle is collected.
	watchKQ *os.File
	// watchRaw lends watchKQ to kevent(2).
	watchRaw syscall.RawConn
	// checkKQ holds the knote polled before every signal.
	checkKQ *os.File
	// checkRaw lends checkKQ to kevent(2).
	checkRaw syscall.RawConn
	// checking serialises the polls of checkKQ, and gone records the end one
	// saw: the event is consumed once read.
	checking sync.Mutex
	gone     bool
	// done is closed once the watcher has seen the end.
	done chan struct{}
	// clk is what Stop's grace window runs on: clock.System from Attach.
	clk clock.Waiter
	// watching starts the one goroutine that observes the end.
	watching sync.Once
	pid      int
}

// procRecord is what the kernel's process record says of a process: when it
// started, as the kernel states it, and its real and effective users.
type procRecord struct {
	start      int64
	ruid, euid int
}

// attachProcess holds pid by two knotes and checks want against the process
// they hold. A cgroup bound is refused: these kernels have none. A users bound
// is read from the kernel's record (bsdabi) before and after the knotes are
// registered; the two must state one start time — a pid that changed hands in
// between is refused —, and the knote still silent afterwards proves the reads
// concerned the process held, so its pid was not yet another's.
func attachProcess(pid int, want coreproc.AttachValue) (coreproc.Process, error) {
	//: no cgroup to check a cgroup bound by.
	if want.Cgroup != "" {
		//: ADOPTION_REFUSED naming the bound.
		return nil, wrapAdoptionRefused(nil, errs.Int("pid", pid), errs.String("want", want.Cgroup),
			errs.String("why", "no cgroup on this platform"))
	}
	var before procRecord
	//: the record a users bound is checked on, read once before the knotes.
	if len(want.UIDs) > 0 {
		var err error
		before, err = readRecord(pid)
		//: gone, or no record this build can read.
		if err != nil {
			//: SIGNAL_FAILED (ESRCH), HANDLE_UNAVAILABLE or UNSUPPORTED_PLATFORM.
			return nil, err
		}
	}
	a, err := newAdopted(pid)
	//: gone, or no kqueue.
	if err != nil {
		//: SIGNAL_FAILED (ESRCH) or HANDLE_UNAVAILABLE.
		return nil, err
	}
	//: the users bound, on the process the knotes hold.
	if err = a.checkUsersHeld(want, before); err != nil {
		a.release()
		//: ADOPTION_REFUSED or SIGNAL_FAILED.
		return nil, err
	}
	//: ended already: the record read may describe a later process.
	if a.exited() {
		a.release()
		//: what a signal would answer.
		return nil, wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: the adopted process.
	return a, nil
}

// checkUsersHeld reads the record again, now that the knotes hold the process,
// requires the start time read before, and checks want's users on it.
func (a *adopted) checkUsersHeld(want coreproc.AttachValue, before procRecord) error {
	//: no user bound: nothing was read.
	if len(want.UIDs) == 0 {
		//: within every bound given.
		return nil
	}
	after, err := readRecord(a.pid)
	//: gone meanwhile, or the pid now names another process.
	if err == nil && after.start != before.start {
		//: what a signal to the process read first would answer.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", a.pid), errs.String("why", "the pid changed hands"))
	}
	//: the users of the process held.
	return checkUsers(a.pid, want, after.ruid, after.euid, err)
}

// newAdopted registers the two knotes on pid.
func newAdopted(pid int) (*adopted, error) {
	watchKQ, watchRaw, err := openKnote(pid)
	//: gone, or no kqueue.
	if err != nil {
		//: the classified refusal.
		return nil, err
	}
	checkKQ, checkRaw, err := openKnote(pid)
	//: the second knote: the process ended between the two.
	if err != nil {
		swallowErr(watchKQ.Close())
		//: the classified refusal.
		return nil, err
	}
	//: the handle; its watcher starts with the first wait.
	return &adopted{
		watchKQ: watchKQ, watchRaw: watchRaw, checkKQ: checkKQ, checkRaw: checkRaw,
		done: make(chan struct{}), clk: clock.System, pid: pid,
	}, nil
}

// openKnote opens a close-on-exec kqueue and registers on it an EVFILT_PROC
// knote for NOTE_EXIT of pid. ESRCH — no such process, or one already exiting
// — is SIGNAL_FAILED, as Linux's pidfd_open says; anything else
// HANDLE_UNAVAILABLE.
func openKnote(pid int) (*os.File, syscall.RawConn, error) {
	//: no fork between the kqueue and its close-on-exec flag.
	syscall.ForkLock.RLock()
	kq, err := syscall.Kqueue()
	//: the flag, before any child could inherit the descriptor.
	if err == nil {
		syscall.CloseOnExec(kq)
	}
	syscall.ForkLock.RUnlock()
	//: no kqueue.
	if err != nil {
		//: HANDLE_UNAVAILABLE.
		return nil, nil, wrapHandleUnavailable(err, errs.Int("pid", pid))
	}
	var change syscall.Kevent_t
	syscall.SetKevent(&change, pid, syscall.EVFILT_PROC, syscall.EV_ADD)
	change.Fflags = syscall.NOTE_EXIT
	//: the registration alone: with no room for events, its refusal is the errno.
	if _, err = syscall.Kevent(kq, []syscall.Kevent_t{change}, nil, nil); err != nil {
		swallowErr(syscall.Close(kq))
		//: no such process, or not visible to this one.
		return nil, nil, classifyKnote(err, pid)
	}
	f := os.NewFile(uintptr(kq), "kqueue")
	raw, err := f.SyscallConn()
	//: a file the runtime cannot lend.
	if err != nil {
		swallowErr(f.Close())
		//: HANDLE_UNAVAILABLE.
		return nil, nil, wrapHandleUnavailable(err, errs.Int("pid", pid))
	}
	//: the knote, held.
	return f, raw, nil
}

// classifyKnote maps a refused registration: ESRCH is a process gone
// (SIGNAL_FAILED), anything else no handle (HANDLE_UNAVAILABLE).
func classifyKnote(err error, pid int) error {
	//: no such process.
	if err == syscall.ESRCH {
		//: the verdict a signal would give.
		return wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: EPERM, EACCES: a process this one may not see.
	return wrapHandleUnavailable(err, errs.Int("pid", pid))
}

// release closes both knotes of a handle never handed out.
func (a *adopted) release() {
	//: the two kqueues.
	swallowErr(a.watchKQ.Close())
	swallowErr(a.checkKQ.Close())
}

// deliver sends sig by pid, once the polled knote has proven the process held
// still running: kill(2) is the only signal these kernels offer for a process
// this one did not fork. A process that has ended is SIGNAL_FAILED wrapping
// ESRCH and nothing is sent; the window between the proof and kill(2) is the
// one a PID reuse could still slip into, as with any pid-addressed signal.
func (a *adopted) deliver(sig coreproc.Signal) error {
	a.checking.Lock()
	defer a.checking.Unlock()
	fired, err := a.pollEnd()
	//: the end seen, or no proof the process lives.
	if fired || err != nil {
		//: nothing sent by number.
		return wrapSignal(errorOr(err, syscall.ESRCH), errs.Int("pid", a.pid), errs.Int("signal", sig.Int()))
	}
	//: the process held is alive: its pid is its own.
	if kErr := syscall.Kill(a.pid, syscall.Signal(sig.Int())); kErr != nil {
		//: EPERM for another user's process, ESRCH for one gone this instant.
		return wrapSignal(kErr, errs.Int("pid", a.pid), errs.Int("signal", sig.Int()))
	}
	//: delivered.
	return nil
}

// pollEnd polls the second knote without waiting; checking is held. Once it
// has fired the end is remembered, the event being consumed.
func (a *adopted) pollEnd() (bool, error) {
	//: the end, seen before or by the watcher.
	if a.gone {
		//: gone.
		return true, nil
	}
	fired, err := pollKnote(a.checkRaw)
	a.gone = fired
	//: what the poll said.
	return fired, err
}

// Handle reports no descriptor: these kernels give no process descriptor for
// a process this one did not fork, and a kqueue is no handle to pass on.
func (a *adopted) Handle() (*os.File, bool) {
	//: nothing to hand out.
	return nil, false
}

// SignalHandle is UnsupportedPlatform: no kernel handle signals a process
// here. Signal is the guarded pid-addressed delivery.
func (a *adopted) SignalHandle(_ coreproc.Signal) error {
	//: as the spawned handle answers on these kernels.
	return coreproc.UnsupportedPlatform
}

// startWatcher starts the goroutine that closes done once the first knote
// has fired, and returns once it runs.
func (a *adopted) startWatcher() {
	done := a.done
	running := make(chan struct{})
	//: Goroutine lifecycle: ends when the process ends — the knote fires —,
	//: which a Stop or the process itself brings about, and signals it by
	//: closing done, which every wait selects on. kevent(2) blocks the thread
	//: under it — a kqueue is not a descriptor the runtime's poller takes —,
	//: so a handle being waited on holds one thread until the end. It starts
	//: only once a caller waits, so a handle nobody waits on holds none.
	go func() {
		close(running)
		defer close(done)
		awaitKnote(a.watchRaw)
	}()
	//: the watcher is running before the first wait parks on done.
	<-running
}

// exited reports, without waiting, whether the process has ended.
func (a *adopted) exited() bool {
	a.checking.Lock()
	defer a.checking.Unlock()
	fired, err := a.pollEnd()
	//: a knote that cannot be polled proves nothing alive.
	return fired || err != nil
}

// awaitKnote blocks in kevent(2) on the kqueue raw lends until its knote
// fires. An interrupted wait (EINTR — the runtime's own signals) resumes; any
// other fault ends the watch, as nothing could then observe the end.
func awaitKnote(raw syscall.RawConn) {
	events := make([]syscall.Kevent_t, 1)
	//: until an event, or a fault no retry mends.
	for {
		var n int
		var kErr error
		cErr := raw.Control(func(fd uintptr) {
			//: no timeout: the end, however late.
			n, kErr = syscall.Kevent(int(fd), nil, events, nil)
		})
		//: fired, or the watch cannot go on.
		if n > 0 || cErr != nil || (kErr != nil && kErr != syscall.EINTR) {
			return
		}
	}
}

// pollKnote reports, without waiting, whether the knote on the kqueue raw
// lends has fired.
func pollKnote(raw syscall.RawConn) (bool, error) {
	events := make([]syscall.Kevent_t, 1)
	var n int
	var kErr error
	zero := syscall.Timespec{}
	cErr := raw.Control(func(fd uintptr) {
		//: a zero timeout, resumed when a signal interrupts it.
		for {
			n, kErr = syscall.Kevent(int(fd), nil, events, &zero)
			//: done, or a fault.
			if kErr != syscall.EINTR {
				return
			}
		}
	})
	//: an event pending, and what failed.
	return n > 0, errorOr(cErr, kErr)
}

// readRecord asks the kernel for the record of pid (sysctl(3), bsdabi's name
// and layout for this build's GOOS and GOARCH). A process gone — the kernel's
// ESRCH, or an empty answer — is SIGNAL_FAILED; a record this build cannot
// read is HANDLE_UNAVAILABLE, never a guess; a GOARCH without a known layout
// is UNSUPPORTED_PLATFORM.
func readRecord(pid int) (procRecord, error) {
	mib, size, ok := bsdabi.ProcRequest(runtime.GOOS, runtime.GOARCH, pid)
	//: no layout for this architecture.
	if !ok {
		//: the typed refusal.
		return procRecord{}, coreproc.UnsupportedPlatform
	}
	buf := make([]byte, size)
	n := uintptr(size)
	//: __sysctl(name, namelen, old, oldlenp, nil, 0); OpenBSD's libc stub, the
	//: kernel's trap elsewhere.
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	//: no such process, or nothing returned for it.
	if errno == syscall.ESRCH || (errno == 0 && n == 0) {
		//: the verdict a signal would give.
		return procRecord{}, wrapSignal(syscall.ESRCH, errs.Int("pid", pid))
	}
	//: the kernel refused the read.
	if errno != 0 {
		//: no record to check a bound on.
		return procRecord{}, wrapAdoptionRefused(errno, errs.Int("pid", pid), errs.String("why", "the process record is unreadable"))
	}
	start, ruid, euid, ok := bsdabi.ProcRecord(runtime.GOOS, runtime.GOARCH, pid, buf[:n])
	//: not the layout this build reads.
	if !ok {
		//: refused, never misread.
		return procRecord{}, wrapHandleUnavailable(nil, errs.Int("pid", pid), errs.String("why", "the process record is not the layout this build reads"))
	}
	//: the record.
	return procRecord{start: start, ruid: ruid, euid: euid}, nil
}
