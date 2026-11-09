//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/exec"
	svcnotify "github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// handoverHere is nil: Linux hands units over.
func handoverHere(_ string) error {
	//: supported.
	return nil
}

// freeze is evFreeze, taken between two events: the events already posted
// are run first, then the model is written down, the descriptors the unit
// keeps duplicated into the hand-over, the processes forgotten — never
// stopped —, every waiter answered UNIT_CLOSED, and the actor ends as Close
// ends it, minus the stop: its finish closes the originals, the hand-over
// keeps the duplicates.
func (a *actor) freeze(req *event) {
	u := a.u
	a.drain()
	//: a unit closing, or one a drained close ended: nothing to hand.
	if a.m.has(flagClosing) || a.on(shellExiting) {
		u.answer([]uint64{req.waiter}, unitClosed(u.s.name))
		//: refused.
		return
	}
	fu := &frozenUnit{name: u.s.name, store: map[uint64]*os.File{}}
	//: a descriptor that cannot be duplicated: the unit runs on, unfrozen.
	if err := a.freezeFiles(fu); err != nil {
		fu.closeFiles()
		u.answer([]uint64{req.waiter}, err)
		//: refused.
		return
	}
	m := &a.m
	waiters := slices.Concat(m.job.waiters, m.pending.waiters, m.closeWaiters)
	fu.model = encodeModel(m)
	fu.procs = a.handProcs()
	clear(a.procs)
	clear(a.children)
	u.answer(waiters, unitClosed(u.s.name))
	u.mu.Lock()
	u.frozen, u.closed = fu, true
	u.mu.Unlock()
	a.put(shellExiting, true)
	u.answer([]uint64{req.waiter}, nil)
}

// drain runs the events posted before the freeze was taken; a second freeze
// among them is refused.
func (a *actor) drain() {
	//: as many as were waiting.
	for n := len(a.u.inbox); n > 0 && !a.on(shellExiting); n-- {
		ev := <-a.u.inbox
		//: another freeze: this one wins.
		if ev.kind == evFreeze {
			a.u.answer([]uint64{ev.waiter}, unitClosed(a.u.s.name))
			continue
		}
		a.handle(&ev)
	}
}

// freezeFiles duplicates into fu the store's descriptors, an instance's
// connection and the notification socket ListenNotify made — which is
// detached, so the actor's end leaves its path for the incoming image.
func (a *actor) freezeFiles(fu *frozenUnit) error {
	//: the store, by id.
	for _, id := range a.storeOrder {
		f, err := dupFile(a.store[id].file)
		//: refused.
		if err != nil {
			//: HANDOVER_MISMATCH.
			return err
		}
		fu.store[id] = f
	}
	//: an instance's connection.
	if a.u.conn != nil {
		f, err := dupFile(a.u.conn)
		//: refused.
		if err != nil {
			//: HANDOVER_MISMATCH.
			return err
		}
		fu.conn = f
	}
	src, ok := a.u.cfg.Notify.(*listenerSource)
	//: a caller's own source: closed with the unit, the caller's to make again.
	if !ok {
		//: nothing more.
		return nil
	}
	f, err := svcnotify.Detach(src.l)
	//: refused.
	if err != nil {
		//: the listener's refusal.
		return err
	}
	fu.notify, fu.socket = f, src.socket
	//: every descriptor duplicated.
	return nil
}

// handProcs lists the processes the shell holds, by pid: the engine's
// children, and the adopted ones with a handle proving them in the
// incoming image.
func (a *actor) handProcs() []handedProc {
	out := make([]handedProc, 0, len(a.procs))
	//: each process.
	for pid, p := range a.procs {
		hp := handedProc{proc: p, pid: pid, child: a.children[pid]}
		ph, ok := p.(coreproc.ProcessHandle)
		//: an adopted process with a kernel handle.
		if !hp.child && ok {
			hp.witness, _ = ph.Handle()
		}
		out = append(out, hp)
	}
	slices.SortFunc(out, func(x, y handedProc) int { return x.pid - y.pid })
	//: the processes.
	return out
}

// dupFile is a new close-on-exec descriptor on f's object, from 3 on.
func dupFile(f *os.File) (*os.File, error) {
	fd := -1
	var errno syscall.Errno
	raw, err := f.SyscallConn()
	//: no descriptor to lend.
	if err != nil {
		//: HANDOVER_MISMATCH.
		return nil, mismatch(err, -1)
	}
	cerr := raw.Control(func(old uintptr) {
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, old, syscall.F_DUPFD_CLOEXEC, 3)
		fd, errno = int(r), e
	})
	//: refused.
	if cerr != nil || errno != 0 {
		//: HANDOVER_MISMATCH.
		return nil, mismatch(firstOf(cerr, errno), -1)
	}
	//: the copy.
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// mismatch is HANDOVER_MISMATCH naming fd, its cause beside it in the chain:
// the sentinel first, so it is the origin errs.Wrap keeps.
func mismatch(cause error, fd int) error {
	//: the sentinel, the cause in its chain.
	return errs.Wrap(joinCause(coresvc.HandoverMismatch, cause), errs.WrapParams{}, errs.Int("fd", fd))
}

