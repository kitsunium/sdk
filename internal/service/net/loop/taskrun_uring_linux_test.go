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
// TASKRUN_FLAG ring whose kernel still holds work is not caught up; a ring
// without the flag keeps the reap's word.
func TestCatchUpVerdict(t *testing.T) {
	type tc struct {
		name                  string
		reaped, flagged, held bool
		want                  bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		//: the verdict.
		if got := caughtUp(c.reaped, c.flagged, c.held); got != c.want {
			t.Fatalf("caughtUp(reaped=%v, flagged=%v, held=%v) = %v, want %v", c.reaped, c.flagged, c.held, got, c.want)
		}
	}
	//: each case.
	for _, c := range []tc{
		{"nothing reaped, flag clear", false, true, false, true},
		{"nothing reaped, work held", false, true, true, false},
		{"reaped, work held", true, true, true, false},
		{"reaped, flag clear", true, true, false, false},
		{"no flag, nothing reaped", false, false, false, true},
		{"no flag, nothing reaped, the word ignored", false, false, true, true},
		{"no flag, reaped", true, false, false, false},
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
