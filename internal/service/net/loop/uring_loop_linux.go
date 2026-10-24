//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"cmp"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The operations a CQE's user_data names, in its top byte.
const (
	udRecv uint64 = iota + 1
	udSend
	udCancel
	udClose
	udAccept
	udWake
	udFan
	udAcceptCancel
	udOrphanClose
)

const (
	// acceptDepth is how many single-shot accepts a ring keeps in flight per
	// listener.
	acceptDepth int = 32
	// acceptReserve is the free direct-descriptor entries a ring keeps: under
	// it the ring stops accepting, and it resumes at twice as many.
	acceptReserve int = 16
	// starvedCap bounds the connections waiting for a provided buffer.
	starvedCap int = 65536
	// reapRounds bounds the CQ batches one ring reaps per iteration, so one
	// ring cannot starve the others forever.
	reapRounds int = 4
	// staleFill is the byte a provided buffer is overwritten with under the
	// fill hook: a stale byte read back shows as it.
	staleFill byte = 0xAA
	// genMask keeps the 24 bits of a generation user_data carries.
	genMask uint32 = 1<<24 - 1
)

// udPack builds a user_data: the operation, 24 bits of generation, the slot.
func udPack(op uint64, gen, slot uint32) uint64 {
	//: op in the top byte.
	return op<<56 | uint64(gen&genMask)<<32 | uint64(slot)
}

// udOp is the operation a user_data names, in its top byte.
func udOp(ud uint64) uint64 {
	//: the top byte.
	return ud >> 56
}

// udGen is the 24 bits of generation a user_data carries.
func udGen(ud uint64) uint32 {
	//: bits 32 to 55.
	return uint32(ud>>32) & genMask
}

// udSlot is the slot a user_data names, in its low half.
func udSlot(ud uint64) uint32 {
	//: the low 32 bits.
	return uint32(ud)
}

// uRing is one ring of an io_uring loop.
type uRing struct {
	ul *uLoop
	u  *uring
	r  *ring
	// slotOf maps a direct-descriptor index to its slot, -1 when none.
	slotOf []int32
	// armed is how many single-shot accepts each listener has in flight;
	// paused a
	// ring that stopped accepting under its reserve.
	armed  []int
	paused bool
	// starved are slots whose recv ran out of buffers, re-armed as buffers
	// come back; recycle the buffers the batch handed out.
	starved []int32
	recycle []uint16
	// inRing is how many provided buffers the kernel holds: a starved recv
	// is re-armed only while some remain, whether or not this batch
	// returned any.
	inRing int
	// lastAccept is the tick of the ring's last accept completion; stalls
	// counts the watchdog's re-arms.
	lastAccept int64
	stalls     int
}

// uLoop is one loop on io_uring: one thread owns every ring.
type uLoop struct {
	lp    *loop
	rings []*uRing
	lfds  []int
	hooks loopHooks
	// wakefd is the Post eventfd, polled by the primary ring; fanfd the
	// secondary rings' completion eventfd, polled by the primary too.
	wakefd int
	fanfd  int
	// wakeMu orders a wake against the eventfd's close.
	wakeMu   sync.Mutex
	evClosed bool
	drainReq atomic.Bool
	stopReq  atomic.Bool
	tid      int
	// flags is the setup flags the probe settled on; stage the stage reached.
	flags uint32
	stage string
}

// uringRunner runs the group's io_uring loops.
type uringRunner struct {
	loops []*uLoop
	wg    sync.WaitGroup
	fin   chan struct{}
	lfds  []int
}

// drain implements runner.
func (ur *uringRunner) drain() {
	//: told, then woken.
	for _, ul := range ur.loops {
		ul.drainReq.Store(true)
		ul.wake()
	}
}

// stop implements runner.
func (ur *uringRunner) stop() {
	//: told, then woken.
	for _, ul := range ur.loops {
		ul.stopReq.Store(true)
		ul.wake()
	}
}

// wait implements runner.
func (ur *uringRunner) wait() { <-ur.fin }

// done implements runner.
func (ur *uringRunner) done() <-chan struct{} { return ur.fin }

// wake writes the Post eventfd.
func (ul *uLoop) wake() {
	ul.wakeMu.Lock()
	defer ul.wakeMu.Unlock()
	//: a stopped loop's number may be another descriptor's now.
	if ul.evClosed || ul.wakefd < 0 {
		//: nothing.
		return
	}
	one := new(uint64(1))
	_, _, errno := syscall.RawSyscall(syscall.SYS_WRITE, uintptr(ul.wakefd), uintptr(unsafe.Pointer(one)), 8)
	//: a full counter already wakes the loop; nothing else can fail here.
	swallowErr(errno)
}

