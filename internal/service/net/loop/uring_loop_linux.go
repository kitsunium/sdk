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
	udShutdown
)

const (
	// acceptDepth is how many single-shot accepts a ring keeps in flight per
	// listener.
	acceptDepth int = 32
	// acceptReserve is the free direct-descriptor entries a ring keeps: under
	// it the ring stops accepting, and it resumes at twice as many.
	acceptReserve int = 16
	// pollIn is POLLIN (asm-generic/poll.h).
	pollIn int16 = 0x1
	// takeOn is uRing.take for a ring that takes its share of the
	// listeners' queues as the group drains.
	takeOn uint8 = 0
	// takeOff is uRing.take for a ring that takes nothing more.
	takeOff uint8 = 1
	// starvedCap bounds the connections waiting for a provided buffer.
	starvedCap int = 65536
	// reapRounds bounds the CQ batches one ring reaps per iteration, so one
	// ring cannot starve the others forever.
	reapRounds int = 4
	// staleFill is the byte a provided buffer is overwritten with under the
	// fill hook: a stale byte read back shows as it.
	staleFill byte = 0xAA
	// maxProcessFDs bounds slotOf on process descriptors, whatever
	// RLIMIT_NOFILE says.
	maxProcessFDs uint64 = 1 << 22
	// genMask keeps the 24 bits of a generation user_data carries.
	genMask uint32 = 1<<24 - 1
	// sweepCloses is the fewest closes since the last sweep started that
	// start the next one on a ring whose closes put their files late
	// (uRing.sweep); a quarter of the table when that is more.
	sweepCloses uint64 = 8192
	// sweepEvery is how many ticks after the last sweep the next one starts,
	// whatever the closes, once at least one happened.
	sweepEvery int64 = 1000
	// sweepScan is the fewest slots a sweep walks per tick.
	sweepScan int = 128
	// sweepTicks is the most ticks a pass takes, so a large table is walked
	// in a second.
	sweepTicks int = 100
	// noEpoch marks a slot with no recv armed.
	noEpoch uint64 = 1<<64 - 1
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

// pollFd is struct pollfd (include/uapi/asm-generic/poll.h).
type pollFd struct {
	fd      int32
	events  int16
	revents int16
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
	// queueDone is, per listener, that the ring took its share of the
	// listener's queue as the group drains and handed it back (plan P6);
	// queueCancel that a cancel of its accepts is in flight. take is
	// takeOff for a ring that takes nothing more: the watchdog drained it
	// before the group did, or an accept failed during the take.
	queueDone   []bool
	queueCancel []bool
	take        uint8
	// taken is, per listener, how many accepts the drain's take armed:
	// bounded by queueTakeMax, past which the arrivals outpace the drain.
	taken []int
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
	// resumeAt and backoff pace the accepts of a ring on process descriptors
	// after EMFILE or ENFILE, as epoll paces its listeners.
	resumeAt int64
	backoff  int64
	// sweepOn is whether the ring sweeps: its connections are direct and a
	// close puts its file only once older requests completed, as before 6.13
	// (uRing.sweep).
	sweepOn bool
	// closes counts the ring's direct-descriptor closes; armEpoch is, per
	// slot, closes when its recv was last armed, noEpoch when none is.
	closes   uint64
	armEpoch []uint64
	// sweepAt is the next slot the running sweep looks at, -1 when none runs;
	// sweepEpoch the closes it started at; sweptCloses the closes the last
	// one started at, lastSweep the tick it ended, sweepTick the tick it last
	// walked; sweeps counts the passes.
	sweepAt     int
	sweepEpoch  uint64
	sweptCloses uint64
	lastSweep   int64
	sweepTick   int64
	sweeps      int
}

// uLoop is one loop on io_uring: one thread owns every ring.
type uLoop struct {
	lp    *loop
	rings []*uRing
	lfds  []int
	// set is the group's hold on the listeners through the drain.
	set   *listenerSet
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
	// direct is whether connections live in the rings' direct-descriptor
	// tables (the probe's release check passed) or in the process table.
	direct bool
	// sw is what the group may use; rep what the probe found (ADR 0189).
	sw  capSwitches
	rep capReport
	// primary and secondary are the setup flags of ring 0, the one the
	// thread waits on, and of the others: DEFER_TASKRUN only on the primary
	// (P3a), the others posting their completions themselves (COOP).
	primary, secondary uint32
	// uses is what the probe settled the rings use; registered is how many
	// rings are registered with the thread.
	uses       ringUses
	registered int
}

// ringUses is what a loop's rings use beyond their setup flags.
type ringUses struct {
	// multishot is whether recvs are multishot; ringFDs whether rings are
	// registered with the thread.
	multishot, ringFDs bool
}

// uringRunner runs the group's io_uring loops.
type uringRunner struct {
	loops []*uLoop
	wg    sync.WaitGroup
	fin   chan struct{}
	lfds  []int
	// set shares the listeners through the drain and closes, once every loop
	// returned, what the drain did not.
	set *listenerSet
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
// that waits on fin, closes the listeners the drain did not and returns.
func startIOURing(g *Group, cfg *Config, hooks loopHooks) error {
	sw := newSwitches(cfg)
	//: the backend receives into provided buffers only: switched off, it is
	//: never set up.
	if sw.disabled(corenet.CapabilityURingPbuf) {
		//: refused by the switch, before any setup.
		return probeError("uring-pbuf switched off", syscall.EOPNOTSUPP)
	}
	fin := make(chan struct{})
	runner := &uringRunner{fin: fin}
	//: TCP_NODELAY on the listener, inherited by every accepted socket; a
	//: socket that refuses it still serves.
	for _, l := range cfg.Listeners {
		noDelay(l.FD, sw)
		runner.lfds = append(runner.lfds, l.FD)
	}
	//: every ring of every loop takes each listener's queue once, as the
	//: group drains.
	runner.set = newListenerSet(runner.lfds, g.sz.loops*g.sz.rings)
	ready := make(chan error, g.sz.loops)
	//: one thread per loop, each building its rings after it locked itself.
	for i := range g.sz.loops {
		lp := newLoop(g, *cfg, i, mmapAnon)
		ul := &uLoop{lp: lp, lfds: runner.lfds, set: runner.set, hooks: hooks, wakefd: -1, fanfd: -1, sw: sw, rep: newReport(sw, "iouring")}
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
		runner.set.closeRest()
	}()
	g.backend = corenet.LoopBackendIOURing
	g.stage = runner.loops[0].stage
	g.caps = runner.loops[0].rep
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
	err := ul.probe()
	//: stages 1 to 3 refused.
	if err != nil {
		//: named by the stage.
		return err
	}
	//: stage 4 and 5 run on every ring as it is built, the self-test once.
	direct, deferred, err := ul.selfTest(ul.primary)
	//: the self-test refused.
	if err != nil {
		//: named by the test.
		return err
	}
	ul.direct = direct
	ul.stage = "self_test+fds"
	//: the release check passed: connections are direct descriptors.
	if direct {
		ul.stage = "self_test+direct"
	}
	ul.reportSelfTest(direct)
	//: built, or refused by the ring that could not be.
	return ul.build(rings, direct && deferred)
}

// build creates the Post queue, the two eventfds and every ring, and arms
// the primary's polls. sweep is whether the rings sweep deferred puts.
func (ul *uLoop) build(rings int, sweep bool) error {
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
		rr, err := ul.newRing(i, sweep)
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
// its accepts. sweep is whether it sweeps deferred puts.
func (ul *uLoop) newRing(i int, sweep bool) (*uRing, error) {
	flags := ul.secondary
	//: the thread waits on ring 0 alone.
	if i == 0 {
		flags = ul.primary
	}
	//: a test's secondaries the kernel signals for.
	if i > 0 && ul.hooks.secondaryPlain {
		flags &^= setupCoopTaskrun
	}
	u, step, errno := openURing(uringEntries, flags)
	//: refused.
	if errno != 0 {
		//: named.
		return nil, probeError(step, errno)
	}
	ul.registerRing(u)
	lp := ul.lp
	rr := &uRing{
		ul: ul, u: u, armed: make([]int, len(ul.lfds)),
		queueDone: make([]bool, len(ul.lfds)), queueCancel: make([]bool, len(ul.lfds)), taken: make([]int, len(ul.lfds)), backoff: backoffMin, sweepOn: sweep, sweepAt: -1,
	}
	rr.r = newRing(lp, i, rr)
	bufs, size := ul.bufShape()
	//: stage 4: provided buffers and the sparse table.
	if errno := u.setupBuffers(bufs, size); errno != 0 || ul.hooks.failStage == "buf_ring" {
		u.close()
		//: named.
		return nil, probeError("buf_ring", orErrno(errno, syscall.EOPNOTSUPP))
	}
	table := uint32(lp.sz.tableSize)
	//: the table the accepts allocate into, when connections are direct.
	if err := ul.registerTable(u, table); err != nil {
		u.close()
		//: named.
		return nil, err
	}
	rr.inRing = int(bufs)
	rr.slotOf = slices.Repeat([]int32{-1}, ul.slotSpace(table))
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

// registerTable registers ring u's sparse direct-descriptor table of n
// entries, when connections are direct descriptors.
func (ul *uLoop) registerTable(u *uring, n uint32) error {
	//: process descriptors need no table.
	if !ul.direct && ul.hooks.failStage != "sparse_files" {
		//: none.
		return nil
	}
	errno := u.registerSparse(n)
	//: refused, or the test fails the stage.
	if errno != 0 || ul.hooks.failStage == "sparse_files" {
		//: named.
		return probeError("sparse_files", orErrno(errno, syscall.EOPNOTSUPP))
	}
	//: registered.
	return nil
}

// slotSpace is how many numbers slotOf maps: the table's entries for direct
// descriptors, every process descriptor below RLIMIT_NOFILE otherwise.
func (ul *uLoop) slotSpace(table uint32) int {
	//: the table's entries.
	if ul.direct {
		//: as registered.
		return int(table)
	}
	//: the process table, bounded.
	return int(min(ul.lp.sz.rlimit, maxProcessFDs))
}

// onFD adapts an SQE filled for a direct descriptor to the loop's mode.
func (ul *uLoop) onFD(s *sqe) {
	//: process descriptors: no fixed-file flag.
	if !ul.direct {
		onProcessFD(s)
	}
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
	//: a paused or draining ring accepts nothing, nor a primary a test
	//: keeps idle.
	if rr.acceptsNothing() {
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
			rr.ul.onFD(s)
			rr.armed[i]++
			room--
		}
	}
}

// acceptsNothing reports a ring that arms no accept: paused, draining, or
// the primary a test keeps idle.
func (rr *uRing) acceptsNothing() bool {
	//: any of them.
	return rr.paused || rr.r.draining || rr.ul.lp.draining || (rr.ul.hooks.primaryIdle && rr.r.index == 0)
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
		//: no room: next time.
		if !rr.cancelAccept(i) {
			//: next time.
			return
		}
	}
}

// cancelAccept cancels every accept in flight on listener i, and reports
// whether the SQ had room for the cancel.
func (rr *uRing) cancelAccept(i int) bool {
	s := rr.u.get()
	//: no room.
	if s == nil {
		rr.r.st.retries++
		//: not sent.
		return false
	}
	prepCancelUserData(s, udPack(udAccept, 0, uint32(i)), udPack(udAcceptCancel, 0, uint32(i)))
	//: sent.
	return true
}

// takeQueues takes the ring's share of every listener's queue as the group
// drains (plan P6). Once the cancels of its accepts came back — an accept
// that completed as its cancel crossed it is served like any other —, it
// arms as many single-shot accepts as the queue holds (TCP_INFO), within the
// room above the reserve, and hands the listener back to the group's set
// once the queue is empty and none is in flight; the last ring closes it. An
// accept left waiting on an empty queue, because another ring took the
// connection, is cancelled again.
func (rr *uRing) takeQueues() {
	//: each listener not handed back yet.
	for i, fd := range rr.ul.lfds {
		//: not handed back yet.
		if !rr.queueDone[i] {
			rr.takeQueue(i, fd)
		}
	}
}

// takeQueue is takeQueues' step on listener i, fd.
func (rr *uRing) takeQueue(i, fd int) {
	q := 0
	//: a ring the watchdog drained, whose accept failed, or past its budget
	//: takes nothing.
	if rr.take == takeOn && rr.taken[i] < queueTakeMax {
		q = queued(fd)
	}
	//: accepts in flight: the drain's cancels, or this take's own.
	if rr.armed[i] > 0 {
		//: one waits on an empty queue: cancelled, once.
		if q == 0 && !rr.queueCancel[i] {
			rr.queueCancel[i] = rr.cancelAccept(i)
		}
		//: their completions come first.
		return
	}
	rr.queueCancel[i] = false
	//: empty: handed back.
	if q == 0 {
		rr.queueDone[i] = true
		rr.ul.set.handBack(i)
		//: taken.
		return
	}
	n := min(q, acceptDepth, rr.acceptRoom(), queueTakeMax-rr.taken[i])
	//: queued, but no entry free, or process descriptors out until the
	//: backoff ends: the drain's closes make room, and the take waits.
	if n == 0 || (rr.paused && rr.ul.lp.tick < rr.resumeAt) {
		//: next batch.
		return
	}
	rr.taken[i] += rr.armTake(i, fd, n)
}

// armTake arms n single-shot accepts on listener i, fd, for the drain's take,
// and says how many the SQ had room for.
func (rr *uRing) armTake(i, fd, n int) int {
	//: one SQE each.
	for k := range n {
		s := rr.u.get()
		//: no room in the SQ.
		if s == nil {
			rr.r.st.retries++
			//: next batch.
			return k
		}
		prepAccept(s, fd, udPack(udAccept, 0, uint32(i)), false)
		rr.ul.onFD(s)
		rr.armed[i]++
	}
	//: all of them.
	return n
}

// queuesTaken reports whether every ring of the loop handed every listener
// back.
func (ul *uLoop) queuesTaken() bool {
	//: every ring, every listener.
	for _, rr := range ul.rings {
		//: one left.
		if slices.Contains(rr.queueDone, false) {
			//: not yet.
			return false
		}
	}
	//: all handed back.
	return true
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
	//: twice the reserve, and past any EMFILE backoff: resume.
	if rr.paused && free >= max(2*reserve, 1) && rr.ul.lp.tick >= rr.resumeAt {
		rr.paused = false
	}
	//: under hard pressure, paced eviction makes room.
	if rr.ul.lp.hard && !rr.ul.direct {
		rr.r.evict()
	}
	rr.armAccepts()
}

// serve is the loop: one wait on the primary, every ring reaped, one batch.
func (ul *uLoop) serve() {
	lp := ul.lp
	tick := kernelTimespec{nsec: int64(tickEvery)}
	//: until stopped, or drained to the last connection, the listeners'
	//: queues taken first.
	for {
		ul.batch(&tick)
		//: finished.
		if ul.stopReq.Load() || (lp.draining && ul.live() == 0 && ul.queuesTaken()) {
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
		rr.sweep()
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
	case udRecv, udSend, udCancel, udClose, udShutdown:
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
	//: a cancel or a shutdown finished, whatever its result: a shutdown of a
	//: socket the peer already reset fails with ENOTCONN, and the close
	//: follows all the same.
	case udCancel, udShutdown:
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
	//: an error: the table is full, the listener failed, or the drain
	//: cancelled it.
	if c.res < 0 {
		rr.acceptFailed(c.res)
		//: during the drain, the take judges it too.
		if lp.draining {
			rr.takeFailed(c.res)
		}
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
	rr.backoff = backoffMin
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
		//: out of process descriptors: closes make room, so the ring waits a
		//: backoff before it tries again, and evicts meanwhile.
		if !rr.ul.direct {
			rr.resumeAt = rr.ul.lp.tick + rr.backoff
			rr.backoff = min(rr.backoff*2, backoffMax)
			rr.ul.lp.hard = true
		}
	}
}

// takeFailed ends the ring's take on an accept failure that says the
// listener is broken; its own cancel, a connection the peer abandoned, an
// empty queue and a lack of descriptors only wait.
func (rr *uRing) takeFailed(res int32) {
	//: broken.
	if !takeWaits(res) {
		rr.take = takeOff
	}
}

// takeWaits reports whether an accept's failure leaves the drain's take
// going: its own cancel, a connection the peer abandoned, a signal, an empty
// queue (EAGAIN, should a kernel honour the listener's O_NONBLOCK), or no
// descriptor until a close frees one.
func takeWaits(res int32) bool {
	//: by errno.
	switch syscall.Errno(-res) {
	//: each of them waits for the next batch.
	case syscall.ECANCELED, syscall.ECONNABORTED, syscall.EINTR, syscall.EAGAIN, syscall.ENFILE, syscall.EMFILE,
		syscall.EPROTO, syscall.ENOPROTOOPT, syscall.EHOSTDOWN, syscall.ENONET, syscall.EHOSTUNREACH,
		syscall.EOPNOTSUPP, syscall.ENETUNREACH, syscall.ENETDOWN:
		//: waits.
		return true
	//: anything else.
	default:
		//: the listener failed.
		return false
	}
}

// refuses reports whether a connection accepted on index is closed at once:
// the watchdog drained the ring while the group serves, MaxConns is reached,
// or the index is past the table. The group's drain adds no refusal of its
// own: an accept that completed as its cancel crossed it, or one the drain's
// take armed, is served unless MaxConns is reached, as at any time.
func (rr *uRing) refuses(index uint32) bool {
	lp := rr.ul.lp
	//: any of the three.
	return (rr.r.draining && !lp.draining) || (lp.sz.maxConns > 0 && lp.grp.live.Load() >= int64(lp.sz.maxConns)) || int(index) >= len(rr.slotOf)
}

// orphanClose closes a direct descriptor no slot owns.
func (rr *uRing) orphanClose(index uint32) {
	//: a process descriptor closes at once.
	if !rr.ul.direct {
		closeFDs([]int{int(index)})
		//: closed.
		return
	}
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
	//: uring-multishot-recv switched off: one completion, re-armed after it.
	if !rr.ul.uses.multishot {
		singleShot(s)
	}
	rr.ul.onFD(s)
	c.ops++
	rr.setEpoch(slot, rr.closes)
}

// setEpoch records the closes count slot's recv was armed at, or noEpoch.
func (rr *uRing) setEpoch(slot int32, epoch uint64) {
	//: only a ring that sweeps keeps them.
	if !rr.sweepOn {
		//: nothing.
		return
	}
	//: the table grows with the peak; so do the epochs.
	for int(slot) >= len(rr.armEpoch) {
		rr.armEpoch = append(rr.armEpoch, noEpoch)
	}
	rr.armEpoch[slot] = epoch
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
	//: the multishot ended: one operation less, and no recv armed until it
	//: is armed again.
	if !more {
		rr.r.conns[slot].ops--
		rr.setEpoch(slot, noEpoch)
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
	//: a process descriptor closes now, nothing of it left in flight.
	if !rr.ul.direct {
		closeFDs([]int{int(c.fd)})
		rr.onClose(slot, c.gen&genMask)
		//: released.
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
	rr.closes++
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
	rr.ul.onFD(s)
	c.ops++
	c.flags |= fSending
}

// shutdown implements driver. Every close goes the same way, whoever asked
// for it, the peer included: shutdown(SHUT_RDWR) on the descriptor, a cancel
// of every operation still on it hard-linked behind the shutdown, and the
// close once the count of operations in flight reaches zero (maybeClose).
//
// The shutdown is what tells the peer. Before 6.13 a direct descriptor's
// close only hangs its file on the table's current resource node, and nodes
// are put strictly in order: one idle connection whose recv was armed early
// holds the file of every later close, and with it the FIN (io_uring/rsrc.c,
// io_rsrc_node_ref_zero, until 6.13 gave each file a node of its own). The
// shutdown sends the FIN at once and ends this connection's own recv on zero,
// whatever the put does later; a peer that closed first is shut down too, or
// the socket would sit in CLOSE_WAIT and its peer in FIN_WAIT2 until the put.
// The cancel is linked behind the shutdown so it can never cancel it, which
// the kernel runs in its worker before 6.13; it only ends the sends still in
// flight. The shutdown is counted and never skips its completion: the close
// is due when the count reaches zero, not when the recv ends.
func (rr *uRing) shutdown(r *ring, slot int32) {
	c := &r.conns[slot]
	//: two SQEs in a row, or no chain: the close waits for the operations to
	//: end by themselves.
	if !rr.u.reserve(2) {
		r.st.retries++
		rr.maybeClose(slot)
		//: later.
		return
	}
	s := rr.u.get()
	prepShutdownFixed(s, uint32(c.fd), syscall.SHUT_RDWR, udPack(udShutdown, c.gen, uint32(slot)))
	rr.ul.onFD(s)
	s.flags |= sqeIOHardlink
	s = rr.u.get()
	prepCancelFixed(s, uint32(c.fd), udPack(udCancel, c.gen, uint32(slot)))
	rr.ul.onFD(s)
	c.ops += 2
}

// sweep bounds the files a direct descriptor's close leaves pinned where the
// put is deferred (sweepOn, before 6.13): a closed socket, its inode and its
// file wait for every recv armed before the close, and an idle connection's
// multishot recv never completes by itself, so with churn and one old
// keep-alive the pinned sockets would grow without bound. Each costs about
// 3.6 KiB of kernel slab and no descriptor (the TCP object, its socket inode,
// its file and its dentry, read from /proc/slabinfo on 6.8 and 6.12; the
// package's CLAUDE.md has the sizes).
//
// A pass starts once the closes since the last one started reach a quarter
// of the table, 8192 at least, or sweepEvery ticks after the last with at
// least one close. It walks the table in at most sweepTicks ticks, sweepScan
// slots a tick at least, and cancels every recv armed before the pass began
// and before a close; recvCancelled arms each again under the current node,
// so the older nodes drain and their files are put. Pinned sockets stay
// under that threshold plus one second of closes, and a pass costs at most
// one cancel and one re-arm per open connection, so at most four of each per
// close. Bytes that arrive between the cancel and the re-arm wait in the
// socket and are read by the new recv: nothing is lost.
func (rr *uRing) sweep() {
	lp := rr.ul.lp
	//: only where a close's put waits on older recvs, once a tick.
	if !rr.sweepOn || lp.tick == rr.sweepTick {
		//: nothing.
		return
	}
	rr.sweepTick = lp.tick
	//: idle: a pass starts when due.
	if rr.sweepAt < 0 && !rr.sweepDue(lp.tick) {
		//: not due.
		return
	}
	//: started at the closes it saw.
	if rr.sweepAt < 0 {
		rr.sweepAt, rr.sweepEpoch = 0, rr.closes
	}
	conns := rr.r.conns
	end := min(rr.sweepAt+max(sweepScan, (len(conns)+sweepTicks-1)/sweepTicks), len(conns))
	//: this tick's share of the table.
	for ; rr.sweepAt < end; rr.sweepAt++ {
		//: no room: the rest at the next tick.
		if !rr.cancelOld(int32(rr.sweepAt)) {
			//: later.
			return
		}
	}
	//: the table walked: the pass is over.
	if rr.sweepAt >= len(conns) {
		rr.sweepAt = -1
		rr.sweptCloses = rr.sweepEpoch
		rr.lastSweep = lp.tick
		rr.sweeps++
	}
}

// sweepDue reports whether a sweep pass is due at tick: enough closes since
// the last one started, or its period elapsed with at least one.
func (rr *uRing) sweepDue(tick int64) bool {
	since := rr.closes - rr.sweptCloses
	every := cmp.Or(rr.ul.hooks.sweepEvery, sweepEvery)
	enough := max(sweepCloses, uint64(len(rr.r.conns)/4))
	//: closes enough, or time with some.
	return since >= enough || (since > 0 && tick-rr.lastSweep >= every)
}

// cancelOld cancels slot's recv when it was armed before the pass began and
// before a close. It reports false when the SQ had no room.
func (rr *uRing) cancelOld(slot int32) bool {
	c := &rr.r.conns[slot]
	//: an open connection whose recv holds an older node.
	if c.state != stOpen || int(slot) >= len(rr.armEpoch) || rr.armEpoch[slot] >= rr.sweepEpoch {
		//: nothing to cancel.
		return true
	}
	s := rr.u.get()
	//: no room.
	if s == nil {
		rr.r.st.retries++
		//: later.
		return false
	}
	prepCancelUserData(s, udPack(udRecv, c.gen, uint32(slot)), udPack(udCancel, c.gen, uint32(slot)))
	c.ops++
	rr.armEpoch[slot] = noEpoch
	//: cancelled; recvCancelled arms it again.
	return true
}

// unread implements driver. A direct descriptor has no process descriptor
// to ask SIOCINQ of, so it is never evicted blind: it reports unread. A
// process descriptor is asked, as epoll asks it.
func (rr *uRing) unread(r *ring, slot int32) bool {
	//: a direct descriptor: never evicted blind.
	if rr.ul.direct {
		//: conservative.
		return true
	}
	var n int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(r.conns[slot].fd), siocinq, uintptr(unsafe.Pointer(&n)))
	//: unknown counts as unread.
	return errno != 0 || n > 0
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

// queued is how many connections wait in the listening socket fd's queue:
// TCP_INFO's count on a TCP listener; on one TCP_INFO does not answer — a
// Unix socket — acceptDepth when a zero-timeout poll says it is readable, and
// zero otherwise. An accept armed past what waits is cancelled once the queue
// reads empty.
func queued(fd int) int {
	var info syscall.TCPInfo
	n := new(uint32(unsafe.Sizeof(info)))
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd), syscall.IPPROTO_TCP, syscall.TCP_INFO,
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(n)), 0)
	//: a TCP listener counts its queue.
	if errno == 0 {
		//: queued connections.
		return int(info.Unacked)
	}
	//: anything else says only whether one waits.
	if readable(fd) {
		//: some.
		return acceptDepth
	}
	//: none.
	return 0
}

// readable polls fd for input with a zero timeout: a listening socket is
// readable while its accept queue holds a connection.
func readable(fd int) bool {
	pfd := pollFd{fd: int32(fd), events: pollIn}
	r, _, errno := syscall.RawSyscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(new(syscall.Timespec{}))), 0, 0, 0)
	//: an answer, with input.
	return errno == 0 && r == 1 && pfd.revents&pollIn != 0
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
	//: the drain: the accepts in flight are cancelled, the listeners'
	//: queues taken, then handed back; settled connections close.
	if ul.drainReq.Load() && !lp.draining {
		ul.startDrain()
	}
	//: while draining, a connection with no partial input closes once its
	//: output left.
	if !lp.draining {
		//: nothing.
		return
	}
	//: every ring, every open connection.
	for _, rr := range ul.rings {
		rr.takeQueues()
		lp.closeSettled(rr.r)
	}
}

// startDrain begins the group's drain on every ring: its accepts in flight
// cancelled, a ring the watchdog drained already set to take nothing.
func (ul *uLoop) startDrain() {
	ul.lp.draining = true
	//: every ring.
	for _, rr := range ul.rings {
		//: a ring the watchdog drained takes nothing.
		if rr.r.draining {
			rr.take = takeOff
		}
		rr.r.draining = true
		//: each listener with accepts in flight.
		for i, n := range rr.armed {
			//: cancelled, when the SQ has room; takeQueues retries.
			if n > 0 {
				rr.queueCancel[i] = rr.cancelAccept(i)
			}
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
