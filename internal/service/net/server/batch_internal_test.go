//go:build linux

package server

import (
	stdnet "net"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestLinuxSelectsTheBatchedReader pins the platform selection itself.
//
// Every behavioural datagram test would pass identically on the portable
// fallback, so none of them prove the batched path is actually in use. If a
// refactor broke the type assertion in newDatagramSource, the engine would quietly
// degrade to one syscall per datagram and every other test would stay green.
// This is the only test that would fail.
func TestLinuxSelectsTheBatchedReader(t *testing.T) {
	t.Parallel()
	pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		//: a close failure here would leak a socket into the rest of the suite.
		if cerr := pc.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()

	reader := newDatagramSource(pc)
	//: a portable reader on linux means the batched path silently vanished.
	if _, ok := reader.(*multiReader); !ok {
		t.Fatalf("newDatagramSource returned %T on linux, want *multiReader — "+
			"the engine has silently degraded to one syscall per datagram", reader)
	}
	//: State's degradation report is derived from this, so it must agree.
	if !batchAvailable() {
		t.Fatal("batchAvailable reports false on linux")
	}
}

// TestFallbackWhenNoRawDescriptor pins the honest degradation: a PacketConn with
// no raw descriptor — a test double, or a wrapper — must still be served rather
// than rejected. recvmmsg needs a real descriptor, and falling back keeps such a
// socket working instead of failing at the first read.
func TestFallbackWhenNoRawDescriptor(t *testing.T) {
	t.Parallel()
	reader := newDatagramSource(wrappedConn{})
	//: the portable reader is the floor every socket can use.
	if _, ok := reader.(*portableReader); !ok {
		t.Fatalf("newDatagramSource returned %T for a descriptor-less conn, want *portableReader", reader)
	}
}

// wrappedConn is a PacketConn that deliberately exposes no SyscallConn.
type wrappedConn struct {
	stdnet.PacketConn
}

// TestBatchSizeDefaultsAreSafe pins that zero means "domain default" and one
// means "no batching", never "unbounded" or a zero-length read.
func TestBatchSizeDefaultsAreSafe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		given int
		want  int
	}{
		{name: "unset takes the default", given: 0, want: defaultBatchSize},
		{name: "negative takes the default", given: -4, want: defaultBatchSize},
		{name: "one disables batching", given: 1, want: 1},
		{name: "explicit size is honoured", given: 32, want: 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := batchSize(corenet.LimitsValue{BatchSize: tc.given})
			//: a wrong default here would change the syscall count silently.
			if got != tc.want {
				t.Fatalf("batchSize(%d) = %d, want %d", tc.given, got, tc.want)
			}
		})
	}
}

// TestPacketSizeDefaultsAreSafe pins the same rule for the datagram ceiling.
func TestPacketSizeDefaultsAreSafe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		given int
		want  int
	}{
		{name: "unset takes the default", given: 0, want: defaultMaxPacketSize},
		{name: "negative takes the default", given: -1, want: defaultMaxPacketSize},
		{name: "explicit size is honoured", given: 1500, want: 1500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := packetSize(corenet.LimitsValue{MaxPacketSize: tc.given})
			//: an unset ceiling must never mean "unbounded".
			if got != tc.want {
				t.Fatalf("packetSize(%d) = %d, want %d", tc.given, got, tc.want)
			}
		})
	}
}
