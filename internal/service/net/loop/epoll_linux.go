//go:build linux

package loop

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// epollAcceptBurst is the most connections one listener readiness
	// accepts before the loop serves the events already in hand.
	epollAcceptBurst int = 128
	// readChunk is the most bytes one read asks for.
	readChunk int = 16 << 10
	// minRead is the least room in the read arena worth a read: below it the
	// connection is read on the next iteration, its readiness still level.
	minRead int = 2 << 10
	// arenaSize is the loop's read arena: where a batch's received bytes
	// live until OnBatchEnd returns.
	arenaSize int = 2 << 20
	// backoffMin is the first wait before a listener disarmed by EMFILE or
	// ENFILE is armed again, in ticks (5 ms rounds up to one).
	backoffMin int64 = 1
	// backoffMax caps that wait, doubled at each pause, at one second.
	backoffMax int64 = int64(time.Second / tickEvery)
	// tagWake marks the epoll event of the wake eventfd, not a connection.
	tagWake int32 = -1
	// tagListen marks listener i's epoll event, at tagListen-i.
	tagListen int32 = -2
	// efdNonblock is EFD_NONBLOCK (include/uapi/linux/eventfd.h, the O_
	// flag's value).
	efdNonblock uintptr = syscall.O_NONBLOCK
	// efdCloexec is EFD_CLOEXEC (include/uapi/linux/eventfd.h, the O_ flag's
	// value).
	efdCloexec uintptr = syscall.O_CLOEXEC
	// epollExclusive is EPOLLEXCLUSIVE (include/uapi/linux/eventpoll.h, 4.5):
	// one loop woken per incoming connection, not every loop.
	epollExclusive uint32 = 1 << 28
	// siocinq is SIOCINQ, FIONREAD on a socket (asm-generic/ioctls.h).
	siocinq uintptr = 0x541B
	// connEvents are a connection's events while it reads.
	connEvents uint32 = syscall.EPOLLIN | syscall.EPOLLRDHUP
)

// epollRunner runs one epoll loop per thread.
type epollRunner struct {
	loops []*epollLoop
	wg    sync.WaitGroup
	fin   chan struct{}
	// lfds are the group's listeners; set shares them through the drain,
	// and closes what the drain did not once every loop returned.
	lfds []int
	set  *listenerSet
}

// epollLoop is one loop on epoll: one ring, one epoll instance, one thread.
type epollLoop struct {
	lp   *loop
	r    *ring
	epfd int
	evfd int
	// lfds are the listeners, shared with the group's other loops; set is
	// the group's hold on them through the drain.
	lfds []int
	set  *listenerSet
	// armed reports whether the listeners are in the epoll set; rearmAt is
	// the tick an EMFILE backoff ends at, backoff its current length.
	armed   bool
	rearmAt int64
	backoff int64
	// events receives one epoll_wait.
	events []syscall.EpollEvent
	// arena is the read arena; off how much of it this batch used.
	arena []byte
	off   int
	// drainReq and stopReq are the group's requests, set from any goroutine.
	drainReq atomic.Bool
	stopReq  atomic.Bool
	// tid is the loop's thread, for the loopdebug check.
	tid int
	// wakeMu orders a wake against the eventfd's close: a Post or a request
	// that lands as the loop stops must never write a descriptor number the
	// process has reused since.
	wakeMu sync.Mutex
	// evClosed is set once the eventfd is closed.
	evClosed bool
	// excl is EPOLLEXCLUSIVE when the group registers its listeners with it,
	// zero when the operator switched it off or the kernel lacks it.
	excl uint32
}

