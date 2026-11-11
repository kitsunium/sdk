// Package server — the datagram groups PacketGroup.HandleLoop serves (#852):
// the refusals, the lanes' count, and the epoll lane every platform has.
package server

import (
	"context"
	"encoding/binary"
	stdnet "net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// laneEcho answers each datagram once and counts each index it saw, per
// lane: a lane's state is its own, indexed by Loop.
type laneEcho struct {
	lanes   atomic.Int64
	seen    [2048]atomic.Uint32
	ends    atomic.Uint64
	byLane  sync.Map
	refused atomic.Uint64
}

// OnStart implements corenet.LoopStarter.
func (h *laneEcho) OnStart(lanes int) {
	h.lanes.Store(int64(lanes))
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *laneEcho) OnPacket(io corenet.PacketLoopIO, from netip.AddrPort, b []byte) {
	//: the index the sender wrote.
	if len(b) >= 4 {
		h.seen[binary.BigEndian.Uint32(b)%2048].Add(1)
	}
	h.byLane.Store(io.Loop(), true)
	//: refused: counted.
	if io.Reply(from, b) != nil {
		h.refused.Add(1)
	}
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (h *laneEcho) OnBatchEnd(corenet.PacketLoopIO) {
	h.ends.Add(1)
}

// startLaneServer starts a server whose datagram group laneEcho serves on
// 127.0.0.1, with env as the operator's variables, and returns it with the
// address bound.
func startLaneServer(t *testing.T, h *laneEcho, env map[string]string, refuse bool, opts ...GroupOption) (*Server, string) {
	t.Helper()
	srv := newServer(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	//: a refused probe, as the leaf answers one (TestPacketLane_failStage).
	if refuse {
		srv.startLane = func(*Server, *boundPacketConn, *PacketGroup) (loop.PacketLane, error) {
			return nil, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
				errs.String("stage", "recvmsg"), errs.String("reason", "injected"))
		}
	}
	srv.PacketGroup("dg", append(opts, Listen("udp4", "127.0.0.1:0"))...).HandleLoop(h)
	//: started.
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	//: the address bound.
	return srv, srv.State().Listeners[0].Address
}

// laneExchange sends n datagrams to addr in windows of 32 and reads each
// echo, within a bound.
func laneExchange(t *testing.T, addr string, n int) {
	t.Helper()
	ua, err := stdnet.ResolveUDPAddr("udp4", addr)
	//: unresolved.
	if err != nil {
		t.Fatal(err)
	}
	c, err := stdnet.DialUDP("udp4", nil, ua)
	//: no client.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	msg := make([]byte, 48)
	buf := make([]byte, 128)
	//: window by window.
	for base := 0; base < n; base += 32 {
		end := min(base+32, n)
		//: the window's datagrams.
		for i := base; i < end; i++ {
			binary.BigEndian.PutUint32(msg, uint32(i))
			_, _ = c.Write(msg)
		}
		//: its echoes.
		for range end - base {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			//: lost.
			if _, err := c.Read(buf); err != nil {
				t.Fatalf("echo lost: %v", err)
			}
		}
	}
}

// laneRow is the uring-multishot-recvmsg row of srv's State.
func laneRow(srv *Server) (corenet.CapabilityValue, bool) {
	//: by name.
	for _, row := range srv.State().Datagrams.Capabilities {
		//: the lane's row.
		if row.Name == corenet.CapabilityURingRecvmsg {
			return row, true
		}
	}
	return corenet.CapabilityValue{}, false
}

// checkOnce fails unless each of the first n indices was seen exactly once.
func checkOnce(t *testing.T, h *laneEcho, n int) {
	t.Helper()
	//: each index.
	for i := range n {
		//: never twice, never lost.
		if v := h.seen[i].Load(); v != 1 {
			t.Fatalf("datagram %d handed on %d times", i, v)
		}
	}
}

// TestPacketLoop_failedProbeServesOnEpollLane is #852's second criterion: the
// io_uring probe refused, the group serves on the batched reader — every
// datagram once —, the row absent with the stage, and no fault.
func TestPacketLoop_failedProbeServesOnEpollLane(t *testing.T) {
	h := &laneEcho{}
	srv, addr := startLaneServer(t, h, nil, true)
	const n = 1000
	laneExchange(t, addr, n)
	_ = srv.Shutdown(context.Background())
	checkOnce(t, h, n)
	row, ok := laneRow(srv)
	//: the row says why.
	if !ok || row.Status != statusAbsent || row.Reason != "recvmsg: injected" {
		t.Fatalf("row = %+v (%v), want absent naming the stage", row, ok)
	}
	//: no fault for a capability that is merely absent.
	if f := srv.State().Faults; len(f) != 0 {
		t.Fatalf("faults = %+v, want none", f)
	}
	//: counted, and one batch end at least.
	if st := srv.State(); st.TotalConns != n || h.ends.Load() == 0 || h.lanes.Load() != 1 {
		t.Fatalf("total=%d ends=%d lanes=%d", st.TotalConns, h.ends.Load(), h.lanes.Load())
	}
}

// TestPacketLoop_disabledByOperator is the switch: KITSUNIUM_NET_DISABLE=
// uring-multishot-recvmsg never tries io_uring, the row reads disabled, and
// the group serves.
func TestPacketLoop_disabledByOperator(t *testing.T) {
	h := &laneEcho{}
	tried := false
	srv := newServer(func(k string) (string, bool) {
		//: only the switch.
		if k == corenet.EnvDisable {
			return string(corenet.CapabilityURingRecvmsg), true
		}
		return "", false
	})
	srv.startLane = func(*Server, *boundPacketConn, *PacketGroup) (loop.PacketLane, error) {
		tried = true
		return nil, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{})
	}
	srv.PacketGroup("dg", Listen("udp4", "127.0.0.1:0")).HandleLoop(h)
	//: started.
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	laneExchange(t, srv.State().Listeners[0].Address, 64)
	row, ok := laneRow(srv)
	//: never tried, reported disabled.
	if tried || !ok || row.Status != statusDisabled {
		t.Fatalf("tried=%v row=%+v (%v), want disabled and untried", tried, row, ok)
	}
}

// TestPacketLoop_lanesPerShard gives Shards(3) three lanes: OnStart(3), and
// Loop answering each lane's index.
func TestPacketLoop_lanesPerShard(t *testing.T) {
	h := &laneEcho{}
	srv, addr := startLaneServer(t, h, nil, true, Shards(3))
	laneExchange(t, addr, 64)
	_ = srv.Shutdown(context.Background())
	//: three lanes, every index within them.
	if h.lanes.Load() != 3 {
		t.Fatalf("OnStart(%d), want 3", h.lanes.Load())
	}
	h.byLane.Range(func(k, _ any) bool {
		//: within the count.
		if i := k.(int); i < 0 || i > 2 {
			t.Fatalf("Loop() = %d outside 0..2", i)
		}
		return true
	})
}

// TestPacketLoop_refusals refuses a loop group with a second handler,
// middlewares, or a Unix datagram address, by name, binding nothing.
func TestPacketLoop_refusals(t *testing.T) {
	cases := []struct {
		name string
		decl func(g *PacketGroup)
		addr GroupOption
		code errs.Code
	}{
		{"handle too", func(g *PacketGroup) {
			g.HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		}, Listen("udp4", "127.0.0.1:0"), corenet.CodeHandlerAlreadySet},
		{"use", func(g *PacketGroup) {
			g.Use(func(h corenet.PacketHandler) corenet.PacketHandler { return h })
		}, Listen("udp4", "127.0.0.1:0"), corenet.CodeLoopOptionUnsupported},
		{"unixgram", func(*PacketGroup) {}, Listen("unixgram", "@kitsunium-dg-refusal"), corenet.CodeLoopOptionUnsupported},
	}
	//: each refusal.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer(func(string) (string, bool) { return "", false })
			g := srv.PacketGroup("dg", c.addr).HandleLoop(&laneEcho{})
			c.decl(g)
			err := srv.Start(context.Background())
			t.Cleanup(func() { _ = srv.Close() })
			//: refused by name.
			if !errs.HasCode(err, c.code) {
				t.Fatalf("Start = %v, want code %v", err, c.code)
			}
		})
	}
}
