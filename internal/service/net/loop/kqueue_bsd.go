//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package loop

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// kqReadChunk is the most bytes one read asks for.
	kqReadChunk int = 16 << 10
	// kqMinRead is the least room in the read arena worth a read: below it the
	// connection is read on the next iteration, its readiness still level.
	kqMinRead int = 2 << 10
	// kqArenaSize is the loop's read arena: where a batch's received bytes
	// live until OnBatchEnd returns.
	kqArenaSize int = 2 << 20
	// kqBackoffMin is the first wait before a listener disarmed by EMFILE or
	// ENFILE is armed again, in ticks.
	kqBackoffMin int64 = 1
	// kqBackoffMax caps that wait, doubled at each pause, at one second.
	kqBackoffMax int64 = int64(time.Second / tickEvery)
	// wakeDrainBytes is how many wake bytes one read empties.
	wakeDrainBytes int = 64
	// noFDSlot marks a descriptor that names no connection in fdSlot.
	noFDSlot int32 = 0
)

// The stages of kqueue's probe, in order (ADR 0222): ProbeStage names the
// last one passed, a refusal the one that failed.
const (
	// stageCreate is kqueue(2) itself.
	stageCreate string = "kqueue"
	// stageWake is the wake pipe, registered and reported readable.
	stageWake string = "kqueue-wake"
	// stageSocket is a socket pair's read readiness, its byte read back, and
	// its peer's close reported as EV_EOF.
	stageSocket string = "kqueue-socket"
)

// kqueueRunner runs one kqueue loop per thread.
type kqueueRunner struct {
	loops []*kqueueLoop
	wg    sync.WaitGroup
	fin   chan struct{}
	// set shares the group's listeners through the drain, and closes what the
	// drain did not once every loop returned.
	set *listenerSet
}

// kqueueLoop is one loop on kqueue: one ring, one kqueue, one thread. Its
// filters are level-triggered, as epoll's are: a read takes at most one chunk
// and the arena may be spent, so a readiness left pending must fire again.
type kqueueLoop struct {
	lp *loop
	r  *ring
	kq int
	// wakeR and wakeW are the wake pipe: a Post or a request writes a byte,
	// the loop reads them all.
	wakeR, wakeW int
	// lfds are the listeners, shared with the group's other loops; owners
	// says which of them this loop accepts on; set is the group's hold on them
	// through the drain.
	lfds   []int
	owners []int
	set    *listenerSet
	// queueDone and taken are the drain take's progress, per listener.
	queueDone []bool
	taken     []int
	// armed reports whether the listeners are registered; rearmAt is the tick
	// an EMFILE backoff ends at, backoff its current length.
	armed   bool
	rearmAt int64
	backoff int64
	// events receives one kevent; ready the listeners it reported, served
	// after every connection's events so no descriptor a batch reported can
	// name a connection accepted in the same batch.
	events []syscall.Kevent_t
	ready  []int
	// fdSlot maps a connection's descriptor to its slot plus one, noFDSlot
	// for none: kqueue names a descriptor, never a slot.
	fdSlot []int32
	// arena is the read arena; off how much of it this batch used.
	arena []byte
	off   int
	// tick is the wait's bound while anything is timed: one tick.
	tick syscall.Timespec
	// inWritable is set while writable flushes, whose write filter is already
	// enabled.
	inWritable bool
	// drainReq and stopReq are the group's requests, set from any goroutine.
	drainReq atomic.Bool
	stopReq  atomic.Bool
	// wakeMu orders a wake against the pipe's close: a Post that lands as the
	// loop stops must never write a descriptor number reused since. wakeW is
	// -1 under it once the pipe is closed, or before it is open.
	wakeMu   sync.Mutex
	wakeByte [1]byte
}

