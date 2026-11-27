//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring datagram lane (#852): served, refused at each
// probe stage, split, bounded, handed over.
package loop

import (
	"encoding/binary"
	"errors"
	stdnet "net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// echoLane answers each datagram copies times, and counts.
type echoLane struct {
	copies          int
	packets, ends   atomic.Uint64
	refused, lanes  atomic.Uint64
	seen            [4096]atomic.Uint32
	foreign, sender atomic.Uint64
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *echoLane) OnPacket(io corenet.PacketLoopIO, from netip.AddrPort, b []byte) {
	h.packets.Add(1)
	//: the index the sender wrote, counted once.
	if len(b) >= 4 {
		h.seen[binary.BigEndian.Uint32(b)%4096].Add(1)
	}
	//: the lane's own index.
	if io.Loop() != 3 {
		h.foreign.Add(1)
	}
	//: a sender the socket can answer.
	if !from.IsValid() || !from.Addr().Unmap().IsLoopback() {
		h.sender.Add(1)
	}
	//: each copy.
	for range max(h.copies, 1) {
		//: refused: counted.
		if io.Reply(from, b) != nil {
			h.refused.Add(1)
		}
	}
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (h *echoLane) OnBatchEnd(corenet.PacketLoopIO) {
	h.ends.Add(1)
}

// laneCounters are a lane's counters, every one set.
type laneCounters struct {
	total, oversized, truncated, failures, gsoOver, drops atomic.Uint64
}

// of is the PacketCountersValue view.
func (c *laneCounters) of() PacketCountersValue {
	//: every pointer.
	return PacketCountersValue{
		Total: &c.total, Oversized: &c.oversized, Truncated: &c.truncated,
		ReplyFailures: &c.failures, GSOOversized: &c.gsoOver, KernelDrops: &c.drops,
	}
}

// udpServer binds a UDP socket on 127.0.0.1 and returns it with a duplicated,
// non-blocking, close-on-exec descriptor of it.
func udpServer(t *testing.T) (*stdnet.UDPConn, int) {
	t.Helper()
	pc, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1)})
	//: no socket.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	raw, err := pc.SyscallConn()
	//: no raw access.
	if err != nil {
		t.Fatal(err)
	}
	dup := -1
	var derr error
	cerr := raw.Control(func(fd uintptr) {
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
		//: duplicated.
		if e != 0 {
			derr = e
			return
		}
		dup = int(r)
		derr = syscall.SetNonblock(dup, true)
	})
	//: no duplicate.
	if cerr != nil || derr != nil {
		t.Fatal(cerr, derr)
	}
	//: both.
	return pc, dup
}

// startLane starts a lane on a fresh socket, skipping where io_uring's
// RECVMSG cannot be set up — after checking the refusal is the documented
// one — and stopping it at cleanup.
func startLane(t *testing.T, h corenet.PacketLoopHandler, c *laneCounters, ceiling int) (*packetLane, *stdnet.UDPConn) {
	t.Helper()
	pc, fd := udpServer(t)
	lane, err := StartPacket(&PacketConfig{Group: "dg", FD: fd, Lane: 3, Ceiling: ceiling, Handler: h, Counters: c.of()})
	//: a kernel without the lane: refused by name, never served half.
	if err != nil {
		//: the documented refusal.
		if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) {
			t.Fatalf("StartPacket: %v, want LoopBackendUnavailable", err)
		}
		requireLane(t, err)
	}
	t.Cleanup(lane.Stop)
	//: serving.
	return lane.(*packetLane), pc
}

// requireLane skips a test of the lane where the kernel has none, unless the
// lane requires io_uring (requiredBackends), where it fails.
func requireLane(t *testing.T, err error) {
	t.Helper()
	//: a lane that must run io_uring.
	if strings.Contains(requiredBackends, "iouring") {
		t.Fatalf("io_uring required, the datagram lane refused: %s", laneRefusal(err))
	}
	t.Skipf("io_uring datagram lane unavailable here: %s", laneRefusal(err))
}