// firstOf is err, or the errno when err is nil and the errno is not 0.
func firstOf(err error, errno syscall.Errno) error {
	//: the first failure.
	if err != nil || errno == 0 {
		//: err.
		return err
	}
	//: the errno.
	return errno
}

// fileIdentity is what fstat reads of f: its number, its type, its device
// and inode.
func fileIdentity(f *os.File) (fileRecord, error) {
	rec := fileRecord{fd: -1}
	var st syscall.Stat_t
	var serr error
	raw, err := f.SyscallConn()
	//: no descriptor to lend.
	if err != nil {
		//: HANDOVER_MISMATCH.
		return rec, mismatch(err, -1)
	}
	cerr := raw.Control(func(fd uintptr) {
		rec.fd = int(fd)
		serr = syscall.Fstat(int(fd), &st)
	})
	//: fstat refused.
	if cerr != nil || serr != nil {
		//: HANDOVER_MISMATCH.
		return rec, mismatch(errors.Join(cerr, serr), rec.fd)
	}
	rec.mode, rec.dev, rec.ino = st.Mode&syscall.S_IFMT, uint64(st.Dev), st.Ino
	//: the record.
	return rec, nil
}

// adoptNotify is the notification source over a socket the hand-over
// carried, at its path.
func adoptNotify(f *os.File, socket string) (coresvc.NotifySource, error) {
	l, err := svcnotify.Adopt(f, socket)
	//: no socket.
	if err != nil {
		//: the listener's refusal.
		return nil, err
	}
	//: the source.
	return &listenerSource{l: l, socket: socket}, nil
}

// restore starts the actor from what Freeze wrote: the model, the store's
// descriptors by id, each process held again, each timer armed for its
// deadline, the group's and the bus name's watches begun again; then the
// reducer reads what the image change hid — a process gone, the group as it
// is, the bus name's owner as a lookup.
func (a *actor) restore(r *restoring) {
	u := a.u
	at := r.m.published.At
	a.m = r.m
	a.m.s = u.s
	a.m.published = snapshot(&a.m)
	a.m.published.Generation, a.m.published.At = a.m.generation, at
	u.mu.Lock()
	u.snap = a.m.published
	u.mu.Unlock()
	a.restoreStore(r.fu)
	lost := a.restoreProcs(r.fu)
	now := u.clock.Now()
	//: each timer armed when frozen, for its deadline.
	for k := range timerCount {
		//: armed.
		if d := a.m.deadlines[k]; !d.IsZero() {
			a.timers[k] = u.clock.NewTimer(d.Sub(now))
			a.armed[k] = a.m.seq[k]
		}
	}
	//: each process that could not be held: gone, its status unknown.
	for _, pid := range lost {
		a.handle(&event{kind: evExit, pid: pid, exit: coreproc.ExitValue{Code: -1}, flags: flagUnknown})
	}
	a.rewatch()
}

// restoreStore holds the store's descriptors again, by id, each watched for
// a hangup as it was; an entry whose descriptor did not come is dropped and
// counted as discarded.
func (a *actor) restoreStore(fu *frozenUnit) {
	m := &a.m
	kept := m.store[:0:0]
	//: each entry, oldest first.
	for _, e := range m.store {
		f := fu.store[e.id]
		delete(fu.store, e.id)
		//: no descriptor for it.
		if f == nil {
			m.storeDiscarded++
			continue
		}
		info, err := f.Stat()
		//: an object fstat cannot read is listed by its name alone.
		if err != nil {
			info = nil
		}
		h := &held{file: f, view: describe(e.name, info)}
		//: FDPOLL= not 0.
		if e.poll {
			h.watched = a.watchHangup(f, e.id)
		}
		h.view.Poll = h.watched
		a.store[e.id] = h
		a.storeOrder = append(a.storeOrder, e.id)
		kept = append(kept, e)
	}
	m.store = kept
	closeFiles(slices.Collect(mapValues(fu.store)))
	clear(fu.store)
	a.publishStore()
}

// mapValues lists a store's descriptors.
func mapValues(m map[uint64]*os.File) func(func(*os.File) bool) {
	//: the iterator.
	return func(yield func(*os.File) bool) {
		//: each descriptor.
		for _, f := range m {
			//: until the caller stops.
			if !yield(f) {
				return
			}
		}
	}
}