// startIOURing starts g's loops on io_uring. A loop whose probe fails stops
// the others; the error names the stage and the kernel's answer.
//
// Goroutine lifecycle: it launches one goroutine per loop, locked to its
// thread, which returns once the loop stopped or drained; one reaper that
// waits for them all and closes fin; and, once the group runs, one goroutine
// that waits on fin, closes the listeners and returns.
func startIOURing(g *Group, cfg *Config, hooks loopHooks) error {
	fin := make(chan struct{})
	runner := &uringRunner{fin: fin}
	//: TCP_NODELAY on the listener, inherited by every accepted socket; a
	//: socket that refuses it still serves.
	for _, l := range cfg.Listeners {
		swallowErr(syscall.SetsockoptInt(l.FD, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1))
		runner.lfds = append(runner.lfds, l.FD)
	}
	ready := make(chan error, g.sz.loops)
	//: one thread per loop, each building its rings after it locked itself.
	for i := range g.sz.loops {
		lp := newLoop(g, *cfg, i, mmapAnon)
		ul := &uLoop{lp: lp, lfds: runner.lfds, hooks: hooks, wakefd: -1, fanfd: -1}
		runner.loops = append(runner.loops, ul)
		g.loops = append(g.loops, lp)
		runner.wg.Add(1)
		go ul.run(&runner.wg, ready, g.sz.rings)
	}
	var first error
	//: every loop reports.
	for range g.sz.loops {
		//: the first failure is the one reported.
		if err := <-ready; err != nil && first == nil {
			first = err
		}
	}
	g.ctl = runner
	//: the reaper owns fin: it is the one goroutine that closes it, once,
	//: after the last loop returned.
	go func() {
		runner.wg.Wait()
		close(fin)
	}()
	//: one loop failed: the group does not run half on io_uring.
	if first != nil {
		runner.stop()
		runner.wait()
		g.loops = nil
		//: refused, the listeners still the caller's to hand to epoll.
		return first
	}
	//: the listeners are the group's now: closed once every loop returned.
	go func() {
		<-fin
		closeFDs(runner.lfds)
	}()
	g.backend = corenet.LoopBackendIOURing
	g.stage = runner.loops[0].stage
	//: running.
	return nil
}

// probeError is a probe stage's failure.
func probeError(stage string, errno syscall.Errno) error {
	//: named.
	return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
		errs.String("stage", stage), errs.String("reason", errno.Error()))
}

// run is the loop's thread.
func (ul *uLoop) run(wg *sync.WaitGroup, ready chan<- error, rings int) {
	defer wg.Done()
	runtime.LockOSThread()
	ul.tid = syscall.Gettid()
	ul.lp.threadOK = threadCheck(ul.tid)
	//: every ring is created on this thread, after the lock.
	if err := ul.setup(rings); err != nil {
		ul.teardown()
		ready <- err
		//: never served.
		return
	}
	ready <- nil
	ul.serve()
	ul.teardown()
}

// setup runs the staged probe on this thread, then builds the rings.
func (ul *uLoop) setup(rings int) error {
	flags, err := ul.probe()
	//: stages 1 to 3 refused.
	if err != nil {
		//: named by the stage.
		return err
	}
	//: stage 4 and 5 run on every ring as it is built, the self-test once.
	if err := ul.selfTest(flags); err != nil {
		//: named by the test.
		return err
	}
	ul.stage = "self_test"
	//: built, or refused by the ring that could not be.
	return ul.build(rings, flags)
}

// setupFlags is the full flag set the probe tries first: DEFER_TASKRUN
// replaces COOP_TASKRUN when the hook asks for it.
func (ul *uLoop) setupFlags() uint32 {
	//: DEFER_TASKRUN replaces COOP_TASKRUN when asked for.
	if ul.hooks.deferTaskrun {
		//: deferred.
		return setupDeferTaskrun | setupSingleIssuer | setupSubmitAll
	}
	//: cooperative.
	return setupCoopTaskrun | setupSingleIssuer | setupSubmitAll
}

// probe runs stages 1 to 3: a ring with the full flag set, one with fewer on
// an older kernel, and REGISTER_PROBE for every op this backend issues. It
// returns the flags that passed.
func (ul *uLoop) probe() (uint32, error) {
	probed, flags, err := ul.openStaged(ul.setupFlags())
	//: no ring at all.
	if err != nil {
		//: named by the step.
		return 0, err
	}
	ul.flags = flags
	ul.stage = "setup"
	ok, perr := probed.supports(opAccept, opRecv, opSend, opAsyncCancel, opClose, opPollAdd, opRead, opNop)
	//: stage 3: the ops this backend issues.
	if perr != 0 || !ok || ul.hooks.failStage == "probe" {
		probed.close()
		//: named.
		return 0, probeError("register_probe", orErrno(perr, syscall.EOPNOTSUPP))
	}
	ul.stage = "register_probe"
	probed.close()
	//: the flags every ring is built with.
	return flags, nil
}

// openStaged runs stages 1 and 2: a ring with flags, and, when an older
// kernel refuses a flag it does not know, one without SINGLE_ISSUER. It
// returns the ring and the flags it was opened with.
func (ul *uLoop) openStaged(flags uint32) (*uring, uint32, error) {
	//: stage 1: the full flag set.
	if ul.hooks.failStage == "setup" {
		//: injected.
		return nil, 0, probeError("setup", syscall.EPERM)
	}
	opened, step, errno := openURing(probeEntries, flags)
	//: stage 2: an older kernel refuses a flag it does not know.
	if errno == syscall.EINVAL && step == "setup" && !ul.hooks.deferTaskrun {
		flags = setupCoopTaskrun | setupSubmitAll
		opened, step, errno = openURing(probeEntries, flags)
	}
	//: no ring at all.
	if errno != 0 {
		//: named by the step.
		return nil, 0, probeError(step, errno)
	}
	//: opened.
	return opened, flags, nil
}

