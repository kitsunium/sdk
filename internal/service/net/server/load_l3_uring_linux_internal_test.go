//go:build linux

// Package server — L3's io_uring datagram lane engine (#852): the pair
// recvmmsg:uring-recvmsg, reported first, gated once its ratio holds.
package server

import (
	stdnet "net"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// l3Lane counts what an io_uring datagram lane hands on, on the lane's
// thread, and adds it to its reader once a batch: an atomic add a datagram
// would weigh on the engine the bench measures.
type l3Lane struct {
	r *l3Reader
	n uint64
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *l3Lane) OnPacket(corenet.PacketLoopIO, netip.AddrPort, []byte) {
	h.n++
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (h *l3Lane) OnBatchEnd(corenet.PacketLoopIO) {
	h.r.datagrams.Add(h.n)
	h.r.receives.Add(1)
	h.n = 0
}

// listenL3URing binds a socket for the io_uring lane and serves it there,
// SO_RXQ_OVFL asked for as the batched readers ask for it.
func listenL3URing(bind string) (*l3Reader, error) {
	pc, err := stdnet.ListenPacket("udp", bind)
	if err != nil {
		return nil, err
	}
	if udp, ok := pc.(*stdnet.UDPConn); ok {
		_ = udp.SetReadBuffer(l3ReadBuffer)
	}
	r := &l3Reader{pc: pc}
	fd, err := dupPacketFD(&boundPacketConn{pc: pc})
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, soRxqOvfl, 1)
	var total, oversized, failures, gso atomic.Uint64
	_, err = loop.StartPacket(&loop.PacketConfig{
		Group: "l3", FD: fd, Ceiling: l3DatagramSize, Handler: &l3Lane{r: r},
		Counters: loop.PacketCountersValue{
			Total: &total, Oversized: &oversized, Truncated: &r.truncated,
			ReplyFailures: &failures, GSOOversized: &gso, KernelDrops: &r.kernel,
		},
	})
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	return r, nil
}

// TestLoadL3_wiredURing is TestLoadL3_wired for the io_uring lane, beside the
// GRO reader: where the kernel refuses the lane, the refusal is named and the
// test skips.
func TestLoadL3_wiredURing(t *testing.T) {
	if r, err := listenL3URing("127.0.0.1:0"); err != nil {
		t.Skipf("io_uring datagram lane unavailable here: %v", err)
	} else {
		_ = r.pc.Close()
	}
	wireL3(t, []string{l3EngineURing, l3EngineB})
}