// startEpoll starts g's loops on epoll.
//
// Goroutine lifecycle: it launches one goroutine per loop, locked to its
// thread, which returns once the loop stopped or drained, and one reaper that
// waits for them all, closes the listeners the drain did not and closes fin,
// then returns.
func startEpoll(g *Group, cfg *Config) error {
	fin := make(chan struct{})
	er := &epollRunner{fin: fin}
	sw := newSwitches(cfg)
	g.caps = newReport(sw, "epoll")
	excl := probeExclusive(sw, &g.caps)
	//: listeners: TCP_NODELAY set before any accept, so every accepted
	//: socket inherits it; a socket that refuses it still serves.
	for _, l := range cfg.Listeners {
		noDelay(l.FD, sw)
		er.lfds = append(er.lfds, l.FD)
	}
	//: each loop takes each listener's queue once, as the group drains.
	er.set = newListenerSet(er.lfds, g.sz.loops)
	ready := make(chan error, g.sz.loops)
	//: one thread per loop, each building its own epoll instance after it
	//: locked itself to its thread.
	for i := range g.sz.loops {
		lp := newLoop(g, *cfg, i, mmapAnon)
		el := &epollLoop{lp: lp, lfds: er.lfds, set: er.set, epfd: -1, evfd: -1, events: make([]syscall.EpollEvent, g.sz.cqBatch), backoff: backoffMin, excl: excl}
		er.loops = append(er.loops, el)
		g.loops = append(g.loops, lp)
		er.wg.Add(1)
		go el.run(&er.wg, ready)
	}
	var first error
	//: every loop says whether it came up.
	for range g.sz.loops {
		//: the first failure is the one reported.
		if err := <-ready; err != nil && first == nil {
			first = err
		}
	}
	g.ctl = er
	//: the reaper owns fin: it is the one goroutine that closes it, once,
	//: after the last loop returned.
	go func() {
		er.wg.Wait()
		er.set.closeRest()
		close(fin)
	}()
	//: a loop that could not start stops the others.
	if first != nil {
		er.stop()
		er.wait()
		//: refused.
		return first
	}
	g.backend = backendEpoll
	//: running.
	return nil
}

// probeExclusive asks a throwaway epoll instance whether it takes
// EPOLLEXCLUSIVE (4.5), on an eventfd of its own, and reports it. It returns
// the flag every listener registration carries.
func probeExclusive(sw capSwitches, rep *capReport) uint32 {
	//: the operator's switch: never probed.
	if sw.disabled(corenet.CapabilityEpollExclusive) {
		//: without.
		return 0
	}
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	//: no instance to ask.
	if err != nil {
		rep.set(corenet.CapabilityEpollExclusive, statusAbsent, "epoll_create1: "+err.Error())
		//: without.
		return 0
	}
	defer closeFDs([]int{epfd})
	efd, _, errno := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	//: nothing to register.
	if errno != 0 {
		rep.set(corenet.CapabilityEpollExclusive, statusAbsent, "eventfd: "+errno.Error())
		//: without.
		return 0
	}
	defer closeFDs([]int{int(efd)})
	ev := new(syscall.EpollEvent{Events: syscall.EPOLLIN | epollExclusive})
	//: the kernel's answer: EINVAL before 4.5.
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, int(efd), ev); err != nil {
		rep.set(corenet.CapabilityEpollExclusive, statusAbsent, "epoll_ctl: "+err.Error())
		//: without.
		return 0
	}
	rep.found(sw, corenet.CapabilityEpollExclusive, true, "")
	//: with.
	return epollExclusive
}

// drain implements runner.
func (er *epollRunner) drain() {
	//: every loop is told, then woken.
	for _, el := range er.loops {
		el.drainReq.Store(true)
		el.wake()
	}
}

// stop implements runner.
func (er *epollRunner) stop() {
	//: every loop is told, then woken.
	for _, el := range er.loops {
		el.stopReq.Store(true)
		el.wake()
	}
}

// wait implements runner.
func (er *epollRunner) wait() {
	<-er.fin
}

// done implements runner.
func (er *epollRunner) done() <-chan struct{} {
	//: closed once every thread returned.
	return er.fin
}

// wake writes the loop's eventfd. It is safe from any goroutine.
func (el *epollLoop) wake() {
	el.wakeMu.Lock()
	defer el.wakeMu.Unlock()
	//: a stopped loop has nothing to wake, and its number may be another's.
	if el.evClosed || el.evfd < 0 {
		//: nothing to do.
		return
	}
	one := new(uint64(1))
	_, _, errno := syscall.RawSyscall(syscall.SYS_WRITE, uintptr(el.evfd), uintptr(unsafe.Pointer(one)), 8)
	//: a full counter already wakes the loop; nothing else can fail here.
	swallowErr(errno)
}

