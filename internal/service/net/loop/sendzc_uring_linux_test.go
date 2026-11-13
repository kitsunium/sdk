//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's zero-copy send (#969): a flight of at least
// SendZCMin bytes leaves by IORING_OP_SEND_ZC, its chunk held until the
// notification, opted into, switched off, refused.
package loop

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	stdnet "net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sendOps counts the sends a ring hands the kernel, by opcode.
type sendOps struct {
	plain, zc atomic.Uint64
}

// hook is a loopHooks.taken counting the sends.
func (o *sendOps) hook(op, _ uint8, _ uint32) {
	switch op {
	case opSend:
		o.plain.Add(1)
	case opSendZC:
		o.zc.Add(1)
	}
}

// zcCase serves on io_uring with uring-send-zc opted into, rings rings,
// SendZCMin minimum and the sends counted, skipping where the kernel lacks it
// (its row then missing, with the reason).
func zcCase(t *testing.T, rings, minimum int, ops *sendOps) func(*Config) {
	t.Helper()
	return func(cfg *Config) {
		cfg.Value.Capabilities = append(cfg.Value.Capabilities, corenet.CapabilityURingSendZC)
		cfg.Value.Rings = rings
		cfg.Value.SendZCMin = minimum
		cfg.hooks.taken = ops.hook
	}
}

// TestSendZC_opcodeBySize pins the choice: with the row on, a flight of at
// least SendZCMin bytes leaves by opcode 47 and a shorter one by opcode 26.
// The count is the hook's, run once io_uring_enter returned with the SQEs
// taken (uring.see): the kernel may send inline during that enter and the
// peer read the answer first, so each count is waited for, bounded, before it
// is judged.
func TestSendZC_opcodeBySize(t *testing.T) {
	var ops sendOps
	g, addr := codeCase(t, zcCase(t, 1, 16<<10, &ops), corenet.CapabilityURingSendZC)
	c := dial(t, addr)
	r := bufio.NewReader(c)
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "hi\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
	waitFor(t, "the echo's send counted", func() bool { return ops.plain.Load() > 0 })
	if ops.plain.Load() == 0 || ops.zc.Load() != 0 {
		t.Fatalf("a 3-byte answer: plain=%d zc=%d, want plain only", ops.plain.Load(), ops.zc.Load())
	}
	const n = 64 << 10
	if _, err := c.Write([]byte("BIG " + strconv.Itoa(n) + "\n")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, n)
	if _, err := io.ReadFull(r, got); err != nil || string(got) != string(pattern(n)) {
		t.Fatalf("64 KiB answer: %v", err)
	}
	waitFor(t, "the 64 KiB answer's send counted", func() bool { return ops.zc.Load() > 0 })
	if ops.zc.Load() == 0 {
		t.Fatalf("a 64 KiB answer left with no SEND_ZC: plain=%d", ops.plain.Load())
	}
	if row := capRow(t, g.State(), corenet.CapabilityURingSendZC); row.Status != statusOn {
		t.Fatalf("row = %+v, want on", row)
	}
}

// TestSendZC_conformance runs the 8 MiB byte-exact response, read slowly,
// with the row on, on one ring and on three, every flight of it zero-copy,
// and the stale-slot write on one ring.
func TestSendZC_conformance(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	for _, rings := range []int{1, 3} {
		t.Run("rings="+strconv.Itoa(rings), func(t *testing.T) {
			var ops sendOps
			mod := zcCase(t, rings, 1, &ops)
			skipWithoutZC(t, b, mod)
			bigResponseCase(t, b, mod)
			if ops.zc.Load() == 0 || ops.plain.Load() != 0 {
				t.Fatalf("8 MiB with SendZCMin 1: plain=%d zc=%d, want zero-copy only", ops.plain.Load(), ops.zc.Load())
			}
			// The stale-slot case reuses one slot of a one-entry table: on three
			// rings the second connection may land on another ring's table.
			if rings == 1 {
				staleWriteCase(t, b, mod)
			}
		})
	}
}