// build creates the Post queue, the two eventfds and every ring, and arms
// the primary's polls.
func (ul *uLoop) build(rings int, flags uint32) error {
	lp := ul.lp
	lp.post = newPostQueue(ul.wake)
	wfd, _, e1 := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	ffd, _, e2 := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	//: no eventfd.
	if e1 != 0 || e2 != 0 {
		//: refused.
		return probeError("eventfd", orErrno(e1, e2))
	}
	ul.wakefd, ul.fanfd = int(wfd), int(ffd)
	//: each ring: its own io_uring, table, buffers, accepts.
	for i := range rings {
		rr, err := ul.newRing(i, flags)
		//: one ring that cannot be built fails the loop.
		if err != nil {
			//: named.
			return err
		}
		ul.rings = append(ul.rings, rr)
		lp.rings = append(lp.rings, rr.r)
	}
	primary := ul.rings[0]
	primary.pollFD(ul.wakefd, udWake)
	//: secondaries signal the primary through one registered eventfd.
	if len(ul.rings) > 1 {
		primary.pollFD(ul.fanfd, udFan)
	}
	//: ready.
	return nil
}

// orErrno returns the first non-zero errno.
func orErrno(a, b syscall.Errno) syscall.Errno {
	//: the first that says something.
	return cmp.Or(a, b)
}

// newRing builds ring i: its io_uring, its sparse table, its buffers, and
// its accepts.
func (ul *uLoop) newRing(i int, flags uint32) (*uRing, error) {
	u, step, errno := openURing(uringEntries, flags)
	//: refused.
	if errno != 0 {
		//: named.
		return nil, probeError(step, errno)
	}
	lp := ul.lp
	rr := &uRing{ul: ul, u: u, armed: make([]int, len(ul.lfds))}
	rr.r = newRing(lp, i, rr)
	bufs, size := ul.bufShape()
	//: stage 4: provided buffers and the sparse table.
	if errno := u.setupBuffers(bufs, size); errno != 0 || ul.hooks.failStage == "buf_ring" {
		u.close()
		//: named.
		return nil, probeError("buf_ring", orErrno(errno, syscall.EOPNOTSUPP))
	}
	table := uint32(lp.sz.tableSize)
	//: the table the accepts allocate into.
	if errno := u.registerSparse(table); errno != 0 || ul.hooks.failStage == "sparse_files" {
		u.close()
		//: named.
		return nil, probeError("sparse_files", orErrno(errno, syscall.EOPNOTSUPP))
	}
	rr.inRing = int(bufs)
	rr.slotOf = slices.Repeat([]int32{-1}, int(table))
	//: a secondary's completions wake the primary.
	if err := ul.registerFan(i, u); err != nil {
		u.close()
		//: named.
		return nil, err
	}
	rr.armAccepts()
	//: built.
	return rr, nil
}

// bufShape is a ring's provided buffers: how many, and their size, or a
// test's tiny buffer ring.
func (ul *uLoop) bufShape() (bufs, size uint32) {
	//: a test's tiny buffer ring.
	if ul.hooks.bufs > 0 {
		//: as the hook asks, 64 bytes at least.
		return ul.hooks.bufs, max(ul.hooks.bufSize, 64)
	}
	//: the defaults.
	return defaultBufs, defaultBufSize
}

// registerFan registers the secondaries' eventfd as ring i's CQ notifier,
// never the Post eventfd; the primary, ring 0, needs none.
func (ul *uLoop) registerFan(i int, u *uring) error {
	//: the primary is the one woken.
	if i == 0 {
		//: nothing to register.
		return nil
	}
	fd := new(int32(ul.fanfd))
	//: registered as the ring's CQ notifier.
	if errno := uringRegister(u.fd, regEventFD, unsafe.Pointer(fd), 1); errno != 0 {
		//: named.
		return probeError("register_eventfd", errno)
	}
	//: registered.
	return nil
}

// pollFD arms a multishot POLLIN on fd, tagged op.
func (rr *uRing) pollFD(fd int, op uint64) {
	s := rr.u.get()
	//: an SQ that never drained: counted, retried at the next tick.
	if s == nil {
		rr.r.st.retries++
		//: dropped.
		return
	}
	prepPollMulti(s, fd, udPack(op, 0, 0))
}