// startKqueue probes kqueue, then starts g's loops on it.
//
// Goroutine lifecycle: it launches one goroutine per loop, locked to its
// thread, which returns once the loop stopped or drained, and one reaper that
// waits for them all, closes the listeners the drain did not and closes fin,
// then returns.
func startKqueue(g *Group, cfg *Config) error {
	stage, err := probeKqueue(cfg.hooks.failStage)
	g.stage = stage
	//: refused by the stage that failed.
	if err != nil {
		//: server falls back for auto, refuses an explicit kqueue.
		return err
	}
	fin := make(chan struct{})
	runner := &kqueueRunner{fin: fin}
	sw := newSwitches(cfg)
	g.caps = newReport(sw, "kqueue")
	lfds, rows := listenerRows(cfg, sw)
	g.caps.apply(rows)
	owners := ownersOf(cfg.Listeners)
	runner.set = newListenerSet(lfds, takersOfShards(owners, g.sz.loops, 1))
	ready := make(chan error, g.sz.loops)
	//: one thread per loop, each building its own kqueue after it locked
	//: itself to its thread.
	for i := range g.sz.loops {
		lp := newLoop(g, *cfg, i, mmapAnon, syscall.Munmap)
		kl := &kqueueLoop{
			lp: lp, lfds: lfds, owners: owners, set: runner.set,
			queueDone: doneOutside(owners, i), taken: make([]int, len(lfds)),
			kq: -1, wakeR: -1, wakeW: -1, events: make([]syscall.Kevent_t, g.sz.cqBatch), backoff: kqBackoffMin,
			tick: syscall.NsecToTimespec(int64(tickEvery)),
		}
		runner.loops = append(runner.loops, kl)
		g.loops = append(g.loops, lp)
		runner.wg.Add(1)
		go kl.run(&runner.wg, ready)
	}
	var first error
	//: every loop says whether it came up.
	for range g.sz.loops {
		//: the first failure is the one reported.
		if err := <-ready; err != nil && first == nil {
			first = err
		}
	}
	g.ctl = runner
	//: the reaper owns fin: it closes it once, after the last loop returned.
	go func() {
		runner.wg.Wait()
		runner.set.closeRest()
		close(fin)
	}()
	//: a loop that could not start stops the others.
	if first != nil {
		runner.stop()
		runner.wait()
		//: refused.
		return first
	}
	reportAffinity(&g.caps, g.loops)
	g.backend = backendKqueue
	//: running.
	return nil
}

// probeKqueue runs kqueue's staged probe on throwaway descriptors and returns
// the last stage passed, or the one that failed with LoopBackendUnavailable
// naming it. failStage, the conformance suite's hook, fails the named stage.
func probeKqueue(failStage string) (string, error) {
	kq, err := syscall.Kqueue()
	//: no kqueue at all.
	if err = hooked(stageCreate, failStage, err); err != nil {
		//: refused at the first stage.
		return stageCreate, unavailable(stageCreate, err)
	}
	defer closeFDs([]int{kq})
	//: the wake pipe.
	if err := hooked(stageWake, failStage, probeWake(kq)); err != nil {
		//: refused.
		return stageWake, unavailable(stageWake, err)
	}
	//: a stream socket's readiness and its end.
	if err := hooked(stageSocket, failStage, probeSocket(kq)); err != nil {
		//: refused.
		return stageSocket, unavailable(stageSocket, err)
	}
	//: every stage passed.
	return stageSocket, nil
}

// hooked is err, or EINVAL when the test hook names stage.
func hooked(stage, failStage string, err error) error {
	//: the suite's injected failure.
	if err == nil && stage == failStage {
		//: as if the kernel refused.
		return syscall.EINVAL
	}
	//: the kernel's answer.
	return err
}

// probeWake registers a pipe's read end on kq, writes a byte and requires the
// kqueue to report it.
func probeWake(kq int) error {
	r, w, err := pipeCloexec()
	//: no pipe.
	if err != nil {
		//: the kernel's answer.
		return err
	}
	defer closeFDs([]int{r, w})
	//: registered, then written.
	if err := kevChange(kq, r, syscall.EVFILT_READ, syscall.EV_ADD); err != nil {
		//: the kernel's answer.
		return err
	}
	//: the byte the wait must see.
	if _, err := syscall.Write(w, []byte{1}); err != nil {
		//: the kernel's answer.
		return err
	}
	//: reported readable.
	return expectEvent(kq, r, 0)
}