// skipWithoutZC starts a group with mod applied and skips, after reading the
// row's reason, where the kernel lacks zero-copy sends.
func skipWithoutZC(t *testing.T, b backendCase, mod func(*Config)) {
	t.Helper()
	g, _ := serve(t, b, &lineH{}, mod)
	row := capRow(t, g.State(), corenet.CapabilityURingSendZC)
	if row.Status == statusMissing {
		t.Skipf("uring-send-zc absent on %s: %s", kernelRelease(), row.Reason)
	}
	if row.Status != statusOn {
		t.Fatalf("row = %+v, want on", row)
	}
	_ = g.Close()
}

// TestSendZC_notificationHoldsTheFlight pins the release order on a live
// ring, CQEs played on the loop's thread: a zero-copy flight sent whole is
// held — its chunk not freed, the slot's operations above zero, the slot not
// closed though its connection failed — until the notification, then freed;
// the failed slot is then closed and released, and the next connection takes
// it under a new generation: the generation moves on only after the
// notification.
func TestSendZC_notificationHoldsTheFlight(t *testing.T) {
	var ops sendOps
	g, addr := codeCase(t, zcCase(t, 1, 1, &ops), corenet.CapabilityURingSendZC)
	c := dial(t, addr)
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ul := uringOf(g)
	lp := g.loops[0]
	type seen struct {
		heldChunk, heldOps, heldOpen bool
		released                     bool
		slot                         int32
		gen                          uint32
		err                          string
	}
	done := make(chan seen, 1)
	if err := lp.Post(func(corenet.LoopIO, uintptr) {
		var out seen
		defer func() { done <- out }()
		rr := ul.rings[0]
		slot := int32(-1)
		for i := range rr.r.conns {
			if rr.r.conns[i].state == stOpen {
				slot = int32(i)
				break
			}
		}
		if slot < 0 {
			out.err = "no open slot"
			return
		}
		cc := &rr.r.conns[slot]
		ref, ok := lp.slab.alloc(4096)
		if !ok || cc.flags&fSending != 0 {
			out.err = "no chunk, or a send in flight"
			return
		}
		ref.len = 4096
		// A zero-copy flight as sendFlight leaves it: the send and its
		// notification counted.
		cc.flight, cc.zc, cc.zcPend = ref, zcOn, 1
		cc.ops += 2
		cc.flags |= fSending
		gen := cc.gen
		rr.onSend(slot, gen, &cqe{userData: udPack(udSend, gen, uint32(slot)), res: 4096, flags: cqeFMore})
		rr.r.fail(slot, rErrno, int32(5))
		out.heldChunk = !cc.flight.empty()
		out.heldOps = cc.ops > 0
		out.heldOpen = cc.gen == gen
		rr.onSend(slot, gen, &cqe{userData: udPack(udSend, gen, uint32(slot)), flags: cqeFNotif})
		out.released = cc.flight.empty() && cc.zcPend == 0
		out.slot, out.gen = slot, gen
	}, 0); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != "" {
		t.Fatal(got.err)
	}
	if !got.heldChunk || !got.heldOps || !got.heldOpen {
		t.Fatalf("before the notification: chunk held %v, ops above zero %v, slot not reused %v", got.heldChunk, got.heldOps, got.heldOpen)
	}
	if !got.released {
		t.Fatal("the notification did not release the flight")
	}
	rr := ul.rings[0]
	waitFor(t, "the failed slot closed and released", func() bool {
		freed := false
		onLoop(t, g, func() {
			cc := &rr.r.conns[got.slot]
			freed = cc.state == stFree && cc.ops == 0 && cc.gen == got.gen && slices.Contains(rr.r.free, got.slot)
		})
		return freed
	})
	c2 := dial(t, addr)
	if _, err := c2.Write([]byte("again\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(c2).ReadString('\n'); err != nil || line != "again\n" {
		t.Fatalf("the next connection: %q, %v", line, err)
	}
	var state uint8
	var gen uint32
	onLoop(t, g, func() { state, gen = rr.r.conns[got.slot].state, rr.r.conns[got.slot].gen })
	if state != stOpen || gen == got.gen {
		t.Fatalf("slot %d after the notification: state %d gen %d, want the next connection open on it under a generation past %d", got.slot, state, gen, got.gen)
	}
}

// TestSendZC_switchedOffAndRefused pins the two ways out: the operator's
// switch reads disabled and issues no opcode 47; a refused probe
// (failStage=send_zc) reads missing — opted into, a fault — and every byte
// of the 8 MiB response leaves by opcode 26.
func TestSendZC_switchedOffAndRefused(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	for _, tc := range []struct {
		name   string
		status string
		mod    func(*Config)
	}{
		{"disabled", statusDisabled, func(c *Config) { c.Disabled = []corenet.Capability{corenet.CapabilityURingSendZC} }},
		{"refused", statusMissing, func(c *Config) { c.hooks.failStage = "send_zc" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ops sendOps
			zc := zcCase(t, 1, 1, &ops)
			mod := func(c *Config) { zc(c); tc.mod(c) }
			g, _ := serve(t, b, &lineH{}, mod)
			if row := capRow(t, g.State(), corenet.CapabilityURingSendZC); row.Status != tc.status {
				t.Fatalf("row = %+v, want %s", row, tc.status)
			}
			_ = g.Close()
			bigResponseCase(t, b, mod)
			if ops.zc.Load() != 0 || ops.plain.Load() == 0 {
				t.Fatalf("plain=%d zc=%d, want plain only", ops.plain.Load(), ops.zc.Load())
			}
		})
	}
}

// ipprotoMPTCP is IPPROTO_MPTCP (include/uapi/linux/in.h), absent from Go's
// syscall: the protocol Go's net.Listen asks for by default since Go 1.24.
const ipprotoMPTCP = 262

// TestSendZC_refusedBySocket pins the refusal at send time (#969): a socket
// the kernel cannot send zero-copy on — a Unix stream socket, and one
// accepted from an MPTCP listener, which lacks SOCK_SUPPORT_ZC as every
// connection Go's default listener accepts does — answers EOPNOTSUPP to
// IORING_OP_SEND_ZC. The connection is not failed: the refused flight is
// sent again by IORING_OP_SEND once its notification came, byte-exact at
// 8 MiB read slowly, one opcode 47 issued in all, every later flight of the
// ring plain.
func TestSendZC_refusedBySocket(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	for _, tc := range []struct {
		name   string
		listen func(t *testing.T) (fd int, network, addr string)
	}{
		{"unix", func(t *testing.T) (int, string, string) {
			path := shortSockPath(t)
			return unixListenFD(t, syscall.SOCK_STREAM, path), "unix", path
		}},
		{"mptcp", mptcpListenFD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fd, network, addr := tc.listen(t)
			var ops sendOps
			cfg := Config{Group: t.Name(), Value: corenet.LoopValue{Backend: b.backend, OutputCap: 256 << 10}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard}
			zcCase(t, 1, 1, &ops)(&cfg)
			g, err := Start(cfg)
			if err != nil {
				t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
			}
			t.Cleanup(func() { _ = g.Close() })
			if row := capRow(t, g.State(), corenet.CapabilityURingSendZC); row.Status != statusOn {
				t.Skipf("uring-send-zc %s on %s: %s", row.Status, kernelRelease(), row.Reason)
			}
			c, err := stdnet.DialTimeout(network, addr, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			const n = 8 << 20
			if _, err := c.Write([]byte("BIG " + strconv.Itoa(n) + "\nafter\nBIG 65536\n")); err != nil {
				t.Fatal(err)
			}
			r := bufio.NewReaderSize(c, 1024)
			got := make([]byte, n)
			for off := 0; off < n; {
				k, err := r.Read(got[off:min(n, off+1+off%7919)])
				if err != nil {
					t.Fatalf("after %d bytes: %v", off, err)
				}
				off += k
			}
			if !bytes.Equal(got, pattern(n)) {
				t.Fatal("the refused flight arrived altered or out of order")
			}
			if line, err := r.ReadString('\n'); err != nil || line != "after\n" {
				t.Fatalf("the echo behind it = %q, %v", line, err)
			}
			if _, err := io.ReadFull(r, got[:65536]); err != nil || !bytes.Equal(got[:65536], pattern(65536)) {
				t.Fatalf("the next flight: %v", err)
			}
			if z, p := ops.zc.Load(), ops.plain.Load(); z != 1 || p == 0 {
				t.Fatalf("zc=%d plain=%d, want one refused SEND_ZC, then plain sends only", z, p)
			}
		})
	}
}

// TestSendZC_refusedForMemory pins the other refusal at send time: the
// pages a zero-copy send pins are charged to RLIMIT_MEMLOCK, and a load
// past it is answered ENOMEM. With the limit lowered to zero after Start,
// every zero-copy send of the 8 MiB answer is refused and the flight still
// arrives byte-exact, sent plainly; the ring is not turned plain for good:
// with the limit back, the next large flight leaves zero-copy again. A
// process holding CAP_IPC_LOCK (root, as the kernel lanes run) is charged
// nothing — io_uring accounts pinned pages only without it —, so there the
// flight must leave zero-copy whole, and the test skips once that holds.
func TestSendZC_refusedForMemory(t *testing.T) {
	ipcLock := holdsIPCLock(t)
	var ops sendOps
	_, addr := codeCase(t, zcCase(t, 1, 1, &ops), corenet.CapabilityURingSendZC)
	var was syscall.Rlimit
	if err := syscall.Getrlimit(rlimitMemlock, &was); err != nil {
		t.Fatal(err)
	}
	low := was
	low.Cur = 0
	if err := syscall.Setrlimit(rlimitMemlock, &low); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			if err := syscall.Setrlimit(rlimitMemlock, &was); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(restore)
	c := dial(t, addr)
	const n = 8 << 20
	if _, err := c.Write([]byte("BIG " + strconv.Itoa(n) + "\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(c, 64<<10)
	got := make([]byte, n)
	if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, pattern(n)) {
		t.Fatalf("8 MiB under a zero memlock limit: %v", err)
	}
	refused := ops.zc.Load()
	if ipcLock {
		if refused == 0 || ops.plain.Load() != 0 {
			t.Fatalf("zc=%d plain=%d with CAP_IPC_LOCK, want every send zero-copy and none refused", refused, ops.plain.Load())
		}
		t.Skip("CAP_IPC_LOCK held: io_uring charges no pinned page to RLIMIT_MEMLOCK, so nothing is refused for memory here; the flight left zero-copy whole")
	}
	if refused == 0 || ops.plain.Load() == 0 {
		t.Fatalf("zc=%d plain=%d, want refused SEND_ZCs, then plain sends", refused, ops.plain.Load())
	}
	restore()
	if _, err := c.Write([]byte("BIG 65536\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, got[:65536]); err != nil || !bytes.Equal(got[:65536], pattern(65536)) {
		t.Fatalf("the next flight: %v", err)
	}
	waitFor(t, "a zero-copy send once the limit is back", func() bool { return ops.zc.Load() > refused })
}

// capIPCLock is CAP_IPC_LOCK's bit (include/uapi/linux/capability.h).
const capIPCLock = 14

// holdsIPCLock reports whether this process's effective set holds
// CAP_IPC_LOCK, read from /proc/self/status's CapEff.
func holdsIPCLock(t *testing.T) bool {
	t.Helper()
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(status)) {
		if hex, ok := strings.CutPrefix(line, "CapEff:"); ok {
			eff, perr := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
			if perr != nil {
				t.Fatalf("CapEff %q: %v", hex, perr)
			}
			return eff&(1<<capIPCLock) != 0
		}
	}
	t.Fatal("no CapEff line in /proc/self/status")
	return false
}

// rlimitMemlock is RLIMIT_MEMLOCK (include/uapi/asm-generic/resource.h),
// absent from Go's syscall on Linux.
const rlimitMemlock = 8

// mptcpListenFD binds a non-blocking MPTCP listener on loopback, as Go's
// net.Listen does by default, and skips where the kernel has no MPTCP or it
// is switched off (net.mptcp.enabled = 0).
func mptcpListenFD(t *testing.T) (int, string, string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, ipprotoMPTCP)
	if err != nil {
		t.Skipf("no MPTCP socket here: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return fd, "tcp", "127.0.0.1:" + strconv.Itoa(sa.(*syscall.SockaddrInet4).Port)
}

// TestSendZC_negativeMinRefused refuses a negative SendZCMin by name.
func TestSendZC_negativeMinRefused(t *testing.T) {
	err := CheckCapabilities("g", corenet.LoopValue{SendZCMin: -1})
	var e *errs.Error
	if !errs.HasCode(err, corenet.CodeLoopValueInvalid) || !errors.As(err, &e) {
		t.Fatalf("SendZCMin -1 = %v, want LoopValueInvalid", err)
	}
}