// laneRefusal renders a lane's refusal with what its Error leaves out: the
// probe's stage, reason and errno — the fields server's State row is read
// from —, then the kernel's release and RLIMIT_MEMLOCK, against which a kernel
// from 6.14 charges every ring an unprivileged process sets up, summed over
// every process of its user (#1580).
func laneRefusal(err error) string {
	var b strings.Builder
	b.WriteString(err.Error())
	//: every field the refusal carries, in order.
	for _, f := range errs.FieldsOf(err) {
		b.WriteString("; " + f.Key() + "=" + f.StringValue())
	}
	memlock := "unknown"
	var lim syscall.Rlimit
	//: the limit, when the kernel tells it.
	if syscall.Getrlimit(rlimitMemlock, &lim) == nil {
		memlock = strconv.FormatUint(lim.Cur, 10) + "/" + strconv.FormatUint(lim.Max, 10)
	}
	b.WriteString("; kernel=" + kernelRelease() + "; RLIMIT_MEMLOCK=" + memlock)
	return b.String()
}

// client dials the lane's socket from 127.0.0.1.
func client(t *testing.T, pc *stdnet.UDPConn) *stdnet.UDPConn {
	t.Helper()
	c, err := stdnet.DialUDP("udp4", nil, pc.LocalAddr().(*stdnet.UDPAddr))
	//: no client.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	//: dialled.
	return c
}

// exchange sends n datagrams of size bytes in windows of 32 answers — within
// the client's receive buffer — and reads
// want answers to each window, within a bound; it returns how many arrived.
func exchange(t *testing.T, c *stdnet.UDPConn, n, size, want int) int {
	t.Helper()
	buf := make([]byte, 65536)
	msg := make([]byte, size)
	got := 0
	//: window by window.
	window := max(1, 32/want)
	for base := 0; base < n; base += window {
		end := min(base+window, n)
		//: the window's datagrams.
		for i := base; i < end; i++ {
			binary.BigEndian.PutUint32(msg, uint32(i))
			//: a refused send fails the test.
			if _, err := c.Write(msg); err != nil {
				t.Fatal(err)
			}
		}
		//: the window's answers.
		for range (end - base) * want {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			k, err := c.Read(buf)
			//: lost.
			if err != nil {
				t.Fatalf("after %d answers: %v", got, err)
			}
			//: the size sent.
			if k != size {
				t.Fatalf("answer of %d bytes, want %d", k, size)
			}
			got++
		}
	}
	//: all of them.
	return got
}

// TestPacketLane_echo serves 4 000 datagrams on io_uring: each handed on
// once, from a loopback sender, on lane 3, answered once, counted.
func TestPacketLane_echo(t *testing.T) {
	h := &echoLane{}
	var c laneCounters
	lane, pc := startLane(t, h, &c, 8192)
	cl := client(t, pc)
	const n = 4000
	//: every answer.
	if got := exchange(t, cl, n, 64, 1); got != n {
		t.Fatalf("answers = %d, want %d", got, n)
	}
	lane.Stop()
	//: once each.
	for i := range n {
		//: never twice, never lost.
		if v := h.seen[i].Load(); v != 1 {
			t.Fatalf("datagram %d handed on %d times", i, v)
		}
	}
	//: the lane's own index, a loopback sender, a batch end, counted.
	if h.foreign.Load() != 0 || h.sender.Load() != 0 || h.ends.Load() == 0 || c.total.Load() != n || c.failures.Load() != 0 {
		t.Fatalf("foreign=%d sender=%d ends=%d total=%d failures=%d", h.foreign.Load(), h.sender.Load(), h.ends.Load(), c.total.Load(), c.failures.Load())
	}
}

