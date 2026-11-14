// Package server — ShareAddress's refusals and the probe's scope (#1330),
// judged on every OS: the branch only Windows, Solaris and illumos take is the
// platform answer passed in.
package server

import (
	stdnet "net"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_shareRefusalFor pins the refusals: none without the option, the
// platform's UnsupportedPlatform before anything else where there is no
// SO_REUSEPORT — a Unix socket's address included —, the family's refusal for
// a Unix socket where there is, nothing for IP addresses.
func Test_shareRefusalFor(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		share     bool
		network   string
		reusePort bool
		want      errs.Code
	}
	tests := []tc{
		{name: "no option", network: "unix", reusePort: false},
		{name: "a TCP address", share: true, network: "tcp", reusePort: true},
		{name: "a UDP address", share: true, network: "udp6", reusePort: true},
		{name: "a Unix socket", share: true, network: "unix", reusePort: true, want: corenet.CodeLoopOptionUnsupported},
		{name: "no SO_REUSEPORT", share: true, network: "tcp", reusePort: false, want: coreproc.CodeUnsupportedPlatform},
		{name: "no SO_REUSEPORT, a Unix socket", share: true, network: "unix", reusePort: false, want: coreproc.CodeUnsupportedPlatform},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := &StreamGroup{name: "g", addrs: []corenet.AddressValue{{Network: c.network, Addr: "x"}}}
			if c.share {
				ShareAddress()(g)
			}
			err := shareRefusalFor(g, corenet.LoopOptionUnsupported, c.reusePort)
			if c.want == 0 {
				if err != nil {
					t.Fatalf("refusal = %v, want none", err)
				}
				return
			}
			if !errs.HasCode(err, c.want) {
				t.Fatalf("refusal = %v, want %v", err, c.want)
			}
		})
	}
}

// Test_mustProbe pins the probe's scope: an address several sockets share
// without ShareAddress, naming its port.
func Test_mustProbe(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		addr  string
		count int
		share bool
		want  bool
	}
	tests := []tc{
		{name: "shards on a named port", addr: "127.0.0.1:8080", count: 4, want: true},
		{name: "one socket", addr: "127.0.0.1:8080", count: 1},
		{name: "shared on purpose", addr: "127.0.0.1:8080", count: 4, share: true},
		{name: "port 0", addr: "[::1]:0", count: 4},
		{name: "no port", addr: "localhost", count: 4},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := mustProbe(corenet.AddressValue{Network: "tcp", Addr: c.addr}, c.count, c.share); got != c.want {
				t.Fatalf("mustProbe = %v, want %v", got, c.want)
			}
		})
	}
}

// Test_probeAddressLeavesAFreeAddressFree pins the probe's other half on every
// OS: a free stream address passes and is left free.
//
// It is deliberately NOT parallel. Between the close of the listener that
// found the port and the probe, and between the probe and the bind that checks
// the port is free again, the port is free, and the OS hands a free port to
// any socket bound to port 0 or connecting out: run beside the package's
// parallel tests, which dial and listen on ephemeral ports by the thousands,
// another socket of this process took it in that window (#1404, on Solaris),
// and the assertion measured the allocator, not the probe. A serial test runs
// while every parallel test of the binary is held at its t.Parallel, and the
// CI guests run one test binary at a time.
func Test_probeAddressLeavesAFreeAddressFree(t *testing.T) {
	type tc struct {
		name string
		// network is the stream network the free address is found on.
		network string
		// host is the address the free port is drawn on.
		host string
	}
	cases := []tc{{name: "tcp on the loopback", network: "tcp", host: "127.0.0.1:0"}}
	runCase := func(t *testing.T, c tc) {
		ln, err := stdnet.Listen(c.network, c.host)
		if err != nil {
			t.Fatal(err)
		}
		addr := corenet.AddressValue{Network: c.network, Addr: ln.Addr().String()}
		swallowErr(ln.Close())
		if err := probeAddress(t.Context(), addr, true); err != nil {
			t.Fatalf("probe of a free address = %v", err)
		}
		again, err := stdnet.Listen(c.network, addr.Addr)
		if err != nil {
			t.Fatalf("the probe left %s held: %v", addr.Addr, err)
		}
		swallowErr(again.Close())
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// Test_probeAddress pins the probe on every OS: a held address is
// AddressInUse naming it, a stream's and a datagram's alike, and an address
// that does not resolve is ListenFailed. A free one is
// Test_probeAddressLeavesAFreeAddressFree's.
func Test_probeAddress(t *testing.T) {
	t.Parallel()
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { swallowErr(ln.Close()) }()
		addr := corenet.AddressValue{Network: "tcp", Addr: ln.Addr().String()}
		if err := probeAddress(t.Context(), addr, true); !errs.HasCode(err, corenet.CodeAddressInUse) {
			t.Fatalf("probe of a held address = %v, want ADDRESS_IN_USE", err)
		}
	})
	t.Run("datagram", func(t *testing.T) {
		t.Parallel()
		pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { swallowErr(pc.Close()) }()
		addr := corenet.AddressValue{Network: "udp", Addr: pc.LocalAddr().String()}
		if err := probeAddress(t.Context(), addr, false); !errs.HasCode(err, corenet.CodeAddressInUse) {
			t.Fatalf("probe of a held address = %v, want ADDRESS_IN_USE", err)
		}
	})
	t.Run("an address that does not resolve", func(t *testing.T) {
		t.Parallel()
		err := probeAddress(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "256.0.0.1:80"}, true)
		if !errs.HasCode(err, corenet.CodeListenFailed) {
			t.Fatalf("probe = %v, want LISTEN_FAILED", err)
		}
	})
}