// probeSocket registers one end of a stream socket pair on kq, writes a byte
// from the other, requires it reported and read back, then closes the peer and
// requires EV_EOF: the readiness and the end the loop serves on.
func probeSocket(kq int) error {
	syscall.ForkLock.RLock()
	sp, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	syscall.ForkLock.RUnlock()
	//: no socket pair.
	if err != nil {
		//: the kernel's answer.
		return err
	}
	defer closeFDs(sp[:1])
	peerOpen := true
	defer func() {
		//: the peer, unless the probe closed it already.
		if peerOpen {
			closeFDs(sp[1:])
		}
	}()
	//: registered, then written to.
	if err := firstErr(syscall.SetNonblock(sp[0], true), kevChange(kq, sp[0], syscall.EVFILT_READ, syscall.EV_ADD)); err != nil {
		//: the kernel's answer.
		return err
	}
	//: the byte the wait must see.
	if _, err := syscall.Write(sp[1], []byte{1}); err != nil {
		//: the kernel's answer.
		return err
	}
	//: reported readable.
	if err := expectEvent(kq, sp[0], 0); err != nil {
		//: not reported.
		return err
	}
	var b [1]byte
	//: and read back.
	if n, err := syscall.Read(sp[0], b[:]); n != 1 || err != nil {
		//: the kernel's answer.
		return errOr(err, syscall.EIO)
	}
	peerOpen = false
	swallowErr(syscall.Close(sp[1]))
	//: the end, as the loop reads a peer that left.
	return expectEvent(kq, sp[0], syscall.EV_EOF)
}

// kevChange applies one change to kq at once.
func kevChange(kq, fd int, filter int16, flags uint16) error {
	var ch [1]syscall.Kevent_t
	syscall.SetKevent(&ch[0], fd, int(filter), int(flags))
	_, err := syscall.Kevent(kq, ch[:], nil, nil)
	//: the kernel's answer.
	return err
}

// expectEvent waits a second at most for kq to report fd readable, with the
// flags of want set.
func expectEvent(kq, fd int, want uint16) error {
	var ev [4]syscall.Kevent_t
	n, err := syscall.Kevent(kq, nil, ev[:], new(syscall.NsecToTimespec(int64(time.Second))))
	//: the wait failed.
	if err != nil {
		//: the kernel's answer.
		return err
	}
	//: each event reported.
	for i := range n {
		//: fd, readable, with the flags asked for.
		if int(ev[i].Ident) == fd && int(ev[i].Filter) == syscall.EVFILT_READ && uint16(ev[i].Flags)&want == want {
			//: as expected.
			return nil
		}
	}
	//: never reported.
	return syscall.ETIMEDOUT
}

// drain implements runner.
func (kr *kqueueRunner) drain() {
	//: every loop is told, then woken.
	for _, kl := range kr.loops {
		kl.drainReq.Store(true)
		kl.wake()
	}
}

// stop implements runner.
func (kr *kqueueRunner) stop() {
	//: every loop is told, then woken.
	for _, kl := range kr.loops {
		kl.stopReq.Store(true)
		kl.wake()
	}
}

// wait implements runner.
func (kr *kqueueRunner) wait() {
	<-kr.fin
}

// done implements runner.
func (kr *kqueueRunner) done() <-chan struct{} {
	//: closed once every thread returned.
	return kr.fin
}

// wake writes a byte to the loop's pipe. It is safe from any goroutine.
func (kl *kqueueLoop) wake() {
	kl.wakeMu.Lock()
	defer kl.wakeMu.Unlock()
	//: a stopped loop has nothing to wake, and its number may be another's.
	if kl.wakeW < 0 {
		//: nothing to do.
		return
	}
	_, err := syscall.Write(kl.wakeW, kl.wakeByte[:])
	//: a full pipe already wakes the loop; nothing else can fail here.
	swallowErr(err)
}

