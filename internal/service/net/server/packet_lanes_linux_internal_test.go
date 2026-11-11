//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package server — a datagram group HandleLoop serves on io_uring (#852).
package server

import (
	"context"
	"testing"
)

// TestPacketLoop_servesOnURing is #852's first criterion short of the veth
// bench: on a kernel that passes the probe the group serves on io_uring, the
// row on, every datagram once, through Shutdown's handover; where the probe
// refuses (an older kernel, a seccomp filter), the row must say why, and
// the test skips.
func TestPacketLoop_servesOnURing(t *testing.T) {
	h := &laneEcho{}
	srv, addr := startLaneServer(t, h, nil, false, Shards(2))
	row, ok := laneRow(srv)
	//: no io_uring lane here: the refusal is reported, never silent.
	if ok && row.Status == statusAbsent && row.Reason != "" {
		t.Skipf("io_uring datagram lane refused here: %s", row.Reason)
	}
	//: on.
	if !ok || row.Status != statusOn {
		t.Fatalf("row = %+v (%v), want on", row, ok)
	}
	const n = 2000
	laneExchange(t, addr, n)
	//: drained and handed over.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkOnce(t, h, n)
	st := srv.State()
	//: counted, two lanes, nothing failed, no fault.
	if st.TotalConns != n || h.lanes.Load() != 2 || st.Datagrams.ReplyFailures != 0 || len(st.Faults) != 0 || h.refused.Load() != 0 {
		t.Fatalf("total=%d lanes=%d failures=%d faults=%+v refused=%d", st.TotalConns, h.lanes.Load(), st.Datagrams.ReplyFailures, st.Faults, h.refused.Load())
	}
}

// TestPacketLoop_closeStopsLanes closes a server whose lanes serve on io_uring:
// Close returns with every lane stopped, the port free to bind again.
func TestPacketLoop_closeStopsLanes(t *testing.T) {
	h := &laneEcho{}
	srv, addr := startLaneServer(t, h, nil, false)
	laneExchange(t, addr, 32)
	//: closed.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	again := newServer(func(string) (string, bool) { return "", false })
	again.PacketGroup("dg", Listen("udp4", addr)).HandleLoop(&laneEcho{})
	//: the address is free again.
	if err := again.Start(context.Background()); err != nil {
		t.Fatalf("rebind after Close: %v", err)
	}
	_ = again.Close()
}