// armAccepts keeps up to acceptDepth single-shot accepts in flight per
// listener, never more than the entries left above the reserve. A multishot
// accept into a direct-descriptor table keeps accepting the whole listen
// backlog before the loop can see the reserve reached: past the table's last
// entry the kernel accepts the connection, cannot install it, and drops it
// (ENFILE), which CI saw on a 6.11 kernel. Single-shot accepts, each holding
// one entry until it completes, cannot overshoot.
func (rr *uRing) armAccepts() {
	//: a paused or draining ring accepts nothing.
	if rr.paused || rr.r.draining || rr.ul.lp.draining {
		//: nothing.
		return
	}
	room := rr.acceptRoom()
	//: each listener, up to its depth, within the room.
	for i, fd := range rr.ul.lfds {
		//: topped up one SQE at a time.
		for rr.armed[i] < acceptDepth && room > 0 {
			s := rr.u.get()
			//: no room in the SQ.
			if s == nil {
				rr.r.st.retries++
				//: next time.
				return
			}
			prepAccept(s, fd, udPack(udAccept, 0, uint32(i)), false)
			rr.armed[i]++
			room--
		}
	}
}

// acceptRoom is how many more accepts may be in flight: the free entries
// above the reserve, less those the accepts in flight will take.
func (rr *uRing) acceptRoom() int {
	table := rr.ul.lp.sz.tableSize
	reserve := max(min(acceptReserve, table/8), 1)
	inFlight := 0
	//: every listener's.
	for _, n := range rr.armed {
		inFlight += n
	}
	//: never negative.
	return max(table-rr.used()-reserve-inFlight+1, 0)
}

// cancelAccepts cancels every accept in flight; their completions disarm
// them.
func (rr *uRing) cancelAccepts() {
	//: each listener with an accept in flight.
	for i, n := range rr.armed {
		//: none.
		if n == 0 {
			continue
		}
		s := rr.u.get()
		//: no room.
		if s == nil {
			rr.r.st.retries++
			//: next time.
			return
		}
		prepCancelUserData(s, udPack(udAccept, 0, uint32(i)), udPack(udAcceptCancel, 0, uint32(i)))
	}
}

// used is how many direct-descriptor entries the ring holds, draining slots
// included: an index stays the kernel's until its close completes.
func (rr *uRing) used() int {
	//: every slot taken and not yet released.
	return len(rr.r.conns) - len(rr.r.free)
}

// pace pauses accepting under the reserve and resumes at twice it. The
// reserve is acceptReserve entries, an eighth of a smaller table, and at
// least one: a full table pauses whatever its size.
func (rr *uRing) pace() {
	table := rr.ul.lp.sz.tableSize
	reserve := min(acceptReserve, table/8)
	free := table - rr.used()
	//: under the reserve: stop.
	if !rr.paused && free < max(reserve, 1) {
		rr.paused = true
		rr.r.st.pauses++
		rr.cancelAccepts()
		//: paused.
		return
	}
	//: twice the reserve: resume.
	if rr.paused && free >= max(2*reserve, 1) {
		rr.paused = false
	}
	rr.armAccepts()
}

// serve is the loop: one wait on the primary, every ring reaped, one batch.
func (ul *uLoop) serve() {
	lp := ul.lp
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until stopped, or drained to the last connection.
	for {
		ul.batch(&tick)
		//: finished.
		if ul.stopReq.Load() || (lp.draining && ul.live() == 0) {
			//: out.
			break
		}
	}
	ul.closeAll()
}

// batch is one iteration: the wait on the primary, every ring reaped, the
// drain, the timers, OnBatchEnd, and the buffers returned.
func (ul *uLoop) batch(tick *kernelTimespec) {
	lp := ul.lp
	//: secondaries submit without waiting; the primary submits and waits.
	for _, rr := range ul.rings[1:] {
		rr.u.submit()
	}
	ul.rings[0].u.wait(tick)
	started := time.Now()
	lp.tick = lp.now()
	//: every ring, whoever woke the loop.
	for _, rr := range ul.rings {
		rr.reapAll()
	}
	ul.control()
	//: timers, pacing and the watchdog, ring by ring.
	for _, rr := range ul.rings {
		rr.r.wheel.advance(lp.tick, rr.r.expire)
		rr.pace()
		rr.watchdog()
	}
	lp.h.OnBatchEnd(lp)
	lp.endBatch(started)
	//: buffers go back only now: OnBatchEnd has returned.
	for _, rr := range ul.rings {
		rr.returnBuffers()
		rr.r.st.retries = rr.u.retries
	}
}

// live is the loop's open connections and slots still held by the kernel.
func (ul *uLoop) live() int {
	n := 0
	//: every ring.
	for _, rr := range ul.rings {
		n += rr.used()
	}
	//: held.
	return n
}

// reapAll reaps the ring's CQ until it is empty, a batch at a time, and
// flushes an overflow the kernel held back.
func (rr *uRing) reapAll() {
	//: bounded by batches, so one ring cannot starve the others forever.
	for range reapRounds {
		n := rr.u.reap(rr.ul.lp.sz.cqBatch, rr.handle)
		rr.r.st.completions += uint64(n)
		//: the kernel held completions back: a submit flushes them.
		if rr.u.overflowed() {
			rr.u.submit()
			continue
		}
		//: empty.
		if n == 0 {
			//: done.
			return
		}
	}
}

