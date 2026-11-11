//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's zero-copy send (#969): a flight of at least
// SendZCMin bytes leaves by IORING_OP_SEND_ZC, its chunk held until the
// notification, opted into, switched off, refused.
package loop

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

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
		cc.flight, cc.zcFlight, cc.zcPend = ref, true, 1
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

// TestSendZC_negativeMinRefused refuses a negative SendZCMin by name.
func TestSendZC_negativeMinRefused(t *testing.T) {
	err := CheckCapabilities("g", corenet.LoopValue{SendZCMin: -1})
	var e *errs.Error
	if !errs.HasCode(err, corenet.CodeLoopValueInvalid) || !errors.As(err, &e) {
		t.Fatalf("SendZCMin -1 = %v, want LoopValueInvalid", err)
	}
}