// TestPacketLane_gsoRuns answers each datagram eight times to one peer at one
// size: one UDP_SEGMENT send each, every copy arriving.
func TestPacketLane_gsoRuns(t *testing.T) {
	h := &echoLane{copies: 8}
	var c laneCounters
	lane, pc := startLane(t, h, &c, 8192)
	cl := client(t, pc)
	const n = 200
	//: eight answers a datagram.
	if got := exchange(t, cl, n, 1000, 8); got != 8*n {
		t.Fatalf("answers = %d, want %d", got, 8*n)
	}
	lane.Stop()
	//: nothing refused, and the copies left as UDP_SEGMENT runs, a run a datagram at most.
	if c.failures.Load() != 0 || h.refused.Load() != 0 || lane.gsoSends.Load() == 0 || lane.gsoSends.Load() > n {
		t.Fatalf("failures=%d refused=%d gso sends=%d", c.failures.Load(), h.refused.Load(), lane.gsoSends.Load())
	}
}

// TestPacketLane_ceiling drops a datagram past the ceiling, counted, and
// hands the next one on.
func TestPacketLane_ceiling(t *testing.T) {
	h := &echoLane{}
	var c laneCounters
	lane, pc := startLane(t, h, &c, 100)
	cl := client(t, pc)
	_, _ = cl.Write(make([]byte, 200))
	//: the one under the ceiling comes back.
	if got := exchange(t, cl, 1, 50, 1); got != 1 {
		t.Fatalf("answers = %d", got)
	}
	lane.Stop()
	//: one dropped and counted, one served.
	if c.oversized.Load() != 1 || c.total.Load() != 2 || h.packets.Load() != 1 {
		t.Fatalf("oversized=%d total=%d packets=%d", c.oversized.Load(), c.total.Load(), h.packets.Load())
	}
}

// TestPacketLane_failStage refuses at each stage with LoopBackendUnavailable
// naming it, and closes the descriptor it was handed.
func TestPacketLane_failStage(t *testing.T) {
	//: each stage.
	for _, stage := range []string{"setup", "probe", "buf_ring", "recvmsg"} {
		t.Run(stage, func(t *testing.T) {
			_, fd := udpServer(t)
			var c laneCounters
			_, err := startPacket(&PacketConfig{Group: "dg", FD: fd, Ceiling: 8192, Handler: &echoLane{}, Counters: c.of()}, stage, 0)
			var e *errs.Error
			//: refused, by name.
			if !errors.As(err, &e) || !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) || stageOf(e) != stage {
				t.Fatalf("err = %v, stage %q, want LoopBackendUnavailable at %q", err, stageOf(e), stage)
			}
			_, _, en := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
			//: the descriptor is closed.
			if en != syscall.EBADF {
				t.Fatalf("descriptor still open after the refusal: %v", en)
			}
		})
	}
}

