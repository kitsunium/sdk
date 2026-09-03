// Package server — datagram listener construction and the read loop.
package server

import (
	"context"
	stdnet "net"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// defaultMaxPacketSize is the largest datagram accepted when a group sets
	// no ceiling. It comfortably exceeds a typical MTU while staying far below
	// the 64 KiB theoretical maximum, so a misconfigured peer cannot make the
	// server allocate a large buffer per socket.
	defaultMaxPacketSize int = 8192
	// defaultBatchSize is how many datagrams one read attempts to collect.
	defaultBatchSize int = 16
)

// packetNetworks are the datagram families the engine serves.
var packetNetworks = map[string]struct{}{
	"udp": {}, "udp4": {}, "udp6": {}, "unixgram": {},
}

// listenPacket binds one datagram socket.
func listenPacket(ctx context.Context, addr corenet.AddressValue) (pc stdnet.PacketConn, err error) {
	//: reject an unserved family before touching the OS.
	if _, ok := packetNetworks[addr.Network]; !ok {
		//: refuse rather than bind something the read loop cannot serve.
		return nil, errs.Wrap(corenet.UnsupportedNetwork, errs.WrapParams{},
			errs.String("network", addr.Network))
	}
	//: an empty target would otherwise fail obscurely inside the stdlib.
	if strings.TrimSpace(addr.Addr) == "" {
		//: refuse with the address that was actually supplied.
		return nil, errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
			errs.String("address", addr.String()))
	}
	var cfg stdnet.ListenConfig
	bound, lerr := cfg.ListenPacket(ctx, addr.Network, addr.Addr)
	//: the OS refused the bind — in use, permission, bad interface.
	if lerr != nil {
		//: surface the address that could not be bound.
		return nil, errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
			errs.String("address", addr.String()), errs.String("cause", lerr.Error()))
	}
	//: the socket is bound and ready to read.
	return bound, nil
}

// boundPacketConn pairs a live datagram socket with the group that owns it.
type boundPacketConn struct {
	// group is the owning group's name.
	group string
	// addr is the address as requested.
	addr corenet.AddressValue
	// pc is the live socket.
	pc stdnet.PacketConn
}

// Close releases the socket, which is what ends its read loop.
func (b *boundPacketConn) Close() error {
	//: closing is the only signal that reliably interrupts a blocking read.
	return b.pc.Close()
}

// packetSize returns the group's datagram ceiling.
func packetSize(limits corenet.LimitsValue) int {
	//: zero means "use the domain default", never "unbounded".
	if limits.MaxPacketSize <= 0 {
		//: apply the domain default rather than leave it unbounded.
		return defaultMaxPacketSize
	}
	//: the caller's explicit ceiling.
	return limits.MaxPacketSize
}

// batchSize returns how many datagrams one read should attempt to collect.
func batchSize(limits corenet.LimitsValue) int {
	//: zero means "use the domain default"; one disables batching explicitly.
	if limits.BatchSize <= 0 {
		//: apply the platform default; one would disable batching explicitly.
		return defaultBatchSize
	}
	//: the caller's explicit batch size.
	return limits.BatchSize
}
