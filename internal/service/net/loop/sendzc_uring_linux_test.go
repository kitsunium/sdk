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

// zcStep is one CQE a zero-copy send's flight receives: its own (res, with
// F_MORE or not), or a notification.
type zcStep struct {
	notif bool
	res   int32
	more  bool
}

// zcAnswer is one way the kernel may answer a zero-copy send: the CQEs it
// posts, in order, from a flight whose sent bytes (sent of 4096) are pinned
// by pinned earlier sends; whether the peer must read the rest; the
// notifications still owed the slot when everything settled; and whether the
// flight must be held past the scripted CQEs until a last notification.
type zcAnswer struct {
	name   string
	sent   int32
	pinned uint8
	steps  []zcStep
	reads  bool
	owed   int32
	held   bool
}

// zcAnswers is every answer TestSendZC_everyAnswerDelivers plays (#1396).
func zcAnswers() []zcAnswer {
	const n = 4096
	refused := func(errno syscall.Errno, more bool) zcStep { return zcStep{res: -int32(errno), more: more} }
	notif := zcStep{notif: true}
	var out []zcAnswer
	for _, e := range []struct {
		name  string
		errno syscall.Errno
	}{{"eopnotsupp", syscall.EOPNOTSUPP}, {"enomem", syscall.ENOMEM}} {
		out = append(out,
			zcAnswer{name: e.name + "/more-then-notif", steps: []zcStep{refused(e.errno, true), notif}, reads: true},
			zcAnswer{name: e.name + "/more-no-notif", steps: []zcStep{refused(e.errno, true)}, reads: true, owed: 1},
			zcAnswer{name: e.name + "/no-more", steps: []zcStep{refused(e.errno, false)}, reads: true},
			zcAnswer{name: e.name + "/no-more-then-notif", steps: []zcStep{refused(e.errno, false), notif}, reads: true},
			zcAnswer{name: e.name + "/notif-then-more", steps: []zcStep{notif, refused(e.errno, true)}, reads: true},
			zcAnswer{name: e.name + "/notif-then-no-more", steps: []zcStep{notif, refused(e.errno, false)}, reads: true},
		)
	}
	return append(out,
		// A send of the flight the kernel took still pins its first 1024
		// bytes: the rest leaves plainly at once, and the chunk is held until
		// that send's notification, the refused send's coming first.
		zcAnswer{name: "enomem/after-a-pinned-send", sent: 1024, pinned: 1, steps: []zcStep{refused(syscall.ENOMEM, true), notif}, reads: true, held: true},
		zcAnswer{name: "accepted/more-then-notif", steps: []zcStep{{res: n, more: true}, notif}},
		zcAnswer{name: "accepted/no-more", steps: []zcStep{{res: n}}},
		zcAnswer{name: "accepted/no-more-then-notif", steps: []zcStep{{res: n}, notif}},
		zcAnswer{name: "accepted/notif-then-more", steps: []zcStep{notif, {res: n, more: true}}},
		zcAnswer{name: "accepted/notif-then-no-more", steps: []zcStep{notif, {res: n}}},
	)
}

// TestSendZC_everyAnswerDelivers plays, on a live ring and on the loop's
// thread, every answer the kernel may give a zero-copy send of a 4096-byte
// flight (#1396): refused — EOPNOTSUPP, ENOMEM — or taken, its own CQE with
// F_MORE or without, a notification after it, before it, or none. A refused
// flight's bytes reach the peer, by IORING_OP_SEND, without waiting for any
// notification; the slot keeps one operation for each notification promised
// and not come, and the chunk is held only while a send the kernel took
// still pins it; a notification nothing counts is dropped, never taken for a
// failed send; the connection answers an echo after it all.
func TestSendZC_everyAnswerDelivers(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	for _, a := range zcAnswers() {
		t.Run(a.name, func(t *testing.T) { zcAnswerCase(t, b, a) })
	}
}

// zcSlot is the open slot of g's first ring, read on the loop's thread.
type zcSlot struct {
	slot int32
	ops  int32
	err  string
}