// handle serves one completion.
func (rr *uRing) handle(c *cqe) {
	ud := c.userData
	//: by operation.
	switch op := udOp(ud); op {
	//: a connection.
	case udAccept:
		rr.onAccept(int(udSlot(ud)), c)
	//: a connection's own operation.
	case udRecv, udSend, udCancel, udClose:
		rr.onConnOp(op, int32(udSlot(ud)), udGen(ud), c)
	//: posted work.
	case udWake:
		rr.onWake(c)
	//: a secondary has completions; every ring is reaped anyway.
	case udFan:
		rr.onFan(c)
	//: a refused connection's index came back.
	case udOrphanClose, udAcceptCancel:
	}
}

// onConnOp serves the completion of an operation slot issued.
func (rr *uRing) onConnOp(op uint64, slot int32, gen uint32, c *cqe) {
	//: by operation.
	switch op {
	//: received bytes.
	case udRecv:
		rr.onRecv(slot, gen, c)
	//: a send finished.
	case udSend:
		rr.onSend(slot, gen, c)
	//: a cancel finished.
	case udCancel:
		rr.opDone(slot, gen)
	//: a close finished.
	case udClose:
		rr.onClose(slot, gen)
	}
}

// onAccept takes a new direct descriptor into a slot and arms its recv.
func (rr *uRing) onAccept(li int, c *cqe) {
	r, lp := rr.r, rr.ul.lp
	rr.lastAccept = lp.tick
	rr.stalls = 0
	//: a single-shot accept is final: one less in flight, topped up below.
	if c.flags&cqeFMore == 0 && li < len(rr.armed) && rr.armed[li] > 0 {
		rr.armed[li]--
	}
	defer rr.armAccepts()
	//: an error: the table is full, or the listener failed.
	if c.res < 0 {
		rr.acceptFailed(c.res)
		//: nothing accepted.
		return
	}
	index := uint32(c.res)
	//: draining, or past MaxConns: accepted and closed.
	if rr.refuses(index) {
		lp.grp.rejected.Add(1)
		rr.orphanClose(index)
		//: refused.
		return
	}
	slot, ok := r.take(int32(index))
	//: no slot: the table is the kernel's size, so this is a race with a
	//: release; refused.
	if !ok {
		lp.grp.rejected.Add(1)
		rr.orphanClose(index)
		//: refused.
		return
	}
	rr.slotOf[index] = slot
	rr.armRecv(slot)
	r.opened(slot)
}

// acceptFailed pauses the ring when an accept failed for want of entries:
// closes make room.
func (rr *uRing) acceptFailed(res int32) {
	//: out of entries: pause until closes make room.
	if res == -int32(syscall.ENFILE) || res == -int32(syscall.EMFILE) {
		rr.paused = true
		rr.r.st.pauses++
	}
}

// refuses reports whether a connection accepted on index is closed at once:
// the loop or the ring drains, MaxConns is reached, or the index is past the
// table.
func (rr *uRing) refuses(index uint32) bool {
	lp := rr.ul.lp
	//: any of the four.
	return lp.draining || rr.r.draining || (lp.sz.maxConns > 0 && lp.grp.live.Load() >= int64(lp.sz.maxConns)) || int(index) >= len(rr.slotOf)
}

// orphanClose closes a direct descriptor no slot owns.
func (rr *uRing) orphanClose(index uint32) {
	s := rr.u.get()
	//: no room: the entry leaks until the ring closes, counted.
	if s == nil {
		rr.r.st.retries++
		//: dropped.
		return
	}
	prepCloseDirect(s, index, udPack(udOrphanClose, 0, index))
}

// armRecv arms slot's multishot recv, counted.
func (rr *uRing) armRecv(slot int32) {
	c := &rr.r.conns[slot]
	s := rr.u.get()
	//: no room: the slot fails rather than sits deaf.
	if s == nil {
		rr.r.st.retries++
		rr.r.fail(slot, rErrno, int32(syscall.EBUSY))
		//: closed.
		return
	}
	prepRecv(s, uint32(c.fd), udPack(udRecv, c.gen, uint32(slot)))
	c.ops++
}

// owns reports whether user_data's generation is slot's.
func (rr *uRing) owns(slot int32, gen uint32) bool {
	//: in the table, and the same generation.
	return int(slot) < len(rr.r.conns) && rr.r.conns[slot].gen&genMask == gen
}

// onRecv delivers received bytes and keeps the multishot armed.
func (rr *uRing) onRecv(slot int32, gen uint32, c *cqe) {
	more := c.flags&cqeFMore != 0
	//: a buffer, whatever else, goes back after the batch.
	if c.flags&cqeFBuffer != 0 {
		rr.recycle = append(rr.recycle, uint16(c.flags>>cqeBufferShift))
		rr.inRing--
	}
	//: a slot is never released with an operation in flight, so this is a
	//: kernel or bookkeeping error; the buffer is returned, nothing else.
	if !rr.owns(slot, gen) {
		//: dropped.
		return
	}
	//: the multishot ended: one operation less.
	if !more {
		rr.r.conns[slot].ops--
	}
	//: by result.
	switch {
	//: bytes, in buffer bid.
	case c.res > 0 && c.flags&cqeFBuffer != 0:
		rr.recvBytes(slot, uint16(c.flags>>cqeBufferShift), c.res, more)
	//: out of buffers: re-armed once some come back.
	case c.res == -int32(errnoENOBUFS):
		rr.recvStarved(slot)
	//: the peer finished sending: what is written still leaves.
	case c.res == 0:
		rr.recvEOF(slot)
	//: cancelled: by this slot's close, or by nobody this loop asked. A
	//: connection still open re-arms, so a cancel it never asked for cannot
	//: leave it deaf.
	case c.res == -int32(errnoECANCELED):
		rr.recvCancelled(slot, more)
	//: the socket failed.
	default:
		rr.failLive(slot, -c.res)
	}
	rr.maybeClose(slot)
}

