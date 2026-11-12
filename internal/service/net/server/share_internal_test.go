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

// Test_probeAddress pins the probe on every OS: a free address passes and is
// left free, a held one is AddressInUse naming it, a stream's and a
// datagram's alike.
func Test_probeAddress(t *testing.T) {
	t.Parallel()
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := corenet.AddressValue{Network: "tcp", Addr: ln.Addr().String()}
		if err := probeAddress(t.Context(), addr, true); !errs.HasCode(err, corenet.CodeAddressInUse) {
			t.Fatalf("probe of a held address = %v, want ADDRESS_IN_USE", err)
		}
		swallowErr(ln.Close())
		if err := probeAddress(t.Context(), addr, true); err != nil {
			t.Fatalf("probe of a free address = %v", err)
		}
		again, err := stdnet.Listen("tcp", addr.Addr)
		if err != nil {
			t.Fatalf("the probe left %s held: %v", addr.Addr, err)
		}
		swallowErr(again.Close())
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