// zcAnswerCase serves one connection, plays a's CQEs on its slot and checks
// what reaches the peer and what the slot holds once settled.
func zcAnswerCase(t *testing.T, b backendCase, a zcAnswer) {
	const n = 4096
	g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Rings = 1 })
	c := dial(t, addr)
	r := bufio.NewReader(c)
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "hi\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
	ul, lp := uringOf(g), g.loops[0]
	rr := ul.rings[0]
	done := make(chan zcSlot, 1)
	if err := lp.Post(func(corenet.LoopIO, uintptr) {
		var out zcSlot
		defer func() { done <- out }()
		out.slot = -1
		for i := range rr.r.conns {
			if rr.r.conns[i].state == stOpen {
				out.slot = int32(i)
				break
			}
		}
		if out.slot < 0 {
			out.err = "no open slot"
			return
		}
		cc := &rr.r.conns[out.slot]
		ref, ok := lp.slab.alloc(n)
		if !ok || cc.flags&fSending != 0 {
			out.err = "no chunk, or a send in flight"
			return
		}
		ref.len = n
		copy(lp.slab.data(ref), pattern(n))
		out.ops = cc.ops
		// A zero-copy flight as sendFlight leaves it, a.sent bytes of it sent
		// by sends whose a.pinned notifications are still to come: the send
		// in flight and its notification counted, and each pending one.
		ref.off = a.sent
		cc.flight, cc.zc, cc.zcPend = ref, zcOn, 1+a.pinned
		cc.ops += 2 + int32(a.pinned)
		cc.flags |= fSending
		ud := udPack(udSend, cc.gen, uint32(out.slot))
		for _, st := range a.steps {
			q := &cqe{userData: ud, res: st.res}
			switch {
			case st.notif:
				q.res, q.flags = 0, cqeFNotif
			case st.more:
				q.flags = cqeFMore
			}
			rr.onSend(out.slot, cc.gen, q)
		}
	}, 0); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != "" {
		t.Fatal(got.err)
	}
	if a.reads {
		want := pattern(n)[a.sent:]
		buf := make([]byte, len(want))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(r, buf); err != nil || !bytes.Equal(buf, want) {
			t.Fatalf("the refused flight's %d bytes: %v", len(want), err)
		}
	}
	type settled struct {
		held, sending bool
		ops           int32
		pend, void    uint8
	}
	read := func() settled {
		var s settled
		onLoop(t, g, func() {
			cc := &rr.r.conns[got.slot]
			s = settled{!cc.flight.empty(), cc.flags&fSending != 0, cc.ops, cc.zcPend, cc.zcVoid}
		})
		return s
	}
	if a.held {
		waitFor(t, "the rest sent plainly", func() bool { s := read(); return s.held && s.ops == got.ops+1 })
		if s := read(); !s.held || !s.sending || s.pend != 1 {
			t.Fatalf("before the pinning send's notification: %+v, want the chunk held, pending 1", s)
		}
		done := make(chan struct{})
		if err := lp.Post(func(corenet.LoopIO, uintptr) {
			defer close(done)
			cc := &rr.r.conns[got.slot]
			rr.onSend(got.slot, cc.gen, &cqe{userData: udPack(udSend, cc.gen, uint32(got.slot)), flags: cqeFNotif})
		}, 0); err != nil {
			t.Fatal(err)
		}
		<-done
	}
	want := settled{ops: got.ops + a.owed, void: uint8(a.owed)}
	waitFor(t, "the flight settled", func() bool { return read() == want })
	if _, err := c.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "after\n" {
		t.Fatalf("the echo after it: %q, %v", line, err)
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
// sent again by IORING_OP_SEND at once, its notification waited for by the
// slot alone (#1396), byte-exact at 8 MiB read slowly, one opcode 47 issued
// in all, every later flight of the ring plain.
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
			if z, p := zcTransfer(t, b, fd, network, addr); z != 1 || p == 0 {
				t.Fatalf("zc=%d plain=%d, want one refused SEND_ZC, then plain sends only", z, p)
			}
		})
	}
}

// TestSendZC_goListener pins the transport server binds an opted-in group on
// (#969): a listener of Go's net.ListenConfig with Multipath TCP switched off,
// as server binds every group that opts into uring-send-zc on io_uring, takes
// connections the kernel sends zero-copy on — every flight of the 8 MiB
// answer and the ones behind it by opcode 47, none by opcode 26. The control
// is the base's listener, Go's default, MPTCP since Go 1.24 where the kernel
// has it: its first SEND_ZC is refused and every later flight leaves plainly,
// so the opt-in sent nothing zero-copy past it. The control skips where the
// default listener is plain TCP (no MPTCP in the kernel, or GODEBUG's
// multipathtcp=0), after reading the listener's protocol.
func TestSendZC_goListener(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	t.Run("multipath-off", func(t *testing.T) {
		fd, addr, mptcp := goListenFD(t, false)
		if mptcp {
			_ = syscall.Close(fd)
			t.Fatal("SetMultipathTCP(false) bound an MPTCP socket")
		}
		if z, p := zcTransfer(t, b, fd, "tcp", addr); z == 0 || p != 0 {
			t.Fatalf("zc=%d plain=%d, want every flight zero-copy", z, p)
		}
	})
	t.Run("go-default", func(t *testing.T) {
		fd, addr, mptcp := goListenFD(t, true)
		if !mptcp {
			_ = syscall.Close(fd)
			t.Skipf("Go's default listener is plain TCP here (%s, GODEBUG=%q): nothing to refuse", kernelRelease(), os.Getenv("GODEBUG"))
		}
		if z, p := zcTransfer(t, b, fd, "tcp", addr); z != 1 || p == 0 {
			t.Fatalf("zc=%d plain=%d, want the base's: one refused SEND_ZC, then plain sends only", z, p)
		}
	})
}