// TestPacketLane_refusalNamesTheKernel refuses at each stage that asks the
// kernel with the errno a loaded host answers there — ENOMEM, a kernel from
// 6.14 charging the ring to RLIMIT_MEMLOCK, and EAGAIN — and requires the
// refusal to name it: the errno field, the reason in the kernel's words,
// never "unsupported", and the ring the stage opened closed. The suite's
// failure message (laneRefusal) carries all of it (#1580).
func TestPacketLane_refusalNamesTheKernel(t *testing.T) {
	type tc struct {
		name  string
		stage string
		errno syscall.Errno
	}
	runCase := func(t *testing.T, c tc) {
		_, fd := udpServer(t)
		before := openFDs(t)
		var lc laneCounters
		_, err := startPacket(&PacketConfig{Group: "dg", FD: fd, Ceiling: 8192, Handler: &echoLane{}, Counters: lc.of()}, c.stage, c.errno)
		//: refused, by the documented code.
		if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) {
			t.Fatalf("err = %v, want LoopBackendUnavailable", err)
		}
		got := map[string]string{}
		//: the refusal's fields, by key.
		for _, f := range errs.FieldsOf(err) {
			got[f.Key()] = f.StringValue()
		}
		//: the stage, the errno and the kernel's words.
		if got["stage"] != c.stage || got["errno"] != strconv.Itoa(int(c.errno)) || !strings.Contains(got["reason"], c.errno.Error()) {
			t.Fatalf("refusal %s: want stage %q, errno %d and a reason naming %q", laneRefusal(err), c.stage, int(c.errno), c.errno.Error())
		}
		msg := laneRefusal(err)
		//: the failure message says what the rendering hides.
		if !strings.Contains(msg, "stage="+c.stage) || !strings.Contains(msg, "RLIMIT_MEMLOCK=") || !strings.Contains(msg, "kernel=") {
			t.Fatalf("laneRefusal = %q, want the stage, the kernel and RLIMIT_MEMLOCK", msg)
		}
		//: the handed descriptor and the stage's ring are closed.
		if after := openFDs(t); after >= before {
			t.Fatalf("descriptors open: %d before the refusal, %d after: the ring or the socket outlived it", before, after)
		}
	}
	for _, c := range []tc{
		{name: "setup ENOMEM", stage: "setup", errno: syscall.ENOMEM},
		{name: "setup EAGAIN", stage: "setup", errno: syscall.EAGAIN},
		{name: "probe ENOMEM", stage: "probe", errno: syscall.ENOMEM},
		{name: "buf_ring ENOMEM", stage: "buf_ring", errno: syscall.ENOMEM},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestWaitWindow_interrupted drives the self-test's wait with an enter a signal
// cuts short: it asks again with what is left of the window, never reads an
// interruption as a window that passed with nothing, and says EINTR when the
// whole window was interrupted (#1580). The kernel cannot be made to deliver the
// signal at that instant, so the enter is the test's.
func TestWaitWindow_interrupted(t *testing.T) {
	const window = 500 * time.Millisecond
	type tc struct {
		name    string
		answers []syscall.Errno
		want    syscall.Errno
		calls   int
		// spent is whether the window must have run out.
		spent bool
	}
	runCase := func(t *testing.T, c tc) {
		var asked []int64
		start := time.Now()
		got := waitWindow(window, func(ts *kernelTimespec) syscall.Errno {
			asked = append(asked, ts.nsec)
			//: a real short wait, as an enter cut by a signal has spent some time: no spinning.
			time.Sleep(time.Millisecond)
			//: one answer a call; the last stands for every later one.
			return c.answers[min(len(asked), len(c.answers))-1]
		})
		//: the answer.
		if got != c.want {
			t.Fatalf("waitWindow = %v, want %v", got, c.want)
		}
		//: how many times it asked, when that is fixed.
		if c.calls > 0 && len(asked) != c.calls {
			t.Fatalf("asked %d times, want %d", len(asked), c.calls)
		}
		//: each ask is bounded by what the window had left, and positive.
		for i, ns := range asked {
			//: never past the window, never nothing.
			if ns <= 0 || ns > int64(window) || (i > 0 && ns > asked[i-1]) {
				t.Fatalf("ask %d waited %d ns of a %d ns window after %v", i, ns, int64(window), asked[:i])
			}
		}
		//: an interrupted window ends with its time.
		if elapsed := time.Since(start); c.spent && elapsed < window {
			t.Fatalf("returned after %v of a %v window", elapsed, window)
		}
	}
	for _, c := range []tc{
		{name: "interrupted twice then woken", answers: []syscall.Errno{syscall.EINTR, syscall.EINTR, 0}, want: 0, calls: 3},
		{name: "interrupted then the tick", answers: []syscall.Errno{syscall.EINTR, errnoETIME}, want: errnoETIME, calls: 2},
		{name: "the tick", answers: []syscall.Errno{errnoETIME}, want: errnoETIME, calls: 1},
		{name: "a refusal", answers: []syscall.Errno{syscall.EBUSY}, want: syscall.EBUSY, calls: 1},
		{name: "interrupted to the end", answers: []syscall.Errno{syscall.EINTR}, want: syscall.EINTR, spent: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestProbeVerdict holds the decision of the self-test's wait: an interruption
// to the end with nothing reaped is refused naming EINTR, in the text and in the
// errno, and so is any other refusal of the wait (EBUSY); a completion reaped despite a last EINTR is not refused, and an ETIME
// with nothing is left to the caller's own refusal (#1580).
func TestProbeVerdict(t *testing.T) {
	type tc struct {
		name      string
		got       cqe
		werr      syscall.Errno
		wantWhy   bool
		wantErrno syscall.Errno
	}
	runCase := func(t *testing.T, c tc) {
		why, errno := probeVerdict(c.got, c.werr)
		//: whether a refusal was named.
		if (why != "") != c.wantWhy {
			t.Fatalf("probeVerdict(%+v, %v) reason = %q, want a reason: %v", c.got, c.werr, why, c.wantWhy)
		}
		//: the errno carried.
		if errno != c.wantErrno {
			t.Fatalf("probeVerdict(%+v, %v) errno = %v, want %v", c.got, c.werr, errno, c.wantErrno)
		}
		//: the reason names the errno it carries.
		if c.wantWhy && !strings.Contains(why, c.wantErrno.Error()) {
			t.Fatalf("reason %q does not name %v", why, c.wantErrno)
		}
	}
	for _, c := range []tc{
		{name: "interrupted to the end, nothing reaped", werr: syscall.EINTR, wantWhy: true, wantErrno: syscall.EINTR},
		{name: "a completion reaped despite a last EINTR", got: cqe{userData: pudProbe, res: 3}, werr: syscall.EINTR},
		{name: "refused wait, nothing reaped", werr: syscall.EBUSY, wantWhy: true, wantErrno: syscall.EBUSY},
		{name: "refused wait, a completion reaped", got: cqe{userData: pudProbe, res: 3}, werr: syscall.EBUSY},
		{name: "ETIME, nothing reaped", werr: errnoETIME},
		{name: "woken, nothing reaped", werr: 0},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// openFDs is how many descriptors the process holds.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	//: no table to read.
	if err != nil {
		t.Fatal(err)
	}
	//: the count.
	return len(ents)
}

// stageOf is the stage field of e.
func stageOf(e *errs.Error) string {
	//: none.
	if e == nil {
		return ""
	}
	//: by name.
	for _, f := range e.Fields() {
		//: the stage.
		if f.Key() == "stage" {
			return f.StringValue()
		}
	}
	return ""
}

// TestPacketLane_handover serves every datagram sent before a handover once:
// the lane's, then what the socket still holds; GRO is off after it.
func TestPacketLane_handover(t *testing.T) {
	h := &echoLane{}
	var c laneCounters
	lane, pc := startLane(t, h, &c, 8192)
	cl := client(t, pc)
	// n stays within what the socket's default receive buffer holds (212 992
	// bytes, each 32-byte datagram charged about 1 KiB of skb) even if the lane
	// reads none before the handover: past it the kernel drops datagrams the
	// lane never saw, and the test would blame the handover for the kernel's
	// loss — as 500 did once under the whole suite's load (lane 378, successor
	// 0, 122 missing).
	const n = 100
	msg := make([]byte, 32)
	//: sent, no answer read.
	for i := range n {
		binary.BigEndian.PutUint32(msg, uint32(i))
		_, _ = cl.Write(msg)
	}
	lane.Handover()
	select {
	case <-lane.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("handover never ended")
	}
	rest := 0
	buf := make([]byte, 64)
	//: what the socket still holds, read by its successor.
	for {
		_ = pc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		k, err := pc.Read(buf)
		//: empty.
		if err != nil {
			break
		}
		h.seen[binary.BigEndian.Uint32(buf[:k])].Add(1)
		rest++
	}
	//: every datagram once.
	for i := range n {
		//: never twice, never lost.
		if v := h.seen[i].Load(); v != 1 {
			t.Fatalf("datagram %d seen %d times (lane %d, successor %d)", i, v, h.packets.Load(), rest)
		}
	}
	raw, _ := pc.SyscallConn()
	gro := -1
	_ = raw.Control(func(fd uintptr) { gro, _ = syscall.GetsockoptInt(int(fd), solUDP, udpGRO) })
	//: GRO off for the successor.
	if gro != 0 {
		t.Fatalf("UDP_GRO = %d after the handover, want 0", gro)
	}
}

// TestPacketLane_allocs pins the hot path at zero allocations: a datagram
// handed on, its reply queued, the batch's SENDMSG built and submitted.
func TestPacketLane_allocs(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pc, fd := udpServer(t)
	cl := client(t, pc)
	var c laneCounters
	lp := &packetLane{
		done: make(chan struct{}), efd: -1,
		cfg: PacketConfig{FD: fd, Ceiling: 8192, Handler: &echoLane{}, Counters: c.of()},
	}
	defer lp.release()
	//: no lane here.
	if stage, why, errno := lp.setup(); stage != "" {
		requireLane(t, refusal("", stage, why, errno))
	}
	name := make([]byte, packetNameRoom)
	binary.LittleEndian.PutUint16(name, afInet)
	binary.BigEndian.PutUint16(name[2:4], uint16(cl.LocalAddr().(*stdnet.UDPAddr).Port))
	copy(name[4:8], []byte{127, 0, 0, 1})
	payload := make([]byte, 64)
	n := testing.AllocsPerRun(200, func() {
		lp.deliver(name, nil, payload, 0)
		lp.endBatch()
		lp.u.submit()
		lp.u.reap(int(lp.u.cqLen), lp.onCQE)
	})
	//: nothing allocated per datagram.
	if n != 0 {
		t.Fatalf("allocs per datagram = %v, want 0", n)
	}
}

// sizeLane records the size of each datagram it is handed.
type sizeLane struct {
	sizes []int
	ends  int
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *sizeLane) OnPacket(_ corenet.PacketLoopIO, _ netip.AddrPort, b []byte) {
	h.sizes = append(h.sizes, len(b))
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (h *sizeLane) OnBatchEnd(corenet.PacketLoopIO) {
	h.ends++
}

// TestPacketLane_groSplits sends one UDP_SEGMENT train of ten 100-byte
// datagrams and a 50-byte one: the lane hands on eleven, in order, whether
// the kernel delivered them coalesced (GRO) or one a message.
func TestPacketLane_groSplits(t *testing.T) {
	h := &sizeLane{}
	var c laneCounters
	lane, pc := startLane(t, h, &c, 8192)
	tx, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	//: no sender.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(tx) }()
	port := pc.LocalAddr().(*stdnet.UDPAddr).Port
	oob := make([]byte, syscall.CmsgSpace(2))
	hdr := (*syscall.Cmsghdr)(unsafe.Pointer(&oob[0]))
	hdr.Level, hdr.Type = int32(solUDP), int32(udpSegment)
	hdr.SetLen(syscall.CmsgLen(2))
	binary.LittleEndian.PutUint16(oob[syscall.CmsgLen(0):], 100)
	//: one send, eleven datagrams.
	if err := syscall.Sendmsg(tx, make([]byte, 1050), oob, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	//: until all eleven are counted.
	for c.total.Load() < 11 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	lane.Stop()
	want := []int{100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 50}
	//: eleven, in order.
	if len(h.sizes) != len(want) {
		t.Fatalf("sizes = %v, want %v", h.sizes, want)
	}
	//: each size.
	for i := range want {
		//: in order.
		if h.sizes[i] != want[i] {
			t.Fatalf("sizes = %v, want %v", h.sizes, want)
		}
	}
}