// recvCancelled re-arms the recv of a connection still open whose recv was
// cancelled and ended.
func (rr *uRing) recvCancelled(slot int32, more bool) {
	//: open, and the recv ended: armed again.
	if !more && rr.r.conns[slot].state == stOpen {
		rr.armRecv(slot)
	}
}

// recvBytes delivers n bytes of buffer bid to an open connection, and
// re-arms its recv when the multishot ended.
func (rr *uRing) recvBytes(slot int32, bid uint16, n int32, more bool) {
	//: only an open connection reads.
	if rr.r.conns[slot].state == stOpen {
		rr.r.deliver(slot, rr.u.buf(bid)[:n])
	}
	//: re-armed unless the connection is going.
	if !more && rr.r.conns[slot].state == stOpen {
		rr.armRecv(slot)
	}
}

// recvStarved queues an open connection whose recv ran out of buffers, to
// be re-armed as buffers come back; past the bounded queue it fails.
func (rr *uRing) recvStarved(slot int32) {
	rr.r.st.starved++
	open := rr.r.conns[slot].state == stOpen
	//: a bounded queue: past it the connection fails.
	if open && len(rr.starved) < starvedCap {
		rr.starved = append(rr.starved, slot)
	} else if open {
		rr.r.fail(slot, rErrno, int32(errnoENOBUFS))
	}
}

// recvEOF marks an open connection's peer done: it settles at the end of the
// batch, its output still leaving.
func (rr *uRing) recvEOF(slot int32) {
	cc := &rr.r.conns[slot]
	//: an open connection settles at the end of the batch.
	if cc.state == stOpen {
		cc.flags |= fPeerDone
		rr.r.markDirty(slot)
	}
}

// failLive fails slot with errno unless it already finished: only once.
func (rr *uRing) failLive(slot, errno int32) {
	state := rr.r.conns[slot].state
	//: only once.
	if state == stOpen || state == stClosing {
		rr.r.fail(slot, rErrno, errno)
	}
}

// onSend advances slot's flight, resubmitting what a short send left.
func (rr *uRing) onSend(slot int32, gen uint32, c *cqe) {
	//: see onRecv.
	if !rr.owns(slot, gen) {
		//: dropped.
		return
	}
	cc := &rr.r.conns[slot]
	cc.ops--
	//: by result.
	switch {
	//: some or all of it left.
	case c.res > 0:
		//: a short send is in flight again, its close not due yet.
		if rr.sendProgress(slot, c.res) {
			//: in flight again.
			return
		}
	//: cancelled: a closing slot is done with it; one still alive sends the
	//: same flight again, so a cancel it never asked for loses no byte.
	case c.res == -int32(errnoECANCELED):
		cc.flags &^= fSending
		//: alive: resubmitted, in flight again.
		if cc.state == stOpen || cc.state == stClosing {
			rr.sendFlight(slot)
			//: in flight.
			return
		}
	//: the socket failed.
	default:
		cc.flags &^= fSending
		rr.failLive(slot, -c.res)
	}
	rr.maybeClose(slot)
}

// sendProgress records n bytes of slot's flight sent. A short send resubmits
// the rest of the same flight, never grown, and reports true; otherwise the
// flight is released and an alive connection is flushed again.
func (rr *uRing) sendProgress(slot, n int32) bool {
	r := rr.r
	cc := &r.conns[slot]
	cc.flight.off += n
	alive := cc.state == stOpen || cc.state == stClosing
	//: a short send: the rest of the same flight, never grown.
	if cc.flight.off < cc.flight.len && alive {
		rr.sendFlight(slot)
		//: in flight again.
		return true
	}
	rr.ul.lp.slab.free(cc.flight)
	cc.flight = chunkRef{idx: noChunk}
	cc.flags &^= fSending
	//: alive: more output may wait, and a producer for room.
	if alive {
		r.sent(slot, int(n))
		r.markDirty(slot)
	}
	//: settled.
	return false
}

// opDone counts a cancel's completion.
func (rr *uRing) opDone(slot int32, gen uint32) {
	//: see onRecv.
	if !rr.owns(slot, gen) {
		//: dropped.
		return
	}
	rr.r.conns[slot].ops--
	rr.maybeClose(slot)
}

