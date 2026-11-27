//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a drain's catch-up on a DEFER_TASKRUN ring ends its rounds
// only once the kernel holds no task work back, which TASKRUN_FLAG's
// IORING_SQ_TASKRUN says (#1595): the verdict, the probe and the row, and,
// on a kernel from 6.13, more works that post nothing than one enter runs
// ahead of a recv.
package loop

import (
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestCatchUpVerdict holds one catch-up round's verdict to what it reaped
// and to IORING_SQ_TASKRUN after the enter: a round that reaped nothing on a
// TASKRUN_FLAG ring whose kernel still holds work is not caught up, unless
// the flag already kept the ring behind heldBatches batches that reaped
// nothing — it may stay raised over no work (#1595, D1); a ring without the
// flag keeps the reap's word.
func TestCatchUpVerdict(t *testing.T) {
	type tc struct {
		name                  string
		reaped, flagged, held bool
		idle                  int
		want                  bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		//: the verdict.
		if got := caughtUp(c.reaped, c.flagged, c.held, c.idle); got != c.want {
			t.Fatalf("caughtUp(reaped=%v, flagged=%v, held=%v, idle=%d) = %v, want %v", c.reaped, c.flagged, c.held, c.idle, got, c.want)
		}
	}
	//: each case.
	for _, c := range []tc{
		{"nothing reaped, flag clear", false, true, false, 0, true},
		{"nothing reaped, work held", false, true, true, 0, false},
		{"reaped, work held", true, true, true, 0, false},
		{"reaped, flag clear", true, true, false, 0, false},
		{"no flag, nothing reaped", false, false, false, 0, true},
		{"no flag, nothing reaped, the word ignored", false, false, true, 0, true},
		{"no flag, reaped", true, false, false, 0, false},
		{"work held, one batch short of the bound", false, true, true, heldBatches - 1, false},
		{"work held, at the bound: caught up", false, true, true, heldBatches, true},
		{"work held, past the bound: caught up", false, true, true, heldBatches + 1, true},
		{"reaped at the bound: another round", true, true, true, heldBatches, false},
		{"flag clear at the bound: caught up", false, true, false, heldBatches, true},
		{"no flag at the bound, reaped", true, false, false, heldBatches, false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestCaps_taskrunFlag starts an io_uring group and reads where the probe put
// TASKRUN_FLAG: on the DEFER_TASKRUN primary alone, its row on, when
// uring-defer-taskrun is opted into; on no ring, the row available, without
// it; on no ring, the row disabled by the operator's name, when switched off
// — the probe then never tries it, and the drain keeps the reap's word.
func TestCaps_taskrunFlag(t *testing.T) {
	type tc struct {
		name     string
		opt      []corenet.Capability
		off      []corenet.Capability
		status   string
		onPrimal bool
	}
	runCase := func(t *testing.T, c tc) {
		b := backendCase{"iouring", corenet.LoopBackendIOURing}
		//: a host without io_uring has nothing to probe.
		if err := probeBackend(b.backend); err != nil {
			t.Skipf("io_uring not present: %v", err)
		}
		g, addr := serve(t, b, &lineH{}, func(cfg *Config) {
			cfg.Value.Rings = 2
			cfg.Value.Capabilities = c.opt
			cfg.Disabled = c.off
			cfg.DisabledBy = "test"
		})
		st := g.State()
		row := capRow(t, st, corenet.CapabilityURingTaskrunFlag)
		//: a kernel the descent refused it on (none the backend serves).
		if row.Status == statusAbsent {
			t.Skipf("uring-taskrun-flag absent on %s: %s", kernelRelease(), row.Reason)
		}
		//: the row.
		if row.Status != c.status || row.Class != classKernel {
			t.Fatalf("uring-taskrun-flag = %+v, want %s, class %s", row, c.status, classKernel)
		}
		//: switched off: named by the operator's variable.
		if c.status == statusDisabled && row.Reason != "test" {
			t.Fatalf("disabled row's reason %q, want test", row.Reason)
		}
		//: each ring's flags.
		for i, rr := range uringOf(g).rings {
			want := c.onPrimal && i == 0
			//: on the deferred primary alone.
			if rr.u.taskrunFlagged() != want || (want && !rr.u.deferred()) {
				t.Errorf("ring %d: taskrunFlagged=%v deferred=%v, want flagged %v", i, rr.u.taskrunFlagged(), rr.u.deferred(), want)
			}
		}
		echoBigClose(t, addr)
	}
	deferOpt := []corenet.Capability{corenet.CapabilityURingDeferTaskrun}
	flagOff := []corenet.Capability{corenet.CapabilityURingTaskrunFlag}
	//: each case.
	for _, c := range []tc{
		{"with uring-defer-taskrun", deferOpt, nil, statusOn, true},
		{"without uring-defer-taskrun", nil, nil, statusAvailable, false},
		{"switched off", deferOpt, flagOff, statusDisabled, false},
		{"switched off, no opt-in", nil, flagOff, statusDisabled, false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// heldWorks are the poll wake-ups queued ahead of the recv, past the
// IO_LOCAL_TW_DEFAULT_MAX (20) works one GETEVENTS enter runs from 6.13.
const heldWorks = 24

// TestURingCatchUp_heldWorkAheadOfARecv queues, on a DEFER_TASKRUN ring,
// heldWorks task works that post nothing — a recv woken by a byte the test
// then reads itself, which the kernel retries, meets EAGAIN and arms again —
// ahead of one recv whose byte is still there, and runs the drain's catch-up
// on it. With TASKRUN_FLAG the recv's completion is reaped within the
// catch-up on every kernel. Without the flag, from 6.13, the first enter runs
// only twenty works, posts nothing, and the ring is judged caught up with the
// recv still held: the code before #1595, the control that shows the
// construction reaches retry_llist; before 6.13 one enter runs every work
// and the control has nothing to show.
func TestURingCatchUp_heldWorkAheadOfARecv(t *testing.T) {
	type tc struct {
		name    string
		flags   uint32
		reaped  uint64
		behind  bool
		from613 bool
	}
	runCase := func(t *testing.T, c tc) {
		//: the control needs retry_llist, which 6.13 added.
		if c.from613 && !kernelAtLeast(6, 13) {
			t.Skipf("kernel %s runs every queued work in one enter (no retry_llist before 6.13)", kernelRelease())
		}
		u := openWaitRing(t, c.flags)
		ul := &uLoop{lp: &loop{draining: true, sz: sizing{cqBatch: 64}}}
		rr := &uRing{ul: ul, u: u}
		rr.r = newRing(ul.lp, 0, rr)
		rr.r.setBehind(false)
		pairs := make([][2]int, heldWorks+1)
		bufs := make([][16]byte, heldWorks+1)
		//: one recv a socket pair, armed on its empty socket.
		for i := range pairs {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
			//: no pair.
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { syscall.Close(fds[0]); syscall.Close(fds[1]) })
			pairs[i] = [2]int{fds[0], fds[1]}
			s := u.get()
			//: the ring of 16 is full: hand it what it holds.
			if s == nil {
				u.submit()
				s = u.get()
			}
			s.opcode, s.fd = opRecv, int32(fds[0])
			s.addr, s.len = uint64(uintptr(unsafe.Pointer(&bufs[i][0]))), uint32(len(bufs[i]))
		}
		u.submit()
		//: nothing completes before a byte comes.
		if n := u.ready(); n != 0 {
			t.Fatalf("%d completions before any byte", n)
		}
		//: a wake-up each, its byte read away: a work that posts nothing.
		for _, p := range pairs[:heldWorks] {
			//: the wake-up queues the work, the read takes its byte.
			if _, err := syscall.Write(p[1], []byte{1}); err != nil {
				t.Fatal(err)
			}
			//: read before any enter runs the work.
			if n, err := syscall.Read(p[0], make([]byte, 1)); n != 1 || err != nil {
				t.Fatalf("read the byte: %d, %v", n, err)
			}
		}
		//: the recv behind them, its byte kept.
		if _, err := syscall.Write(pairs[heldWorks][1], []byte{2}); err != nil {
			t.Fatal(err)
		}
		behind, cut := rr.catchUp()
		//: the catch-up's verdict and what it reaped.
		if cut || behind != c.behind || rr.r.st.completions != c.reaped {
			t.Fatalf("%s: catchUp = behind %v, cut %v, %d reaped; want behind %v, %d reaped",
				kernelRelease(), behind, cut, rr.r.st.completions, c.behind, c.reaped)
		}
		runtime.KeepAlive(bufs)
	}
	deferred := setupSingleIssuer | setupSubmitAll | setupDeferTaskrun
	//: each case.
	for _, c := range []tc{
		{"taskrun flag: the recv reaped", deferred | setupTaskrunFlag, 1, false, false},
		{"no flag: judged caught up with the recv held", deferred, 0, false, true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// kernelAtLeast reports whether uname -r names a kernel from major.minor.
func kernelAtLeast(major, minor int) bool {
	var got [2]int
	rest := kernelRelease()
	//: the release's first two numbers.
	for i := range got {
		end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
		//: the number runs to the end.
		if end < 0 {
			end = len(rest)
		}
		n, err := strconv.Atoi(rest[:end])
		//: unreadable: not proven.
		if err != nil {
			return false
		}
		got[i], rest = n, strings.TrimPrefix(rest[end:], ".")
	}
	//: by major, then minor.
	return got[0] > major || (got[0] == major && got[1] >= minor)
}

// staleTaskrun runs, on u, the construction that leaves IORING_SQ_TASKRUN
// raised over no work (#1595, D1): a recv linked to a NOP, armed on an empty
// socket, its byte written after, then one enter that waits for two
// completions. The kernel clears the flag only on entering
// __io_run_local_work; the recv's flush queues the NOP on the emptied list,
// which raises it, and, one event short of min_events (6.12 and later; any
// enter on 6.8), the same pass runs the NOP. It reaps both completions,
// enters once more with GETEVENTS and min_complete zero, as the catch-up's
// rounds do, and reports whether the flag still reads raised over an empty
// CQ.
func staleTaskrun(t *testing.T, u *uring) bool {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	//: no pair.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fds[0]); syscall.Close(fds[1]) })
	var buf [16]byte
	s := u.get()
	s.opcode, s.fd, s.flags = opRecv, int32(fds[0]), sqeIOLink
	s.addr, s.len = uint64(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf))
	u.get().opcode = opNop
	u.submit()
	//: the recv waits for its byte, the NOP behind it.
	if n := u.ready(); n != 0 {
		t.Fatalf("%d completions before any byte", n)
	}
	//: the wake-up queues the recv's task work.
	if _, err := syscall.Write(fds[1], []byte{1}); err != nil {
		t.Fatal(err)
	}
	ts := kernelTimespec{sec: 5}
	arg := getEventsArg{ts: uint64(uintptr(unsafe.Pointer(&ts)))}
	fd, reg := u.enterTarget()
	_, errno := uringEnterWait(fd, 0, 2, enterGetEvents|enterExtArg|reg, unsafe.Pointer(&arg), unsafe.Sizeof(arg))
	//: woken on both, or at the bound.
	if errno != 0 && errno != errnoETIME {
		t.Fatalf("enter: %v", errno)
	}
	//: the recv and the NOP.
	if n := u.reap(16, func(*cqe) {}); n != 2 {
		t.Fatalf("%s: %d completions, want the recv and its linked NOP", kernelRelease(), n)
	}
	runtime.KeepAlive(&buf)
	u.flushOverflow()
	//: raised, with nothing in the CQ.
	return u.workHeld() && u.ready() == 0
}

// TestURingTaskrunFlag_raisedOverNoWork pins, on the kernel it runs on, that
// IORING_SQ_TASKRUN may stay raised with no task work held (#1595, D1): the
// flag is exact one way only — clear, no work is held —, so the drain's
// catch-up cannot take a raised flag for held work forever. A ring without
// TASKRUN_FLAG never raises it. A kernel that clears it over the
// construction is said, and the case skipped.
func TestURingTaskrunFlag_raisedOverNoWork(t *testing.T) {
	type tc struct {
		name  string
		flags uint32
		want  bool
	}
	runCase := func(t *testing.T, c tc) {
		u := openWaitRing(t, c.flags)
		got := staleTaskrun(t, u)
		//: the kernel did not leave it raised: nothing to pin here.
		if c.want && !got {
			t.Skipf("kernel %s clears IORING_SQ_TASKRUN over a recv linked to a NOP", kernelRelease())
		}
		//: the word as the kernel left it.
		if got != c.want {
			t.Fatalf("%s: IORING_SQ_TASKRUN raised over no work = %v, want %v", kernelRelease(), got, c.want)
		}
		t.Logf("%s: IORING_SQ_TASKRUN raised over no work = %v", kernelRelease(), got)
	}
	deferred := setupSingleIssuer | setupSubmitAll | setupDeferTaskrun
	//: each case.
	for _, c := range []tc{
		{"taskrun flag: raised, nothing held", deferred | setupTaskrunFlag, true},
		{"no flag: never raised", deferred, false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestURingCatchUp_staleFlagQuietRing drains a quiet ring, batch after
// batch, a tick each, one connection open on it and never heard. With
// IORING_SQ_TASKRUN left raised over no work (staleTaskrun) the catch-up
// takes the flag for held work only heldBatches batches running that reap
// nothing; the ring is then caught up, and the connection drainable once it
// stayed silent drainQuiet, not left open until the Shutdown's deadline
// (#1595, D1). A ring whose flag reads clear is caught up at its first
// batch.
func TestURingCatchUp_staleFlagQuietRing(t *testing.T) {
	type tc struct {
		name   string
		flags  uint32
		stale  bool
		caught int64
	}
	runCase := func(t *testing.T, c tc) {
		u := openWaitRing(t, c.flags)
		//: the construction the kernel did not reproduce.
		if staleTaskrun(t, u) != c.stale {
			t.Skipf("kernel %s: IORING_SQ_TASKRUN raised over no work != %v", kernelRelease(), c.stale)
		}
		ul := &uLoop{lp: &loop{draining: true, sz: sizing{cqBatch: 64}}}
		rr := &uRing{ul: ul, u: u}
		rr.r = newRing(ul.lp, 0, rr)
		ul.rings = []*uRing{rr}
		rr.r.conns = []connCtl{{state: stOpen, spill: chunkRef{idx: noChunk}}}
		caught, closed := int64(-1), int64(-1)
		//: a batch a tick, until the connection may close or drainQuiet passed.
		for tick := int64(0); tick <= drainQuietTicks && closed < 0; tick++ {
			ul.lp.tick = tick
			ul.reapRings()
			ul.catchUp()
			//: the first batch the ring is caught up.
			if caught < 0 && !rr.r.behind() {
				caught = tick
			}
			//: the drain's look.
			if rr.r.drainable(0) {
				closed = tick
			}
		}
		//: caught up within the bound, closed at drainQuiet.
		if caught != c.caught || closed != drainQuietTicks {
			t.Fatalf("%s: caught up at batch %d (want %d), drainable at tick %d (want %d, drainQuiet)",
				kernelRelease(), caught, c.caught, closed, drainQuietTicks)
		}
	}
	deferred := setupSingleIssuer | setupSubmitAll | setupDeferTaskrun
	//: each case.
	for _, c := range []tc{
		{"stale flag: caught up after heldBatches batches", deferred | setupTaskrunFlag, true, int64(heldBatches)},
		{"no flag: caught up at once", deferred, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestURingCatchUp_heldCountRestarts holds the count of batches that
// IORING_SQ_TASKRUN alone kept a ring behind to consecutive ones (#1595): on
// a ring whose flag stays raised over no work, a completion reaped by
// reapRings between two batches (heldMark differs at the next catchUp), or by
// the catch-up's own rounds before they end behind (countHeld's else), starts
// the count again, so the ring is caught up heldBatches batches after the
// last completion and not before.
func TestURingCatchUp_heldCountRestarts(t *testing.T) {
	type tc struct {
		name string
		// between is true when the completion is reaped between two batches,
		// as reapRings does; false when the catch-up's own reap takes it.
		between bool
		// behind is how many batches after the completion's own stay behind.
		behind int
	}
	runCase := func(t *testing.T, c tc) {
		u := openWaitRing(t, setupSingleIssuer|setupSubmitAll|setupDeferTaskrun|setupTaskrunFlag)
		//: the construction the kernel did not reproduce.
		if !staleTaskrun(t, u) {
			t.Skipf("kernel %s: IORING_SQ_TASKRUN not raised over no work", kernelRelease())
		}
		ul := &uLoop{lp: &loop{draining: true, sz: sizing{cqBatch: 64}}}
		rr := &uRing{ul: ul, u: u}
		rr.r = newRing(ul.lp, 0, rr)
		//: two batches behind on the flag alone.
		for range 2 {
			//: not caught up.
			if behind, cut := rr.catchUp(); !behind || cut {
				t.Fatalf("%s: stale flag, catchUp = behind %v, cut %v, want behind", kernelRelease(), behind, cut)
			}
		}
		s := u.get()
		s.opcode, s.userData = opNop, udPack(udOrphanClose, 0, 0)
		u.submit()
		//: the completion waits in the CQ.
		if n := u.ready(); n != 1 {
			t.Fatalf("%d completions after the NOP, want 1", n)
		}
		//: reaped before the next batch, as reapRings does.
		if c.between {
			rr.reapAll()
		}
		n := 0
		//: batches until caught up, bounded.
		for ; n <= 3*heldBatches; n++ {
			//: caught up.
			if behind, _ := rr.catchUp(); !behind {
				break
			}
		}
		//: the batch that reaped it, when the catch-up did, counts as behind.
		if n != c.behind {
			t.Fatalf("%s: %d batches behind after the completion, want %d", kernelRelease(), n, c.behind)
		}
	}
	//: each case.
	for _, c := range []tc{
		{"reaped between batches: the count starts again", true, heldBatches},
		{"reaped in the rounds: the count starts again", false, heldBatches + 1},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestURingCatchUp_flagClearRestartsCount holds that a round reading
// IORING_SQ_TASKRUN clear ends the episode the count belongs to (#1595): the
// flag is exact then, and a count left at the bound would make the next
// episode run on the base rule, the flag vetoing nothing. A raised flag
// leaves the count to grow.
func TestURingCatchUp_flagClearRestartsCount(t *testing.T) {
	type tc struct {
		name  string
		stale bool
		from  int
		want  int
	}
	runCase := func(t *testing.T, c tc) {
		u := openWaitRing(t, setupSingleIssuer|setupSubmitAll|setupDeferTaskrun|setupTaskrunFlag)
		//: raised over no work, when the case needs it.
		if c.stale && !staleTaskrun(t, u) {
			t.Skipf("kernel %s: IORING_SQ_TASKRUN not raised over no work", kernelRelease())
		}
		//: a fresh ring holds no work, and says so.
		if !c.stale && u.workHeld() {
			t.Fatalf("%s: IORING_SQ_TASKRUN raised on a fresh ring", kernelRelease())
		}
		ul := &uLoop{lp: &loop{draining: true, sz: sizing{cqBatch: 64}}}
		rr := &uRing{ul: ul, u: u}
		rr.r = newRing(ul.lp, 0, rr)
		rr.heldIdle = c.from
		rr.catchUp()
		//: the count after one batch.
		if rr.heldIdle != c.want {
			t.Fatalf("%s: heldIdle after one batch = %d, want %d", kernelRelease(), rr.heldIdle, c.want)
		}
	}
	//: each case.
	for _, c := range []tc{
		{"flag clear: the count starts again", false, heldBatches - 1, 0},
		{"flag raised: the count grows", true, 1, 2},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