// setup builds the loop's kqueue, wake pipe, arena and ring on its own thread.
func (kl *kqueueLoop) setup() error {
	kq, err := syscall.Kqueue()
	//: no kqueue.
	if err != nil {
		//: refused.
		return unavailable(stageCreate, err)
	}
	syscall.CloseOnExec(kq)
	kl.kq = kq
	r, w, err := pipeCloexec()
	//: no wake pipe.
	if err != nil {
		//: refused.
		return unavailable(stageWake, err)
	}
	kl.wakeMu.Lock()
	kl.wakeR, kl.wakeW = r, w
	kl.wakeMu.Unlock()
	//: the wake pipe is registered for the loop's life.
	if err := kevChange(kl.kq, kl.wakeR, syscall.EVFILT_READ, syscall.EV_ADD); err != nil {
		//: refused.
		return unavailable(stageWake, err)
	}
	arena, err := mmapAnon(kqArenaSize)
	//: no arena.
	if err != nil {
		//: refused.
		return unavailable("mmap arena", err)
	}
	kl.arena = arena
	lp := kl.lp
	lp.post = newPostQueue(kl.wake)
	kl.r = newRing(lp, 0, kl)
	lp.rings = []*ring{kl.r}
	kl.armListeners()
	//: ready to serve.
	return nil
}

// armListeners registers every listener this loop accepts on.
func (kl *kqueueLoop) armListeners() {
	//: armed once, until a backoff or the drain removes them.
	if kl.armed || kl.lp.draining {
		//: nothing to do.
		return
	}
	//: each listener of this loop; one the kernel refuses stays out of this
	//: loop's set, and the group's other loops still accept on it.
	for i, fd := range kl.lfds {
		//: another loop's shard.
		if !ownedBy(kl.owners, i, kl.lp.index) {
			continue
		}
		swallowErr(kevChange(kl.kq, fd, syscall.EVFILT_READ, syscall.EV_ADD))
	}
	kl.armed = true
}

// disarmListeners takes every listener out of the kqueue.
func (kl *kqueueLoop) disarmListeners() {
	//: nothing armed.
	if !kl.armed {
		//: done.
		return
	}
	//: each listener; one that was never registered has nothing to remove.
	for i, fd := range kl.lfds {
		//: another loop's shard was never registered here.
		if !ownedBy(kl.owners, i, kl.lp.index) {
			continue
		}
		swallowErr(kevChange(kl.kq, fd, syscall.EVFILT_READ, syscall.EV_DELETE))
	}
	kl.armed = false
}

// run is the loop's thread.
func (kl *kqueueLoop) run(wg *sync.WaitGroup, ready chan<- error) {
	defer wg.Done()
	runtime.LockOSThread()
	kl.lp.threadOK = threadCheck(0)
	kl.lp.pin()
	//: everything the loop owns is built on its own thread.
	if err := kl.setup(); err != nil {
		kl.teardown()
		ready <- err
		//: never served.
		return
	}
	ready <- nil
	lp := kl.lp
	//: one iteration per batch until the group stops or the drain completes.
	for {
		n := kl.wait()
		lp.begin()
		kl.handle(n)
		kl.control()
		kl.r.wheel.advance(lp.tick, kl.r.expire)
		lp.shrinkSlab()
		kl.pace()
		lp.h.OnBatchEnd(lp)
		lp.endBatch()
		kl.off = 0
		lp.yield()
		//: finished: stopped, or drained to the last connection.
		if kl.stopReq.Load() || (lp.draining && kl.r.live == 0 && kl.queuesTaken()) {
			//: out of the loop.
			break
		}
	}
	kl.closeAll()
	kl.teardown()
}

// wait blocks for the next batch of events, a tick at most while anything is
// timed or the slab has arenas to give back (#809).
func (kl *kqueueLoop) wait() int {
	var ts *syscall.Timespec
	//: something to time or to unmap: a tick at most.
	if kl.r.live != 0 || !kl.armed || kl.lp.draining || kl.lp.slab.reclaimable() {
		ts = &kl.tick
	}
	n, err := syscall.Kevent(kl.kq, nil, kl.events, ts)
	//: an interrupted wait is an empty batch.
	if err != nil {
		//: nothing reaped.
		return 0
	}
	kl.r.st.completions += uint64(n)
	//: reaped.
	return n
}