// setup builds the loop's epoll instance, eventfd, arena and ring on its own
// thread.
func (el *epollLoop) setup() error {
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	//: no epoll instance.
	if err != nil {
		//: refused.
		return unavailable("epoll_create1", err)
	}
	el.epfd = epfd
	evfd, _, errno := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
	//: no eventfd.
	if errno != 0 {
		//: refused.
		return unavailable("eventfd2", errno)
	}
	el.evfd = int(evfd)
	//: the wake eventfd is in the set for the loop's life.
	if err := el.ctl(syscall.EPOLL_CTL_ADD, el.evfd, syscall.EPOLLIN, tagWake, 0); err != nil {
		//: refused.
		return unavailable("epoll_ctl eventfd", err)
	}
	arena, err := mmapAnon(arenaSize)
	//: no arena.
	if err != nil {
		//: refused.
		return unavailable("mmap arena", err)
	}
	el.arena = arena
	lp := el.lp
	lp.post = newPostQueue(el.wake)
	el.r = newRing(lp, 0, el)
	lp.rings = []*ring{el.r}
	el.armListeners()
	//: ready to serve.
	return nil
}

// ctl adds or modifies fd in the epoll set, tagging the event with tag and gen.
func (el *epollLoop) ctl(op, fd int, events uint32, tag int32, gen uint32) error {
	ev := new(syscall.EpollEvent{Events: events, Fd: tag, Pad: int32(gen)})
	//: the kernel copies the event.
	return syscall.EpollCtl(el.epfd, op, fd, ev)
}

// armListeners puts every listener in the epoll set, one loop woken per
// connection.
func (el *epollLoop) armListeners() {
	//: armed once, until a backoff or the drain removes them.
	if el.armed || el.lp.draining {
		//: nothing to do.
		return
	}
	//: each listener, tagged by index; one the kernel refuses stays out of
	//: this loop's set, and the group's other loops still accept on it.
	for i, fd := range el.lfds {
		swallowErr(el.ctl(syscall.EPOLL_CTL_ADD, fd, syscall.EPOLLIN|el.excl, tagListen-int32(i), 0))
	}
	el.armed = true
}

// disarmListeners takes every listener out of the epoll set.
func (el *epollLoop) disarmListeners() {
	//: nothing armed.
	if !el.armed {
		//: done.
		return
	}
	//: each listener; one that was never in the set has nothing to remove.
	for _, fd := range el.lfds {
		swallowErr(syscall.EpollCtl(el.epfd, syscall.EPOLL_CTL_DEL, fd, nil))
	}
	el.armed = false
}

// run is the loop's thread.
func (el *epollLoop) run(wg *sync.WaitGroup, ready chan<- error) {
	defer wg.Done()
	runtime.LockOSThread()
	el.tid = syscall.Gettid()
	el.lp.threadOK = threadCheck(el.tid)
	//: everything the loop owns is built on its own thread.
	if err := el.setup(); err != nil {
		el.teardown()
		ready <- err
		//: never served.
		return
	}
	ready <- nil
	lp := el.lp
	//: one iteration per batch until the group stops or the drain completes.
	for {
		n := el.wait()
		started := time.Now()
		lp.tick = lp.now()
		el.handle(n)
		el.control()
		el.r.wheel.advance(lp.tick, el.r.expire)
		el.pace()
		lp.h.OnBatchEnd(lp)
		lp.endBatch(started)
		el.off = 0
		//: finished: stopped, or drained to the last connection.
		if el.stopReq.Load() || (lp.draining && el.r.live == 0) {
			//: out of the loop.
			break
		}
	}
	el.closeAll()
	el.teardown()
}

// wait blocks for the next batch of events, a tick at most while anything is
// timed.
func (el *epollLoop) wait() int {
	timeout := int(tickEvery / time.Millisecond)
	//: nothing to time: sleep until an event.
	if el.r.live == 0 && el.armed && !el.lp.draining {
		timeout = -1
	}
	n, err := syscall.EpollWait(el.epfd, el.events, timeout)
	//: an interrupted wait is an empty batch.
	if err != nil {
		//: nothing reaped.
		return 0
	}
	el.r.st.completions += uint64(n)
	//: reaped.
	return n
}

