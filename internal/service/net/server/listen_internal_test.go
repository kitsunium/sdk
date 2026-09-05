// Package server — listener construction.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	stdnet "net"
	"runtime"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// addrTarget names how a case's address is produced.
type addrTarget int

const (
	// addrLiteral uses the case's own address verbatim.
	addrLiteral addrTarget = iota
	// addrTempSocket puts a Unix socket in the test's temporary directory.
	addrTempSocket
	// addrAlreadyBound holds a real port for the duration of the case, so the
	// bind under test meets one that is genuinely taken.
	addrAlreadyBound
)

// targetAddr produces the address a case binds.
func targetAddr(t *testing.T, target addrTarget, literal string) string {
	t.Helper()
	switch target {
	//: a socket path inside the test's own directory, cleaned up with it.
	case addrTempSocket:
		//: unique per test, so parallel cases cannot collide.
		return t.TempDir() + "/listen.sock"
	//: a port held open for the whole case.
	case addrAlreadyBound:
		held, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("holding an address: %v", err)
		}
		t.Cleanup(func() {
			if cerr := held.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		})
		//: the address is taken for as long as the case runs.
		return held.Addr().String()
	//: the case supplied the address itself.
	default:
		//: verbatim, including the deliberately unusable ones.
		return literal
	}
}

// testIdentity builds a self-signed server identity for the TLS cases.
func testIdentity(t *testing.T) corenet.IdentityValue {
	t.Helper()
	key, kerr := ecdsa.GenerateKey(elliptic.P256(), nil)
	if kerr != nil {
		t.Fatalf("generate key: %v", kerr)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, cerr := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if cerr != nil {
		t.Fatalf("create certificate: %v", cerr)
	}
	keyDER, merr := x509.MarshalECPrivateKey(key)
	if merr != nil {
		t.Fatalf("marshal key: %v", merr)
	}
	id, ierr := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		MinVersion: tls.VersionTLS12,
	})
	if ierr != nil {
		t.Fatalf("identity: %v", ierr)
	}
	return id
}