// handle dispatches n events: the wake and every connection's first, then the
// listeners, so an accept never hands a new connection a descriptor an event
// of this batch still names.
func (kl *kqueueLoop) handle(n int) {
	kl.ready = kl.ready[:0]
	//: in the order the kernel reported them.
	for i := range n {
		ev := &kl.events[i]
		fd := int(ev.Ident)
		//: by descriptor.
		switch {
		//: posted work.
		case fd == kl.wakeR:
			kl.drainWake()
		//: a connection.
		case fd < len(kl.fdSlot) && kl.fdSlot[fd] != noFDSlot:
			kl.conn(kl.fdSlot[fd]-1, int(ev.Filter), uint16(ev.Flags), int32(ev.Fflags))
		//: a listener, served last; or a descriptor closed earlier this batch.
		default:
			//: a listener of this loop.
			if i := slices.Index(kl.lfds, fd); i >= 0 {
				kl.ready = append(kl.ready, i)
			}
		}
	}
	//: the listeners, once every connection event was served.
	for _, i := range kl.ready {
		kl.accept(kl.lfds[i])
	}
}

// drainWake empties the wake pipe and runs the posted operations.
func (kl *kqueueLoop) drainWake() {
	var buf [wakeDrainBytes]byte
	//: until the pipe is empty; a byte or many say the same.
	for {
		n, err := syscall.Read(kl.wakeR, buf[:])
		//: emptied, or failed: the posted work runs either way.
		if n <= 0 || err != nil {
			break
		}
	}
	//: the Post consumer runs on the loop's thread, checked under loopdebug.
	if kl.lp.threadOK != nil && !kl.lp.threadOK() {
		panic("net/loop: Post consumer off its loop's thread")
	}
	kl.lp.post.drain(kl.lp)
}

// accept takes up to acceptBurst connections from lfd.
func (kl *kqueueLoop) accept(lfd int) {
	//: bounded, so a flood of connections cannot starve the ones open.
	for range acceptBurst {
		nfd, err := acceptConn(lfd)
		//: a failed accept ends the burst or skips to the next connection.
		if err != nil {
			//: the burst is over.
			if kl.acceptFailed(err) {
				//: retried at the next readiness, after the backoff, or at a close.
				return
			}
			continue
		}
		kl.admit(nfd)
	}
}

// acceptFailed handles an accept failure and reports whether it ends the
// burst.
func (kl *kqueueLoop) acceptFailed(err error) bool {
	//: by errno.
	switch err {
	//: the queue is empty: done for now.
	case syscall.EAGAIN:
		//: ends the burst.
		return true
	//: out of descriptors: the listener waits, eviction makes room.
	case syscall.EMFILE, syscall.ENFILE:
		kl.pause()
		//: retried after the backoff, or at a close.
		return true
	//: a connection the peer abandoned in the queue, or a signal.
	case syscall.ECONNABORTED, syscall.EINTR:
		//: the next one.
		return false
	//: anything else ends the burst.
	default:
		//: retried at the next readiness.
		return true
	}
}

// admit registers the accepted descriptor nfd on the ring, or closes it:
// MaxConns and a full table refuse. Both filters are registered, the write
// one disabled, so every later change is an enable or a disable that cannot
// fail for want of a filter.
func (kl *kqueueLoop) admit(nfd int) {
	lp, rg := kl.lp, kl.r
	//: past MaxConns: accepted and closed, a refusal the peer can see.
	if m := lp.sz.maxConns; m > 0 && lp.grp.live.Load() >= int64(m) {
		kl.reject(nfd)
		//: refused.
		return
	}
	slot, ok := rg.take(int32(nfd))
	//: the ring's table is full.
	if !ok {
		kl.reject(nfd)
		//: refused.
		return
	}
	noSigPipe(nfd)
	var ch [2]syscall.Kevent_t
	syscall.SetKevent(&ch[0], nfd, syscall.EVFILT_READ, syscall.EV_ADD)
	syscall.SetKevent(&ch[1], nfd, syscall.EVFILT_WRITE, syscall.EV_ADD|syscall.EV_DISABLE)
	//: registered before OnOpen, so a Write in OnOpen can be flushed.
	if _, err := syscall.Kevent(kl.kq, ch[:], nil, nil); err != nil {
		rg.abandon(slot)
		swallowErr(syscall.Close(nfd))
		//: never served.
		return
	}
	kl.bind(nfd, slot)
	kl.backoff = kqBackoffMin
	rg.opened(slot)
}

// bind records that descriptor fd is the connection on slot.
func (kl *kqueueLoop) bind(fd int, slot int32) {
	//: a descriptor past the map grows it, as a ring's table grows, at least
	//: to fd.
	if fd >= len(kl.fdSlot) {
		n := max(slotCap(len(kl.fdSlot), fd+1+slotStep), fd+1)
		kl.fdSlot = growTo(kl.fdSlot, n)[:n]
	}
	kl.fdSlot[fd] = slot + 1
}