// maybeClose closes a dead slot's direct descriptor once no operation of it
// remains in flight: only then can the kernel not hand the index out again
// while a completion for it is still coming.
func (rr *uRing) maybeClose(slot int32) {
	c := &rr.r.conns[slot]
	//: alive, or still busy in the kernel.
	if c.state != stDead || c.ops != 0 {
		//: later.
		return
	}
	s := rr.u.get()
	//: no room: retried at the next settle of the slot.
	if s == nil {
		rr.r.st.retries++
		//: later.
		return
	}
	prepCloseDirect(s, uint32(c.fd), udPack(udClose, c.gen, uint32(slot)))
	c.ops = 1
	c.state = stFree
}

// onClose releases the slot whose direct descriptor closed.
func (rr *uRing) onClose(slot int32, gen uint32) {
	//: see onRecv.
	if !rr.owns(slot, gen) {
		//: dropped.
		return
	}
	c := &rr.r.conns[slot]
	index := c.fd
	//: the index is free in the kernel and here.
	if index >= 0 && int(index) < len(rr.slotOf) {
		rr.slotOf[index] = -1
	}
	rr.r.release(slot)
}

// onWake runs the posted operations.
func (rr *uRing) onWake(c *cqe) {
	ul := rr.ul
	var buf [8]byte
	_, _, errno := syscall.RawSyscall(syscall.SYS_READ, uintptr(ul.wakefd), uintptr(unsafe.Pointer(&buf[0])), 8)
	//: the counter is reset by the read; its value does not matter.
	swallowErr(errno)
	//: the consumer is the loop's thread, checked under loopdebug.
	if ul.lp.threadOK != nil && !ul.lp.threadOK() {
		panic("net/loop: Post consumer off its loop's thread")
	}
	ul.lp.post.drain(ul.lp)
	//: the multishot poll ended: armed again.
	if c.flags&cqeFMore == 0 {
		rr.pollFD(ul.wakefd, udWake)
	}
}

// onFan resets the secondaries' eventfd.
func (rr *uRing) onFan(c *cqe) {
	var buf [8]byte
	_, _, errno := syscall.RawSyscall(syscall.SYS_READ, uintptr(rr.ul.fanfd), uintptr(unsafe.Pointer(&buf[0])), 8)
	//: the counter is reset by the read; its value does not matter.
	swallowErr(errno)
	//: the multishot poll ended: armed again.
	if c.flags&cqeFMore == 0 {
		rr.pollFD(rr.ul.fanfd, udFan)
	}
}

// returnBuffers gives the batch's buffers back to the kernel, then re-arms
// the recvs that ran out, as many as the kernel holds buffers for.
func (rr *uRing) returnBuffers() {
	//: a test scrambles the order, so a bid must match its bytes whatever
	//: the ring's order.
	if rr.ul.hooks.shuffle {
		slices.Reverse(rr.recycle)
	}
	rr.giveBack()
	rr.rearmStarved()
}

// giveBack adds each buffer the batch handed out back to the ring, filled
// with staleFill under test, and publishes them once.
func (rr *uRing) giveBack() {
	u := rr.u
	//: each buffer once, filled under test.
	for _, bid := range rr.recycle {
		//: a stale byte would show as staleFill.
		if rr.ul.hooks.fill {
			fillStale(u.buf(bid))
		}
		u.addBuf(bid)
	}
	//: published once for the batch.
	if len(rr.recycle) > 0 {
		u.publishBufs()
		rr.inRing += len(rr.recycle)
		rr.recycle = rr.recycle[:0]
	}
}

// fillStale overwrites b with staleFill.
func fillStale(b []byte) {
	//: every byte.
	for i := range b {
		b[i] = staleFill
	}
}

// rearmStarved re-arms one starved recv per buffer the kernel holds, the
// oldest first; a recv re-armed on an empty ring would only fail again.
func (rr *uRing) rearmStarved() {
	k := 0
	//: the oldest first, as many as the ring holds buffers for.
	for k < len(rr.starved) && k < rr.inRing {
		slot := rr.starved[k]
		k++
		//: re-armed only if still open.
		if rr.r.conns[slot].state == stOpen {
			rr.armRecv(slot)
		}
	}
	rr.starved = rr.starved[:copy(rr.starved, rr.starved[k:])]
}

// flush implements driver: the output becomes the flight, and the flight is
// sent. The flight never grows while the kernel holds it.
func (rr *uRing) flush(r *ring, slot int32) {
	c := &r.conns[slot]
	//: a flight is still the kernel's: its completion flushes again.
	if c.flags&fSending != 0 || c.out.empty() || c.out.off == c.out.len {
		//: later, or nothing.
		return
	}
	c.flight, c.out = c.out, chunkRef{idx: noChunk}
	rr.sendFlight(slot)
}

// sendFlight submits the unsent rest of slot's flight.
func (rr *uRing) sendFlight(slot int32) {
	c := &rr.r.conns[slot]
	s := rr.u.get()
	//: no room: the connection fails rather than stalls silently.
	if s == nil {
		rr.r.st.retries++
		rr.r.fail(slot, rErrno, int32(syscall.EBUSY))
		//: closed.
		return
	}
	prepSend(s, uint32(c.fd), rr.ul.lp.slab.data(c.flight), udPack(udSend, c.gen, uint32(slot)))
	c.ops++
	c.flags |= fSending
}

