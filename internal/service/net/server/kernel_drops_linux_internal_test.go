//go:build linux

package server

import (
	"context"
	stdnet "net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// rxqOption reads SO_RXQ_OVFL back off the server's only datagram socket: 1
// when the kernel was asked to attach its drop count, 0 when it was not.
func rxqOption(t *testing.T, srv *Server) int {
	t.Helper()
	srv.mu.RLock()
	sc := packetSocket(srv.packetConns[0].pc)
	srv.mu.RUnlock()
	raw, err := sc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var gerr error
	if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soRxqOvfl) }); cerr != nil {
		t.Fatal(cerr)
	}
	if gerr != nil {
		t.Fatal(gerr)
	}
	return value
}

// gatedPackets is a datagram handler that counts what it is given and blocks on
// the first until its gate opens: the receive queue then fills behind it.
type gatedPackets struct {
	gate      chan struct{}
	once      sync.Once
	delivered atomic.Uint64
}

// ServePacket implements corenet.PacketHandler.
func (g *gatedPackets) ServePacket(_ context.Context, _ corenet.Packet) error {
	g.delivered.Add(1)
	g.once.Do(func() { <-g.gate })
	return nil
}

// settled waits until the handler's count stops growing for a while.
func (g *gatedPackets) settled(t *testing.T) uint64 {
	t.Helper()
	last, stable := uint64(0), 0
	deadline := time.Now().Add(10 * time.Second)
	for stable < 10 {
		if time.Now().After(deadline) {
			t.Fatal("the handler never settled")
		}
		time.Sleep(10 * time.Millisecond)
		if now := g.delivered.Load(); now != last {
			last, stable = now, 0
		} else {
			stable++
		}
	}
	return last
}

// TestKernelDrops_aFloodedBufferIsCounted pins the audit's case: a UDP group
// with a receive buffer of 8 KiB, flooded with 10 000 datagrams while its
// handler waits, loses most of them in the kernel — and State says how many.
// A last datagram, sent after the queue drained, carries the final count, so
// the drops equal what was sent less what was delivered: loopback loses
// nothing else.
func TestKernelDrops_aFloodedBufferIsCounted(t *testing.T) {
	t.Parallel()
	h := &gatedPackets{gate: make(chan struct{})}
	srv := New()
	srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), ReceiveBuffer(8<<10)).Handle(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if got := rxqOption(t, srv); got != 1 {
		t.Fatalf("SO_RXQ_OVFL = %d, want it requested", got)
	}
	addr := srv.State().Listeners[0].Address
	conn, err := stdnet.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(conn.Close()) })
	const flood = 10000
	payload := make([]byte, 64)
	sent := 0
	for range flood {
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("send #%d: %v", sent, err)
		}
		sent++
	}
	close(h.gate)
	h.settled(t)
	//: the queue is empty: this one carries the final count.
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("send the last: %v", err)
	}
	sent++
	delivered := h.settled(t)
	st := srv.State()
	if st.Datagrams.KernelDrops == 0 {
		t.Fatalf("a flooded 8 KiB buffer dropped nothing: delivered %d of %d", delivered, sent)
	}
	if want := uint64(sent) - delivered; st.Datagrams.KernelDrops != want {
		t.Errorf("KernelDrops = %d, want sent %d less delivered %d = %d", st.Datagrams.KernelDrops, sent, delivered, want)
	}
	if len(st.Datagrams.Capabilities) != 1 || st.Datagrams.Capabilities[0].Name != corenet.CapabilityUDPRxqOvfl || st.Datagrams.Capabilities[0].Status != statusOn {
		t.Errorf("Capabilities = %+v, want udp-rxq-ovfl on", st.Datagrams.Capabilities)
	}
}

// TestKernelDrops_disabledByTheOperator pins KITSUNIUM_NET_DISABLE=udp-rxq-ovfl:
// the control message is not requested, the count stays zero however many the
// kernel dropped, and the row reads disabled.
func TestKernelDrops_disabledByTheOperator(t *testing.T) {
	t.Parallel()
	env := map[string]string{corenet.EnvDisable: string(corenet.CapabilityUDPRxqOvfl)}
	h := &gatedPackets{gate: make(chan struct{})}
	srv := newServer(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), ReceiveBuffer(8<<10)).Handle(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	if got := rxqOption(t, srv); got != 0 {
		t.Fatalf("SO_RXQ_OVFL = %d under the switch, want it never requested", got)
	}
	conn, err := stdnet.Dial("udp", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(conn.Close()) })
	for range 2000 {
		if _, err := conn.Write(make([]byte, 64)); err != nil {
			t.Fatal(err)
		}
	}
	close(h.gate)
	h.settled(t)
	if _, err := conn.Write(make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	h.settled(t)
	st := srv.State()
	if st.Datagrams.KernelDrops != 0 {
		t.Errorf("KernelDrops = %d under the switch, want 0", st.Datagrams.KernelDrops)
	}
	if rows := st.Datagrams.Capabilities; len(rows) != 1 || rows[0].Status != statusDisabled || rows[0].Reason != corenet.EnvDisable {
		t.Errorf("Capabilities = %+v, want udp-rxq-ovfl disabled by %s", rows, corenet.EnvDisable)
	}
	if h.delivered.Load() == 0 {
		t.Error("the socket delivered nothing")
	}
}

// TestKernelDrops_controlRoomHoldsBothMessages pins the room a batched read
// gives one message: GRO's and the drop count's together, and the caller's
// largest besides, so asking for the second never cuts the first.
func TestKernelDrops_controlRoomHoldsBothMessages(t *testing.T) {
	t.Parallel()
	const callersLargest = 64
	if need := syscall.CmsgSpace(groSegmentBytes) + syscall.CmsgSpace(rxqBytes) + callersLargest; groControlSpace < need {
		t.Fatalf("groControlSpace = %d, want at least %d", groControlSpace, need)
	}
}

// cmsg lays one control message out as the kernel does: the header, the
// payload, and the padding to the next message.
func cmsg(level, typ int32, payload []byte) []byte {
	buf := make([]byte, syscall.CmsgSpace(len(payload)))
	hdr := (*syscall.Cmsghdr)(unsafe.Pointer(&buf[0]))
	hdr.Level, hdr.Type = level, typ
	hdr.SetLen(syscall.CmsgLen(len(payload)))
	copy(buf[syscall.CmsgLen(0):], payload)
	return buf
}

// TestKernelDrops_rxqOverflowWalksTheControlBuffer pins the parser on the
// kernel's own layout: the count is found among other messages, and a
// truncated buffer ends the walk without reading past it.
func TestKernelDrops_rxqOverflowWalksTheControlBuffer(t *testing.T) {
	t.Parallel()
	count := make([]byte, rxqBytes)
	*(*uint32)(unsafe.Pointer(&count[0])) = 7
	other := cmsg(syscall.IPPROTO_UDP, udpGRO, []byte{1, 2, 3, 4})
	both := append(append([]byte(nil), other...), cmsg(syscall.SOL_SOCKET, soRxqOvfl, count)...)
	if got, ok := rxqOverflow(both); !ok || got != 7 {
		t.Errorf("rxqOverflow among others = %d, %v, want 7", got, ok)
	}
	if _, ok := rxqOverflow(other); ok {
		t.Error("rxqOverflow found a count in a buffer that has none")
	}
	if _, ok := rxqOverflow(both[:len(both)-8]); ok {
		t.Error("rxqOverflow read a message cut short")
	}
}