// reject closes nfd at accept, counts the refusal and raises hard pressure.
func (kl *kqueueLoop) reject(nfd int) {
	swallowErr(syscall.Close(nfd))
	kl.lp.grp.rejected.Add(1)
	kl.lp.hard = true
}

// pause disarms the listeners for a backoff after EMFILE or ENFILE, and
// raises hard pressure so idle connections can be evicted.
func (kl *kqueueLoop) pause() {
	kl.disarmListeners()
	kl.rearmAt = kl.lp.tick + kl.backoff
	kl.backoff = min(kl.backoff*2, kqBackoffMax)
	kl.lp.hard = true
	kl.r.st.pauses++
}

// pace re-arms the listeners once a backoff ended, and evicts idle
// connections while the loop is under hard pressure.
func (kl *kqueueLoop) pace() {
	lp := kl.lp
	//: the backoff is over.
	if !kl.armed && !lp.draining && lp.tick >= kl.rearmAt {
		kl.armListeners()
	}
	//: under hard pressure, paced eviction makes room.
	if lp.hard {
		kl.r.evict()
		//: pressure ends once the group is back under its ceiling.
		if kl.armed && (lp.sz.maxConns == 0 || lp.grp.live.Load() < int64(lp.sz.maxConns)) {
			lp.hard = false
		}
	}
}

// conn serves one filter's event of the connection on slot. An end reported
// with an error — EV_EOF and the socket's error in fflags — on a connection
// that no longer reads fails it; a peer's plain end leaves its output going.
func (kl *kqueueLoop) conn(slot int32, filter int, flags uint16, fflags int32) {
	c := &kl.r.conns[slot]
	//: by filter.
	switch filter {
	//: room in the socket: the rest of the output.
	case syscall.EVFILT_WRITE:
		//: still open.
		if c.state != stDead {
			kl.writable(slot)
		}
	//: bytes, or the peer's end: read until the kernel says why.
	case syscall.EVFILT_READ:
		kl.readEvent(slot, flags, fflags)
	}
}

// readEvent serves a read filter's event: a read while the connection reads,
// a failure when the kernel reports a socket error on one that no longer
// does.
func (kl *kqueueLoop) readEvent(slot int32, flags uint16, fflags int32) {
	c := &kl.r.conns[slot]
	//: reading.
	if c.state == stOpen && c.flags&fPeerDone == 0 {
		kl.read(slot)
		//: read.
		return
	}
	//: a socket error reported on a connection that no longer reads.
	if flags&syscall.EV_EOF != 0 && fflags != 0 && (c.state == stOpen || c.state == stClosing) {
		kl.r.fail(slot, rErrno, fflags)
	}
}

// writable resumes slot's output once the socket has room, and stops asking
// for room once it drained.
func (kl *kqueueLoop) writable(slot int32) {
	r := kl.r
	r.conns[slot].flags &^= fSending
	kl.inWritable = true
	kl.flush(r, slot)
	kl.inWritable = false
	c := &r.conns[slot]
	//: still open and drained: the write filter stops asking for room.
	if c.state != stDead && c.flags&fSending == 0 {
		swallowErr(kevChange(kl.kq, int(c.fd), syscall.EVFILT_WRITE, syscall.EV_DISABLE))
	}
}

// waitWritable asks for slot's socket to report room: its write filter is
// enabled, unless writable is already serving it.
func (kl *kqueueLoop) waitWritable(c *connCtl) {
	c.flags |= fSending
	//: writable keeps the filter enabled as it is.
	if kl.inWritable {
		//: nothing to change.
		return
	}
	swallowErr(kevChange(kl.kq, int(c.fd), syscall.EVFILT_WRITE, syscall.EV_ENABLE))
}

