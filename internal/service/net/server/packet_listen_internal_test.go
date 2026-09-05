// Package server — datagram listener construction and the read loop.
package server

import (
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_listenPacket pins that an unusable address is refused BEFORE the OS is
// touched, and that a stream family cannot reach the datagram engine.
//
// The two engines share a declaration surface, so "udp" on the stream side and
// "tcp" on the datagram side are the mistakes a caller actually makes. Refusing
// them by name is what turns a read loop that never receives anything into a
// startup error naming the family.
func Test_listenPacket(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network and addr are the address to bind.
		network string
		addr    string
		// target selects how the address under test is produced.
		target addrTarget
		// wantCode is the refusal, or zero when the bind must succeed.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a udp address", network: "udp", addr: "127.0.0.1:0"},
		{name: "an IPv4-only datagram address", network: "udp4", addr: "127.0.0.1:0"},
		{name: "a unix datagram socket", network: "unixgram", target: addrTempSocket},
		{
			//: the stream engine's family, refused before the OS is touched.
			name: "a stream family on the datagram engine", network: "tcp", addr: "127.0.0.1:0",
			wantCode: corenet.CodeUnsupportedNetwork,
		},
		{name: "an unserved family", network: "carrier-pigeon", addr: "nest", wantCode: corenet.CodeUnsupportedNetwork},
		{name: "no address at all", network: "udp", addr: "", wantCode: corenet.CodeInvalidAddress},
		{name: "an address of only spaces", network: "udp", addr: "   ", wantCode: corenet.CodeInvalidAddress},
		{name: "an address that does not resolve", network: "udp", addr: "256.0.0.1:0", wantCode: corenet.CodeListenFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr := targetAddr(t, c.target, c.addr)

		pc, err := listenPacket(t.Context(), corenet.AddressValue{Network: c.network, Addr: addr})

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("listenPacket(%q, %q) = %v, want code %v", c.network, addr, err, c.wantCode)
			}
			//: a refused bind hands back nothing, or a read loop would start on
			//: a socket that does not exist.
			if pc != nil {
				t.Error("listenPacket returned a socket beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("listenPacket(%q, %q) = %v, want nil", c.network, addr, err)
		}
		defer func() {
			if cerr := pc.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}()
		//: the kernel's own choice, which is what State must report.
		if pc.LocalAddr() == nil || pc.LocalAddr().String() == "" {
			t.Fatal("the socket reports no address")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_boundPacketConn_Close pins that closing is what ends the read loop.
// Closing the socket is the only signal that reliably interrupts a blocking
// read, so a Close that failed to propagate would leave a goroutine parked for
// the life of the process — and holding an in-flight token the drain waits on.
func Test_boundPacketConn_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closeTwice closes the socket a second time.
		closeTwice bool
	}
	tests := []tc{
		{name: "a live socket"},
		//: a concurrent Close may already have closed it during shutdown, which
		//: is why every caller discards the second error rather than reporting it.
		{name: "a socket already closed", closeTwice: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		raw, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		bound := &boundPacketConn{
			group: "g",
			addr:  corenet.AddressValue{Network: "udp", Addr: raw.LocalAddr().String()},
			pc:    raw,
		}
		read := make(chan error, 1)
		go func() {
			_, _, rerr := raw.ReadFrom(make([]byte, 16))
			read <- rerr
		}()

		if cerr := bound.Close(); cerr != nil {
			t.Fatalf("Close = %v, want nil", cerr)
		}

		//: the parked read must come back, which is the entire point.
		select {
		case rerr := <-read:
			if rerr == nil {
				t.Fatal("ReadFrom returned a datagram from a closed socket")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ReadFrom is still parked after Close — the read loop would never end")
		}
		if !c.closeTwice {
			return
		}
		//: the second close fails, and every caller in this package discards it
		//: deliberately rather than reporting a socket that is already gone.
		if cerr := bound.Close(); cerr == nil {
			t.Error("closing an already-closed socket reported success")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packetSize pins that an unset ceiling means the DOMAIN DEFAULT, never
// "unbounded" and never zero.
//
// Zero would size every read slot at one probe byte, so every datagram would be
// counted as oversized and dropped; unbounded would let a misconfigured peer
// make the server allocate a 64 KiB buffer per slot per socket.
func Test_packetSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// given is the group's configured ceiling.
		given int
		// want is the ceiling the read loop must use.
		want int
	}
	tests := []tc{
		{name: "unset takes the default", given: 0, want: defaultMaxPacketSize},
		{name: "negative takes the default", given: -1, want: defaultMaxPacketSize},
		{name: "an explicit ceiling is honoured", given: 1500, want: 1500},
		{name: "a ceiling of one byte", given: 1, want: 1},
		{name: "the largest datagram UDP can carry", given: 65507, want: 65507},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := packetSize(corenet.LimitsValue{MaxPacketSize: c.given})
		//: an unset ceiling must never mean "unbounded".
		if got != c.want {
			t.Fatalf("packetSize(%d) = %d, want %d", c.given, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_batchSize pins that zero means "domain default" and one means "no
// batching", never "unbounded" and never a zero-length read.
//
// A batch of zero would produce an empty slot slice, and the batched reader
// indexes slots[0] — so the wrong default here is a panic on the first read
// rather than a performance regression.
func Test_batchSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// given is the group's configured batch size.
		given int
		// want is the batch size the read loop must use.
		want int
	}
	tests := []tc{
		{name: "unset takes the default", given: 0, want: defaultBatchSize},
		{name: "negative takes the default", given: -4, want: defaultBatchSize},
		{name: "one disables batching", given: 1, want: 1},
		{name: "an explicit size is honoured", given: 32, want: 32},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := batchSize(corenet.LimitsValue{BatchSize: c.given})
		//: a wrong default here would change the syscall count silently, and a
		//: zero would empty the slot slice the reader indexes.
		if got != c.want {
			t.Fatalf("batchSize(%d) = %d, want %d", c.given, got, c.want)
		}
		if got < 1 {
			t.Fatalf("batchSize(%d) = %d — the batched reader indexes slots[0]", c.given, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