// restoreProcs holds each process again and watches it; it returns the pids
// it could not hold.
func (a *actor) restoreProcs(fu *frozenUnit) []int {
	var lost []int
	//: each process.
	for i := range fu.procs {
		hp := &fu.procs[i]
		p, err := a.holdAgain(hp)
		closeFiles([]*os.File{hp.witness})
		hp.witness = nil
		//: gone, or not provably the same.
		if err != nil {
			lost = append(lost, hp.pid)
			continue
		}
		a.watch(p)
		//: still the engine's child.
		if hp.child {
			a.children[hp.pid] = true
		}
	}
	//: the lost.
	return lost
}

// holdAgain is a handle on a frozen unit's process: the outgoing image's
// own when the image did not change; else a child reclaimed, or an adopted
// process attached again whose witness — the handle the outgoing image
// passed — still names that pid, so the pid was not reused meanwhile.
func (a *actor) holdAgain(hp *handedProc) (coreproc.Process, error) {
	//: the same image: the same handle.
	if hp.proc != nil {
		//: as it was.
		return hp.proc, nil
	}
	//: the image's child still.
	if hp.child {
		//: its real status, through the ledger.
		return exec.Reclaim(a.u.ctx, hp.pid)
	}
	att := a.u.cfg.Attacher
	//: nothing to attach with, or nothing proving the pid.
	if att == nil || hp.witness == nil {
		//: lost.
		return nil, coreproc.InvalidSpec
	}
	p, err := att.AttachPID(a.u.ctx, hp.pid)
	//: refused.
	if err != nil {
		//: lost.
		return nil, err
	}
	//: the pid was reused: the process attached is another.
	if !witnessNames(hp.witness, hp.pid) {
		//: lost, the other process untouched.
		return nil, coreproc.InvalidSpec
	}
	//: the same process.
	return p, nil
}

// witnessNames reports whether the pidfd w still names pid: its fdinfo's
// Pid line, -1 or absent once the process was reaped.
func witnessNames(w *os.File, pid int) bool {
	fd := -1
	raw, err := w.SyscallConn()
	//: no descriptor.
	if err != nil {
		//: no.
		return false
	}
	gone(raw.Control(func(v uintptr) { fd = int(v) }))
	info, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	//: unreadable.
	if err != nil {
		//: no.
		return false
	}
	//: each line.
	for line := range strings.SplitSeq(string(info), "\n") {
		//: the pid the pidfd names.
		if v, ok := strings.CutPrefix(line, "Pid:"); ok {
			n, aerr := strconv.Atoi(strings.TrimSpace(v))
			//: the same.
			return aerr == nil && n == pid
		}
	}
	//: no pidfd.
	return false
}

// rewatch begins again the watches the image change ended, for a unit that
// runs: the group's — its baseline the OOM count now, then the group read
// as it is — and the bus name's, whose first answer is a lookup, read with
// the grace, never as a loss (S0e).
func (a *actor) rewatch() {
	u := a.u
	//: a dead unit watches nothing until its next start.
	if isDead(a.m.state) {
		return
	}
	//: the group, read again once watched.
	if a.group != nil && a.startWatch() == nil {
		a.handle(&event{kind: evCgroup})
	}
	//: a Type=dbus unit's name.
	if u.s.typ != coresvc.TypeDbus {
		return
	}
	flags, err := a.startBusWatch()
	//: an answer to read.
	if err == nil && flags != 0 {
		a.handle(&event{kind: evBusName, flags: flags})
	}
}

// stopHanded stops in order the processes of a unit no Resume took: SIGTERM,
// the grace, SIGKILL, each reaped where it is a child. A process that
// cannot be held is left alone: nothing proves it is the unit's.
//
// Goroutine lifecycle: one per process, each ended by its process's end;
// joined before stopHanded returns.
func stopHanded(ctx context.Context, grace time.Duration, procs []handedProc) {
	var wg sync.WaitGroup
	//: each process, at once.
	for i := range procs {
		p := heldProcess(ctx, &procs[i])
		//: nothing proves it.
		if p == nil {
			continue
		}
		wg.Go(stopperOf(ctx, p, grace))
	}
	wg.Wait()
}

// heldProcess is the handle of a process a unit ran: the outgoing image's,
// or — the image having changed — the child held again, nil when it cannot be.
func heldProcess(ctx context.Context, hp *handedProc) coreproc.Process {
	p := hp.proc
	//: the image changed: held again first.
	if p == nil && hp.child {
		reclaimed, err := exec.Reclaim(ctx, hp.pid)
		gone(err)
		p = reclaimed
	}
	//: the handle, or nil.
	return p
}

// stopperOf is the stop of one process, its end awaited.
func stopperOf(ctx context.Context, p coreproc.Process, grace time.Duration) func() {
	//: SIGTERM, the grace, SIGKILL, the reap.
	return func() {
		gone(p.Stop(ctx, grace, sigTERM))
		_, werr := p.Wait()
		gone(werr)
	}
}