// shutdown implements driver: every operation of the slot is cancelled,
// unlinked; the close follows once the count reaches zero.
func (rr *uRing) shutdown(r *ring, slot int32) {
	c := &r.conns[slot]
	//: nothing in flight: the close goes now.
	if c.ops == 0 {
		rr.maybeClose(slot)
		//: closing.
		return
	}
	s := rr.u.get()
	//: no room: the close waits for the operations to end by themselves.
	if s == nil {
		r.st.retries++
		//: later.
		return
	}
	prepCancelFixed(s, uint32(c.fd), udPack(udCancel, c.gen, uint32(slot)))
	c.ops++
}

// unread implements driver. A direct descriptor has no process descriptor
// to ask SIOCINQ of, so io_uring never evicts blind: it reports unread.
func (rr *uRing) unread(_ *ring, _ int32) bool {
	//: conservative.
	return true
}

// watchdog re-arms an accept that looks stalled, and drains a ring whose
// accept stays stalled. It acts on an observable stall only: the accept armed
// and not paused, the listener's queue not empty, and no accept completion
// for watchdogTicks.
func (rr *uRing) watchdog() {
	lp := rr.ul.lp
	//: nothing to watch.
	if rr.paused || rr.r.draining || lp.draining || lp.tick-rr.lastAccept < watchdogTicks {
		//: healthy, or not judged.
		return
	}
	//: any listener with a queue and an armed accept.
	for i, fd := range rr.ul.lfds {
		//: unarmed accepts are pace's.
		if rr.armed[i] == 0 || listenerQueue(fd) == 0 {
			continue
		}
		rr.r.st.watchdog++
		rr.stalls++
		rr.lastAccept = lp.tick
		//: past the re-arms: the ring drains; the others keep accepting.
		if rr.stalls > watchdogFires {
			rr.r.draining = true
			rr.cancelAccepts()
			//: drained.
			return
		}
		rr.cancelAccepts()
		//: re-armed when the cancels complete.
		return
	}
}

// listenerQueue is the listening socket's accept queue (TCP_INFO's unacked
// on a listener), zero when it cannot be read.
func listenerQueue(fd int) uint32 {
	var info syscall.TCPInfo
	n := new(uint32(unsafe.Sizeof(info)))
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd), syscall.IPPROTO_TCP, syscall.TCP_INFO,
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(n)), 0)
	//: unreadable counts as empty: no stall is judged on it.
	if errno != 0 {
		//: empty.
		return 0
	}
	//: queued connections.
	return info.Unacked
}

// control applies the group's drain.
func (ul *uLoop) control() {
	lp := ul.lp
	//: the drain: no more accepts; settled connections close.
	if ul.drainReq.Load() && !lp.draining {
		lp.draining = true
		//: every ring.
		for _, rr := range ul.rings {
			rr.r.draining = true
			rr.cancelAccepts()
		}
	}
	//: while draining, a connection with no partial input closes once its
	//: output left.
	if !lp.draining {
		//: nothing.
		return
	}
	//: every ring, every open connection.
	for _, rr := range ul.rings {
		r := rr.r
		for s := r.lruHead; s != noLink; {
			c := &r.conns[s]
			nxt := c.lruNext
			//: mid-request ones finish first.
			if c.state == stOpen && c.spill.empty() {
				c.state, c.reason = stClosing, rDrain
				r.markDirty(s)
			}
			s = nxt
		}
	}
}

// closeAll fails every open connection as the loop stops, and lets the
// cancels and closes they issue complete, a bounded time.
func (ul *uLoop) closeAll() {
	//: every ring.
	for _, rr := range ul.rings {
		r := rr.r
		//: finish unlinks each.
		for r.lruHead != noLink {
			r.fail(r.lruHead, rDrain, 0)
		}
		rr.cancelAccepts()
		rr.u.submit()
	}
	deadline := time.Now().Add(time.Second)
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until every slot is released or the second is up.
	for ul.live() > 0 && time.Now().Before(deadline) {
		ul.rings[0].u.wait(&tick)
		//: each ring.
		for _, rr := range ul.rings {
			rr.u.submit()
			rr.reapAll()
		}
	}
	//: published once more.
	for _, rr := range ul.rings {
		rr.r.publish()
	}
}

// teardown releases the rings, the eventfds and the slab.
func (ul *uLoop) teardown() {
	lp := ul.lp
	//: no more posts.
	if lp.post != nil {
		lp.post.close()
	}
	//: each ring, its mappings with it; closing the ring closes every direct
	//: descriptor still in its table.
	for _, rr := range ul.rings {
		rr.u.close()
	}
	ul.wakeMu.Lock()
	closeFDs([]int{ul.wakefd, ul.fanfd})
	ul.evClosed = true
	ul.wakeMu.Unlock()
	//: the slab's mappings.
	for _, m := range lp.slab.mapped {
		swallowErr(syscall.Munmap(m))
	}
	lp.slab.mapped = nil
}