// goListenFD binds a loopback listener with Go's net.ListenConfig, Multipath
// TCP as multipath says (true keeps Go's default), and hands back a
// duplicated, non-blocking descriptor of it, as server does, its address, and
// whether the kernel made it an MPTCP socket (SO_PROTOCOL). The descriptor is
// the caller's to hand to a group, which closes it (Listener), or to close: a
// second close here would close whatever reused its number.
func goListenFD(t *testing.T, multipath bool) (int, string, bool) {
	t.Helper()
	var lc stdnet.ListenConfig
	if !multipath {
		lc.SetMultipathTCP(false)
	}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	rc, err := ln.(*stdnet.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	fd, proto := -1, 0
	var derr, perr error
	if cerr := rc.Control(func(raw uintptr) {
		proto, perr = syscall.GetsockoptInt(int(raw), syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
		syscall.ForkLock.RLock()
		fd, derr = syscall.Dup(int(raw))
		if derr == nil {
			syscall.CloseOnExec(fd)
		}
		syscall.ForkLock.RUnlock()
	}); cerr != nil || derr != nil || perr != nil {
		t.Fatalf("the listener's descriptor: %v %v %v", cerr, derr, perr)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	return fd, ln.Addr().String(), proto == ipprotoMPTCP
}

// zcTransfer serves fd on b with uring-send-zc opted into and SendZCMin 1,
// asks for an 8 MiB answer read slowly, an echo and a 64 KiB answer behind
// it, checks each byte-exact, and returns the sends by opcode 47 and 26. It
// skips where the kernel lacks zero-copy sends.
func zcTransfer(t *testing.T, b backendCase, fd int, network, addr string) (zc, plain uint64) {
	t.Helper()
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
			t.Fatalf("after %d bytes: %v; zc=%d plain=%d; %s", off, err, ops.zc.Load(), ops.plain.Load(), zcWhere(g))
		}
		off += k
	}
	if !bytes.Equal(got, pattern(n)) {
		t.Fatal("the 8 MiB answer arrived altered or out of order")
	}
	if line, err := r.ReadString('\n'); err != nil || line != "after\n" {
		t.Fatalf("the echo behind it = %q, %v", line, err)
	}
	if _, err := io.ReadFull(r, got[:65536]); err != nil || !bytes.Equal(got[:65536], pattern(65536)) {
		t.Fatalf("the next flight: %v", err)
	}
	return ops.zc.Load(), ops.plain.Load()
}

// zcWhere says where a zero-copy exchange stands when its peer read
// nothing (#1396): each ring's accepted connections and bytes in and out, and
// its watchdog fires and accept pauses, and every slot in use's flight — how
// it leaves, its notifications pending and owed, its operations — read on the
// loop's thread, bounded.
func zcWhere(g *Group) string {
	var b strings.Builder
	for _, rs := range g.State().RingStates {
		b.WriteString("ring " + strconv.Itoa(rs.Ring) + ": accepted " + strconv.FormatUint(rs.Accepted, 10) +
			", in " + strconv.FormatUint(rs.BytesIn, 10) + ", out " + strconv.FormatUint(rs.BytesOut, 10) +
			", watchdog " + strconv.FormatUint(rs.WatchdogFires, 10) + ", pauses " + strconv.FormatUint(rs.AcceptPauses, 10) + "; ")
	}
	slots := make(chan string, 1)
	if err := g.loops[0].Post(func(corenet.LoopIO, uintptr) {
		var sb strings.Builder
		for _, rr := range uringOf(g).rings {
			for i := range rr.r.conns {
				cc := &rr.r.conns[i]
				if cc.state == stFree {
					continue
				}
				sb.WriteString("slot " + strconv.Itoa(i) + ": state " + strconv.Itoa(int(cc.state)) + " zc " + strconv.Itoa(int(cc.zc)) +
					" pend " + strconv.Itoa(int(cc.zcPend)) + " void " + strconv.Itoa(int(cc.zcVoid)) + " ops " + strconv.Itoa(int(cc.ops)) +
					" sending " + strconv.FormatBool(cc.flags&fSending != 0) + " flight " + strconv.Itoa(int(cc.flight.off)) + "/" + strconv.Itoa(int(cc.flight.len)) + "; ")
			}
		}
		slots <- sb.String()
	}, 0); err != nil {
		return b.String() + "the loop refused the post: " + err.Error()
	}
	select {
	case s := <-slots:
		return b.String() + s
	case <-time.After(2 * time.Second):
		return b.String() + "the loop did not answer within 2 s"
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
// is switched off (net.mptcp.enabled = 0). The descriptor is handed to the
// group, which closes it; it is closed here only when the listen failed.
func mptcpListenFD(t *testing.T) (int, string, string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, ipprotoMPTCP)
	if err != nil {
		t.Skipf("no MPTCP socket here: %v", err)
	}
	var sa syscall.Sockaddr
	if err = syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err == nil {
		if err = syscall.Listen(fd, 128); err == nil {
			sa, err = syscall.Getsockname(fd)
		}
	}
	if err != nil {
		_ = syscall.Close(fd)
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