// read reads once from slot and hands the bytes to the handler.
func (kl *kqueueLoop) read(slot int32) {
	buf, inSpill, ok := kl.readBuf(slot)
	//: no room to read into.
	if !ok {
		//: closed, or read next time.
		return
	}
	r := kl.r
	c := &r.conns[slot]
	n, err := syscall.Read(int(c.fd), buf)
	//: by outcome.
	switch {
	//: bytes, delivered from where they were read.
	case n > 0:
		kl.received(slot, buf[:n], inSpill)
	//: the peer sent its last byte: what is written still leaves.
	case n == 0 && err == nil:
		c.flags |= fPeerDone
		swallowErr(kevChange(kl.kq, int(c.fd), syscall.EVFILT_READ, syscall.EV_DISABLE))
		r.markDirty(slot)
	//: nothing after all.
	case err == syscall.EAGAIN || err == syscall.EINTR:
	//: the socket failed.
	default:
		r.fail(slot, rErrno, errnoOf(err))
	}
}

// readBuf is where slot's next read lands: behind a spill, straight into it
// so the handler sees one run; otherwise the read arena. ok is false when
// there is no room: the spill failed the connection, or the arena is spent.
func (kl *kqueueLoop) readBuf(slot int32) (buf []byte, inSpill, ok bool) {
	r := kl.r
	//: behind a spill, straight into it.
	if r.hasSpill(slot) {
		buf = r.spillRoom(slot, kqReadChunk)
		//: at the cap, or the budget spent.
		if buf == nil {
			r.fail(slot, rSpill, 0)
			//: closed.
			return nil, true, false
		}
		//: the spill's free tail.
		return buf, true, true
	}
	//: the arena is spent: the level-triggered readiness waits a batch.
	if len(kl.arena)-kl.off < kqMinRead {
		//: read next time.
		return nil, false, false
	}
	//: the arena's next run.
	return kl.arena[kl.off:min(len(kl.arena), kl.off+kqReadChunk)], false, true
}

// received hands the bytes just read for slot to the handler, from where they
// were read.
func (kl *kqueueLoop) received(slot int32, data []byte, inSpill bool) {
	//: in the spill, or in the arena.
	if inSpill {
		kl.r.deliverSpill(slot, len(data))
	} else {
		kl.off += len(data)
		kl.r.deliver(slot, data)
	}
}

// flush implements driver: it writes slot's output until the socket is full.
func (kl *kqueueLoop) flush(r *ring, slot int32) {
	lp := kl.lp
	//: written in order, chunk by chunk, until done or the socket is full.
	for {
		c := &r.conns[slot]
		//: a send already waits on the write filter.
		if c.flags&fSending != 0 {
			//: the readiness resumes it.
			return
		}
		data := lp.slab.data(c.out)
		//: the output has left.
		if len(data) == 0 {
			//: done.
			return
		}
		n, err := syscall.Write(int(c.fd), data)
		//: by outcome.
		switch {
		//: some or all of it left.
		case n > 0:
			c.out.off += int32(n)
			r.wrote(n)
			//: the chunk is drained: it goes back to the slab at once.
			if c.out.off == c.out.len {
				lp.slab.free(c.out)
				c.out = chunkRef{idx: noChunk}
			}
			r.sent(slot, n)
		//: the socket is full: the write filter resumes it.
		case err == syscall.EAGAIN:
			kl.waitWritable(c)
			//: resumed later.
			return
		//: interrupted: again.
		case err == syscall.EINTR:
		//: the socket failed.
		default:
			r.fail(slot, rErrno, errnoOf(err))
			//: closed.
			return
		}
	}
}

// shutdown implements driver: the descriptor closes, which removes both its
// filters from the kqueue, and the slot is free at once, since kqueue holds
// nothing in flight.
func (kl *kqueueLoop) shutdown(r *ring, slot int32) {
	fd := int(r.conns[slot].fd)
	//: the descriptor names no connection from here on.
	if fd < len(kl.fdSlot) {
		kl.fdSlot[fd] = noFDSlot
	}
	swallowErr(syscall.Close(fd))
	r.dropFile(slot)
	r.release(slot)
	//: a close is the moment a paused listener may accept again.
	if !kl.armed && !kl.lp.draining {
		kl.armListeners()
	}
}

