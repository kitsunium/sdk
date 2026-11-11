//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring datagram lane (#852): served, refused at each
// probe stage, split, bounded, handed over.
package loop

import (
	"encoding/binary"
	"errors"
	stdnet "net"
	"net/netip"
	"runtime"
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
		t.Fatalf("io_uring required, the datagram lane refused: %v", err)
	}
	t.Skipf("io_uring datagram lane unavailable here: %v", err)
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
			_, err := startPacket(&PacketConfig{Group: "dg", FD: fd, Ceiling: 8192, Handler: &echoLane{}, Counters: c.of()}, stage)
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
	if stage, why := lp.setup(); stage != "" {
		requireLane(t, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{}, errs.String("stage", stage), errs.String("reason", why)))
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