// handle dispatches n events.
func (el *epollLoop) handle(n int) {
	//: in the order the kernel reported them.
	for i := range n {
		ev := &el.events[i]
		//: by tag.
		switch {
		//: posted work.
		case ev.Fd == tagWake:
			el.drainWake()
		//: a listener is readable.
		case ev.Fd <= tagListen:
			el.accept(el.lfds[tagListen-ev.Fd])
		//: a connection.
		default:
			el.conn(ev.Fd, uint32(ev.Pad), ev.Events)
		}
	}
}

// drainWake reads the eventfd's counter and runs the posted operations.
func (el *epollLoop) drainWake() {
	var buf [8]byte
	_, _, errno := syscall.RawSyscall(syscall.SYS_READ, uintptr(el.evfd), uintptr(unsafe.Pointer(&buf[0])), 8)
	//: the counter is reset by the read; its value does not matter, and an
	//: empty counter (EAGAIN) only means another read already reset it.
	swallowErr(errno)
	//: the Post consumer runs on the loop's thread, checked under loopdebug.
	if el.lp.threadOK != nil && !el.lp.threadOK() {
		panic("net/loop: Post consumer off its loop's thread")
	}
	el.lp.post.drain(el.lp)
}

// accept takes up to epollAcceptBurst connections from lfd.
func (el *epollLoop) accept(lfd int) {
	//: bounded, so a flood of connections cannot starve the ones open.
	for range epollAcceptBurst {
		nfd, _, err := syscall.Accept4(lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		//: a failed accept ends the burst or skips to the next connection.
		if err != nil {
			//: the burst is over.
			if el.acceptFailed(err) {
				//: retried at the next readiness, after the backoff, or at a close.
				return
			}
			continue
		}
		el.admit(nfd)
	}
}

// acceptFailed handles an accept4 failure and reports whether it ends the
// burst.
func (el *epollLoop) acceptFailed(err error) bool {
	//: by errno.
	switch err {
	//: the queue is empty: done for now.
	case syscall.EAGAIN:
		//: ends the burst.
		return true
	//: out of descriptors: the listener waits, eviction makes room.
	case syscall.EMFILE, syscall.ENFILE:
		el.pause()
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
// MaxConns and a full table refuse. A draining loop admits too: what it takes
// from the queue is served, never closed unread.
func (el *epollLoop) admit(nfd int) {
	lp, rg := el.lp, el.r
	//: past MaxConns: accepted and closed, a refusal the peer can see.
	if m := lp.sz.maxConns; m > 0 && lp.grp.live.Load() >= int64(m) {
		el.reject(nfd)
		//: refused.
		return
	}
	slot, ok := rg.take(int32(nfd))
	//: the ring's table is full.
	if !ok {
		el.reject(nfd)
		//: refused.
		return
	}
	//: registered before OnOpen, so a Write in OnOpen can be flushed.
	if err := el.ctl(syscall.EPOLL_CTL_ADD, nfd, connEvents, slot, rg.conns[slot].gen); err != nil {
		rg.abandon(slot)
		swallowErr(syscall.Close(nfd))
		//: never served.
		return
	}
	el.backoff = backoffMin
	rg.opened(slot)
}

// reject closes nfd at accept, counts the refusal and raises hard pressure.
func (el *epollLoop) reject(nfd int) {
	swallowErr(syscall.Close(nfd))
	el.lp.grp.rejected.Add(1)
	el.lp.hard = true
}

// pause disarms the listeners for a backoff after EMFILE or ENFILE, and
// raises hard pressure so idle connections can be evicted.
func (el *epollLoop) pause() {
	el.disarmListeners()
	el.rearmAt = el.lp.tick + el.backoff
	el.backoff = min(el.backoff*2, backoffMax)
	el.lp.hard = true
	el.r.st.pauses++
}

// pace re-arms the listeners once a backoff ended, and evicts idle
// connections while the loop is under hard pressure.
func (el *epollLoop) pace() {
	lp := el.lp
	//: the backoff is over.
	if !el.armed && !lp.draining && lp.tick >= el.rearmAt {
		el.armListeners()
	}
	//: under hard pressure, paced eviction makes room.
	if lp.hard {
		el.r.evict()
		//: pressure ends once the group is back under its ceiling.
		if el.armed && (lp.sz.maxConns == 0 || lp.grp.live.Load() < int64(lp.sz.maxConns)) {
			lp.hard = false
		}
	}
}

// conn serves the events of the connection on slot.
func (el *epollLoop) conn(slot int32, gen, events uint32) {
	r := el.r
	//: an event for a slot closed and reused earlier in the batch.
	if int(slot) >= len(r.conns) || r.conns[slot].gen != gen {
		//: stale.
		return
	}
	//: writable: the rest of the output, and EPOLLOUT dropped once it left.
	if events&syscall.EPOLLOUT != 0 && r.conns[slot].state != stDead {
		el.writable(slot)
	}
	el.readable(slot, events)
}

// writable resumes slot's output on EPOLLOUT, and stops asking for room once
// it drained.
func (el *epollLoop) writable(slot int32) {
	r := el.r
	r.conns[slot].flags &^= fSending
	el.flush(r, slot)
	c := &r.conns[slot]
	//: still open and drained: the mask stops asking for room.
	if c.state != stDead && c.flags&fSending == 0 {
		swallowErr(el.ctl(syscall.EPOLL_CTL_MOD, int(c.fd), el.outEvents(c), slot, c.gen))
	}
}

// readable serves slot's input and hang-up events.
func (el *epollLoop) readable(slot int32, events uint32) {
	r := el.r
	c := &r.conns[slot]
	//: readable, or hung up: read until the kernel says why.
	if events&(syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 && c.state == stOpen && c.flags&fPeerDone == 0 {
		el.read(slot)
		//: read.
		return
	}
	//: a hang-up on a connection that no longer reads.
	if events&(syscall.EPOLLHUP|syscall.EPOLLERR) != 0 && (c.state == stOpen || c.state == stClosing) {
		r.fail(slot, rErrno, int32(syscall.ECONNRESET))
	}
}

// read reads once from slot and hands the bytes to the handler.
func (el *epollLoop) read(slot int32) {
	buf, inSpill, ok := el.readBuf(slot)
	//: no room to read into.
	if !ok {
		//: closed, or read next time.
		return
	}
	r := el.r
	c := &r.conns[slot]
	n, err := syscall.Read(int(c.fd), buf)
	//: by outcome.
	switch {
	//: bytes, delivered from where they were read.
	case n > 0:
		el.received(slot, buf[:n], inSpill)
	//: the peer sent its last byte: what is written still leaves.
	case n == 0 && err == nil:
		c.flags |= fPeerDone
		swallowErr(el.ctl(syscall.EPOLL_CTL_MOD, int(c.fd), el.outEvents(c), slot, c.gen))
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
func (el *epollLoop) readBuf(slot int32) (buf []byte, inSpill, ok bool) {
	r := el.r
	//: behind a spill, straight into it.
	if r.hasSpill(slot) {
		buf = r.spillRoom(slot, readChunk)
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
	if len(el.arena)-el.off < minRead {
		//: read next time.
		return nil, false, false
	}
	//: the arena's next run.
	return el.arena[el.off:min(len(el.arena), el.off+readChunk)], false, true
}

// received hands the n bytes just read for slot to the handler, from where
// they were read.
func (el *epollLoop) received(slot int32, data []byte, inSpill bool) {
	//: in the spill, or in the arena.
	if inSpill {
		el.r.deliverSpill(slot, len(data))
	} else {
		el.off += len(data)
		el.r.deliver(slot, data)
	}
}

// outEvents is the event mask slot wants: reading unless its peer is done,
// writing while output waits on the socket.
func (el *epollLoop) outEvents(c *connCtl) uint32 {
	var ev uint32
	//: still reading: open, and its peer not done.
	if c.state == stOpen && c.flags&fPeerDone == 0 {
		ev |= connEvents
	}
	//: waiting for room in the socket.
	if c.flags&fSending != 0 {
		ev |= syscall.EPOLLOUT
	}
	//: the mask.
	return ev
}

// flush implements driver: it writes slot's output until the socket is full.
func (el *epollLoop) flush(r *ring, slot int32) {
	lp := el.lp
	//: written in order, chunk by chunk, until done or the socket is full.
	for {
		c := &r.conns[slot]
		//: a send already waits on EPOLLOUT.
		if c.flags&fSending != 0 {
			//: the readiness resumes it.
			return
		}
		data := lp.slab.data(c.out)
		//: everything left.
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
			//: the chunk is drained: it goes back to the slab at once, since
			//: no handler holds the output.
			if c.out.off == c.out.len {
				lp.slab.free(c.out)
				c.out = chunkRef{idx: noChunk}
			}
			r.sent(slot, n)
		//: the socket is full: EPOLLOUT resumes it.
		case err == syscall.EAGAIN:
			c.flags |= fSending
			swallowErr(el.ctl(syscall.EPOLL_CTL_MOD, int(c.fd), el.outEvents(c), slot, c.gen))
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

// shutdown implements driver: the descriptor leaves the set and closes, and
// the slot is free at once, since epoll holds nothing in flight.
func (el *epollLoop) shutdown(r *ring, slot int32) {
	fd := int(r.conns[slot].fd)
	//: the close removes it from the set anyway; the DEL only makes it
	//: explicit, and a close that fails leaves nothing to retry.
	swallowErr(syscall.EpollCtl(el.epfd, syscall.EPOLL_CTL_DEL, fd, nil))
	swallowErr(syscall.Close(fd))
	r.release(slot)
	//: a close is the moment a paused listener may accept again.
	if !el.armed && !el.lp.draining {
		el.armListeners()
	}
}

// unread implements driver: SIOCINQ, the bytes the socket holds unread.
func (el *epollLoop) unread(r *ring, slot int32) bool {
	var n int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(r.conns[slot].fd), siocinq, uintptr(unsafe.Pointer(&n)))
	//: unknown counts as unread: never evict blind.
	return errno != 0 || n > 0
}

// control applies the group's drain and stop requests.
func (el *epollLoop) control() {
	lp, r := el.lp, el.r
	//: the drain: the listeners leave the set (CTL_DEL), their queues are
	//: taken to EAGAIN, then handed back for the last loop to close; every
	//: settled connection closes.
	if el.drainReq.Load() && !lp.draining {
		lp.draining = true
		r.draining = true
		el.disarmListeners()
		el.takeQueues()
	}
	//: while draining, a connection with no partial input closes once its
	//: output left.
	if lp.draining {
		//: every open connection is looked at.
		for s := r.lruHead; s != noLink; {
			c := &r.conns[s]
			nxt := c.lruNext
			//: mid-request connections finish first, fresh ones once heard.
			if r.drainable(c) {
				c.state, c.reason = stClosing, rDrain
				r.markDirty(s)
			}
			s = nxt
		}
	}
}

// takeQueues takes every listener's queue to EAGAIN once the listeners left
// the loop's set, admitting each connection, and hands each listener back to
// the group's set, whose last loop closes it: a connection the kernel
// completed before the close is served, not reset by it (plan P6).
func (el *epollLoop) takeQueues() {
	//: each listener, then handed back.
	for i, fd := range el.lfds {
		el.takeQueue(fd)
		el.set.handBack(i)
	}
}

// takeQueue accepts from lfd until its queue is empty, queueTakeMax at most.
func (el *epollLoop) takeQueue(lfd int) {
	//: bounded: past it the arrivals outpace the drain.
	for range queueTakeMax {
		nfd, _, err := syscall.Accept4(lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		//: EAGAIN ends the take, as does a failure that ends a burst.
		if err != nil {
			//: empty, or nothing more to take.
			if el.acceptFailed(err) {
				//: taken.
				return
			}
			continue
		}
		el.admit(nfd)
	}
}

// closeAll closes every connection still open, as the loop stops.
func (el *epollLoop) closeAll() {
	r := el.r
	//: least recent first; finish unlinks each one.
	for r.lruHead != noLink {
		r.fail(r.lruHead, rDrain, 0)
	}
	r.publish()
}

// teardown releases everything the loop owns but the listeners.
func (el *epollLoop) teardown() {
	lp := el.lp
	//: no more posts reach a stopped loop.
	if lp.post != nil {
		lp.post.close()
	}
	el.disarmListeners()
	el.wakeMu.Lock()
	closeFDs([]int{el.epfd, el.evfd})
	el.evClosed = true
	el.wakeMu.Unlock()
	//: the arena and the slab's mappings.
	if el.arena != nil {
		swallowErr(syscall.Munmap(el.arena))
	}
	//: every mapping of the slab; the loop is gone either way.
	for _, m := range lp.slab.mapped {
		swallowErr(syscall.Munmap(m))
	}
	lp.slab.mapped = nil
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
