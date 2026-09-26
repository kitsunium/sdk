// Package health — the two halves of Ask no external test can see: the host
// it dials for a listen address, and the posture of the client it dials with.
package health

import (
	"errors"
	"net/http"
	"testing"
)

// TestLoopbackForEveryUnspecifiedSpelling pins the listen-to-dial mapping over
// every spelling of "no particular address", in both families, and pins that
// every other host — an address, a zoned address, a name — is dialled as
// written.
func TestLoopbackForEveryUnspecifiedSpelling(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		host string
		want string
	}
	tests := []tc{
		{"empty", "", "127.0.0.1"},
		{"IPv4 unspecified", "0.0.0.0", "127.0.0.1"},
		{"IPv4-mapped unspecified", "::ffff:0.0.0.0", "127.0.0.1"},
		{"IPv6 unspecified", "::", "::1"},
		{"IPv6 unspecified, long form", "0:0:0:0:0:0:0:0", "::1"},
		{"IPv6 unspecified with a zone", "::%eth0", "::1"},
		{"IPv4 loopback", "127.0.0.1", "127.0.0.1"},
		{"another IPv4 address", "10.1.2.3", "10.1.2.3"},
		{"IPv6 loopback", "::1", "::1"},
		{"a zoned link-local address keeps its zone", "fe80::1%eth0", "fe80::1%eth0"},
		{"a name", "localhost", "localhost"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the host to dial.
		if got := loopbackFor(c.host); got != c.want {
			t.Errorf("loopbackFor(%q) = %q, want %q", c.host, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDialTargetJoinsWhatItMapped pins that the port survives the mapping and
// that an IPv6 host is bracketed again.
func TestDialTargetJoinsWhatItMapped(t *testing.T) {
	t.Parallel()
	type tc struct {
		listen string
		want   string
	}
	tests := []tc{
		{":4000", "127.0.0.1:4000"},
		{"0.0.0.0:4000", "127.0.0.1:4000"},
		{"[::]:4000", "[::1]:4000"},
		{"127.0.0.1:4000", "127.0.0.1:4000"},
		{"localhost:4000", "localhost:4000"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := dialTarget(c.listen)
		//: a usable listen address.
		if err != nil || got != c.want {
			t.Errorf("dialTarget(%q) = %q, %v; want %q", c.listen, got, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.listen, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAskClientNeverProxiesAndKeepsNothing pins the client's posture, which no
// loopback test can show: net/http never proxies a loopback address anyway,
// so only the transport itself says whether a named host would go through
// HTTP_PROXY. DefaultClient would, would keep the connection, and would
// follow a redirect.
func TestAskClientNeverProxiesAndKeepsNothing(t *testing.T) {
	t.Parallel()
	client := askClient()
	transport, isTransport := client.Transport.(*http.Transport)
	//: a transport of its own, never the shared default.
	if !isTransport || transport == http.DefaultTransport {
		t.Fatalf("the client's transport is %T, want a fresh *http.Transport", client.Transport)
	}
	//: no proxy, whatever the environment says.
	if transport.Proxy != nil {
		t.Error("the transport consults a proxy")
	}
	//: no connection kept once the Ask returns.
	if !transport.DisableKeepAlives {
		t.Error("the transport keeps connections alive")
	}
	//: a header bounded far under net/http's default.
	if transport.MaxResponseHeaderBytes != maxAskHeaderBytes {
		t.Errorf("header bound = %d, want %d", transport.MaxResponseHeaderBytes, maxAskHeaderBytes)
	}
	//: a redirect is returned, not followed.
	if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Error("the client follows redirects")
	}
}