// Test_shardable pins WHICH families can carry several listeners on one address.
//
// A Unix socket cannot: SO_REUSEPORT is an IP-socket option and a second bind on
// the same path fails outright. That distinction is what lets resolveShards tell
// a request that never made sense from one the platform could not honour — and
// only the second is a degradation worth reporting.
func Test_shardable(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network is the socket family.
		network string
		// want is whether several listeners can share one address.
		want bool
	}
	tests := []tc{
		{name: "tcp", network: "tcp", want: true},
		{name: "tcp4", network: "tcp4", want: true},
		{name: "tcp6", network: "tcp6", want: true},
		{name: "udp", network: "udp", want: true},
		{name: "udp4", network: "udp4", want: true},
		{name: "udp6", network: "udp6", want: true},
		//: a second bind on a socket path fails outright.
		{name: "unix", network: "unix"},
		{name: "unixgram", network: "unixgram"},
		{name: "unixpacket", network: "unixpacket"},
		{name: "an invalid family", network: "carrier-pigeon"},
		{name: "no family at all", network: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := shardable(c.network); got != c.want {
			t.Fatalf("shardable(%q) = %v, want %v", c.network, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveShards pins the rule that an unavailable optimisation degrades
// LOUDLY. Every case that cannot deliver what was asked must collapse to one
// listener and say so, because a silent fallback is indistinguishable from a
// working one — which is the failure mode ListenerState.Degraded exists for.
//
// The mirror rule matters just as much: auto-sizing disappoints no expectation,
// so collapsing it to one listener is not a degradation. Reporting it as one
// would make every Unix listener in the fleet look broken.
func Test_resolveShards(t *testing.T) {
	t.Parallel()
	cores := runtime.GOMAXPROCS(0)
	type tc struct {
		// name describes the case.
		name string
		// requested is the caller's shard count; zero means auto.
		requested int
		// network is the socket family.
		network string
		// want is the resolved listener count.
		want int
		// degraded is whether the outcome must be reported as a fallback.
		degraded bool
	}
	tests := []tc{
		{name: "one is always achievable", requested: 1, network: "tcp", want: 1},
		{name: "one on a unix socket is achievable too", requested: 1, network: "unix", want: 1},
		{name: "zero auto-sizes to the core count", requested: 0, network: "tcp", want: cores},
		{name: "a negative count auto-sizes too", requested: -4, network: "tcp", want: cores},
		{name: "an explicit count is honoured on tcp", requested: 4, network: "tcp", want: 4},
		{name: "udp shards like tcp", requested: 4, network: "udp", want: 4},
		{
			name: "a unix socket cannot be shared and says so",
			//: SO_REUSEPORT is an IP-socket option; a second bind on a path fails.
			requested: 4, network: "unix", want: 1, degraded: true,
		},
		{
			name:      "unix auto-sizing collapses without complaining",
			requested: 0, network: "unix", want: 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		count, degraded, reason := resolveShards(c.requested, c.network)

		//: on a platform without the option every multi-shard request degrades,
		//: so the expectation is conditional rather than absolute.
		wantCount, wantDegraded := c.want, c.degraded
		if !reusePortSupported() && shardable(c.network) {
			wantCount, wantDegraded = 1, c.requested > 1
		}
		if count != wantCount {
			t.Fatalf("resolveShards(%d, %q) = %d shards, want %d", c.requested, c.network, count, wantCount)
		}
		if degraded != wantDegraded {
			t.Fatalf("degraded = %v, want %v (reason %q)", degraded, wantDegraded, reason)
		}
		//: the flag and the reason must agree, or State contradicts itself.
		if degraded != (reason != "") {
			t.Fatalf("degraded=%v but reason=%q — the report contradicts itself", degraded, reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_reusePortControl pins that the option is set BEFORE the bind.
//
// setsockopt(SO_REUSEPORT) after bind has no effect at all, which is precisely
// why this runs from net.ListenConfig's Control hook. The proof is behavioural
// and there is no cheaper one: two listeners bind the same address through the
// hook, and only a socket that genuinely carries the option lets the second
// succeed.
func Test_reusePortControl(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network is the family the shards bind.
		network string
		// addr is the address the first shard asks for.
		addr string
	}
	tests := []tc{
		{name: "an IPv4 port", network: "tcp", addr: "127.0.0.1:0"},
		{name: "an IPv4-only port", network: "tcp4", addr: "127.0.0.1:0"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := stdnet.ListenConfig{Control: reusePortControl}

		first, err := cfg.Listen(t.Context(), c.network, c.addr)
		if err != nil {
			t.Fatalf("the first bind through the control hook failed: %v", err)
		}
		defer func() {
			if cerr := first.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}()

		second, serr := cfg.Listen(t.Context(), c.network, first.Addr().String())

		//: on a platform with the option, a second listener on the same address
		//: is exactly what sharding needs; without it the refusal is the honest
		//: answer, and resolveShards is what turns it into a reported degradation.
		if !reusePortSupported() {
			if serr == nil {
				if cerr := second.Close(); cerr != nil {
					t.Errorf("close: %v", cerr)
				}
				t.Fatal("a second listener bound an address on a platform with no SO_REUSEPORT")
			}
			return
		}
		if serr != nil {
			t.Fatalf("the second shard could not bind %s: %v — the option is not "+
				"reaching the socket before bind", first.Addr(), serr)
		}
		if cerr := second.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_listen pins that an unusable address is refused BEFORE the OS is touched,
// and that the group's identity decides the listener's nature.
//
// Refusing early is what turns "bind: invalid argument" three frames down into a
// typed error naming the family or the address that was wrong — the difference
// between a startup failure an operator can act on and one they have to guess at.
func Test_listen(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network and addr are the address to bind.
		network string
		addr    string
		// target selects how the address under test is produced.
		target addrTarget
		// secured wraps the listener in TLS.
		secured bool
		// wantCode is the refusal, or zero when the bind must succeed.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a tcp address", network: "tcp", addr: "127.0.0.1:0", target: addrLiteral},
		{name: "an IPv4-only address", network: "tcp4", addr: "127.0.0.1:0"},
		{name: "a unix socket", network: "unix", target: addrTempSocket},
		{name: "a TLS listener", network: "tcp", addr: "127.0.0.1:0", secured: true},
		{
			//: refused before the OS is touched, so the error names the family.
			name: "an unserved family", network: "carrier-pigeon", addr: "nest",
			wantCode: corenet.CodeUnsupportedNetwork,
		},
		{name: "a datagram family on the stream engine", network: "udp", addr: "127.0.0.1:0", wantCode: corenet.CodeUnsupportedNetwork},
		{name: "no address at all", network: "tcp", addr: "", wantCode: corenet.CodeInvalidAddress},
		{name: "an address of only spaces", network: "tcp", addr: "   ", wantCode: corenet.CodeInvalidAddress},
		{
			//: the OS refuses this one, which is a different error from the two
			//: above and must stay distinguishable — a family the engine does not
			//: serve is a wiring mistake, an address already in use is an
			//: operational one, and an operator needs to know which.
			name: "an address already in use", network: "tcp", target: addrAlreadyBound,
			wantCode: corenet.CodeListenFailed,
		},
		{
			name: "an address that does not resolve", network: "tcp", addr: "256.0.0.1:0",
			wantCode: corenet.CodeListenFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr := targetAddr(t, c.target, c.addr)
		var id corenet.IdentityValue
		if c.secured {
			id = testIdentity(t)
		}

		ln, err := listen(t.Context(), corenet.AddressValue{Network: c.network, Addr: addr}, id, false)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("listen(%q, %q) = %v, want code %v", c.network, addr, err, c.wantCode)
			}
			//: a refused bind hands back nothing, or a caller checking only the
			//: value would serve on a listener that does not exist.
			if ln != nil {
				t.Error("listen returned a listener beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("listen(%q, %q) = %v, want nil", c.network, addr, err)
		}
		defer func() {
			if cerr := ln.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}()
		//: the kernel's own choice, which is what State must report.
		if ln.Addr() == nil || ln.Addr().String() == "" {
			t.Fatal("the listener reports no address")
		}
		//: an identity must produce a TLS listener, or every connection on the
		//: group would be served in plaintext under a name that says otherwise.
		_, isTLS := ln.(*stdnet.TCPListener)
		if c.secured && isTLS {
			t.Fatal("a group carrying an identity got a plaintext listener")
		}
		if !c.secured && c.network == "tcp" && !isTLS {
			t.Fatalf("a plaintext group got a %T", ln)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_boundListener_Close pins that closing is what unblocks the accept loop.
// There is no other signal that reliably interrupts a blocking Accept, so a
// Close that failed to propagate would leave a goroutine parked for the life of
// the process.
func Test_boundListener_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// closeTwice closes the listener a second time.
		closeTwice bool
	}
	tests := []tc{
		{name: "a live listener"},
		//: a concurrent Close may already have closed it during shutdown, which
		//: is why every caller discards the second error rather than reporting it.
		{name: "a listener already closed", closeTwice: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		raw, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		bound := &boundListener{
			group: "g",
			addr:  corenet.AddressValue{Network: "tcp", Addr: raw.Addr().String()},
			ln:    raw,
		}
		accepted := make(chan error, 1)
		go func() {
			_, aerr := raw.Accept()
			accepted <- aerr
		}()

		if cerr := bound.Close(); cerr != nil {
			t.Fatalf("Close = %v, want nil", cerr)
		}

		//: the parked Accept must come back, which is the entire point.
		select {
		case aerr := <-accepted:
			if aerr == nil {
				t.Fatal("Accept returned a connection from a closed listener")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Accept is still parked after Close — the accept loop would never end")
		}
		if !c.closeTwice {
			return
		}
		//: the second close fails, and every caller in this package discards it
		//: deliberately rather than reporting a socket that is already gone.
		if cerr := bound.Close(); cerr == nil {
			t.Error("closing an already-closed listener reported success")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