// unread implements driver: a one-byte MSG_PEEK on the non-blocking socket
// says whether it holds bytes not yet received — package syscall has no
// FIONREAD on darwin and the BSDs, and OpenBSD takes a raw ioctl only through
// its libc. The byte stays in the socket.
func (kl *kqueueLoop) unread(r *ring, slot int32) bool {
	var b [1]byte
	n, _, err := syscall.Recvfrom(int(r.conns[slot].fd), b[:], syscall.MSG_PEEK)
	//: empty (EAGAIN) or ended (zero bytes) holds nothing; anything else,
	//: unknown included, counts as unread: never evict blind.
	return err != syscall.EAGAIN && (err != nil || n != 0)
}

// control applies the group's drain and stop requests.
func (kl *kqueueLoop) control() {
	lp, r := kl.lp, kl.r
	//: the drain: the listeners leave the kqueue, their queues are taken to
	//: EAGAIN, then handed back for the last loop to close; every settled
	//: connection closes.
	if kl.drainReq.Load() && !lp.draining {
		lp.draining = true
		r.draining = true
		kl.disarmListeners()
	}
	//: while draining, the queues are taken, batch after batch, and a
	//: connection with no partial input closes once its output left.
	if lp.draining {
		kl.takeQueues()
		lp.closeSettled(r)
	}
}

// takeQueues takes every listener's queue to EAGAIN once the listeners left
// the loop's kqueue, admitting each connection, and hands each listener back
// to the group's set, whose last loop closes it (ADR 0196).
func (kl *kqueueLoop) takeQueues() {
	//: each listener not handed back yet.
	for i, fd := range kl.lfds {
		//: over: handed back.
		if !kl.queueDone[i] && kl.takeQueue(i, fd) {
			kl.queueDone[i] = true
			kl.set.handBack(i)
		}
	}
}

// queuesTaken reports whether the loop handed every listener back.
func (kl *kqueueLoop) queuesTaken() bool {
	//: one left, or none.
	return !slices.Contains(kl.queueDone, false)
}

// takeQueue accepts from listener i, lfd, queueTakeBatch at most this batch,
// and reports whether its take is over: epoll's rule (takeAfter).
func (kl *kqueueLoop) takeQueue(i, lfd int) bool {
	//: bounded per batch, so the loop serves between two takes.
	for range queueTakeBatch {
		//: past the budget, or no slot until a close.
		if kl.taken[i] >= queueTakeMax || kl.r.full() {
			//: over, or next batch.
			return kl.taken[i] >= queueTakeMax
		}
		nfd, err := acceptConn(lfd)
		//: a failure ends the take, waits, or skips one connection.
		if err != nil {
			//: by what it says of the queue.
			switch takeAfter(err, kl.lp.grp.live.Load()) {
			//: empty, the listener failed, or no descriptor will come.
			case takeOver:
				//: over.
				return true
			//: no descriptor until a close.
			case takeLater:
				//: next batch.
				return false
			//: the next one.
			case takeNext:
				continue
			}
		}
		kl.taken[i]++
		kl.admit(nfd)
	}
	//: more next batch.
	return false
}

// closeAll closes every connection still open, as the loop stops.
func (kl *kqueueLoop) closeAll() {
	r := kl.r
	//: least recent first; finish unlinks each one.
	for r != nil && r.lruHead != noLink {
		r.fail(r.lruHead, rDrain, 0)
	}
	//: counters published once more.
	if r != nil {
		r.publish()
	}
}

// teardown releases everything the loop owns but the listeners.
func (kl *kqueueLoop) teardown() {
	lp := kl.lp
	//: no more posts reach a stopped loop.
	if lp.post != nil {
		lp.post.close()
	}
	kl.disarmListeners()
	kl.wakeMu.Lock()
	closeFDs([]int{kl.kq, kl.wakeR, kl.wakeW})
	kl.kq, kl.wakeR, kl.wakeW = -1, -1, -1
	kl.wakeMu.Unlock()
	//: the arena and the slab's mappings.
	if kl.arena != nil {
		swallowErr(syscall.Munmap(kl.arena))
	}
	//: every mapping of the slab; the loop is gone either way.
	lp.slab.release()
}

// errnoOf extracts the errno of a syscall error, EIO when it is none.
func errnoOf(err error) int32 {
	//: the kernel's own value.
	if e, ok := err.(syscall.Errno); ok {
		//: as returned.
		return int32(e)
	}
	//: an error the kernel did not give.
	return int32(syscall.EIO)
}
