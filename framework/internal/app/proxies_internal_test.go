package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// clientAddr believes a header only from a listed proxy, and then walks it
// from the right to the first hop outside the list (RFC 7239's model).
func TestClientAddrWalksTheChainFromTheRight(t *testing.T) {
	proxies, bad := parseTrustedProxies("10.0.0.0/8, 192.0.2.99, fd00::/8")
	if bad != "" {
		t.Fatalf("the list was refused at %q", bad)
	}
	for _, c := range []struct {
		name, peer string
		forwarded  []string
		want       string
	}{
		{"an untrusted peer is the client", "192.0.2.7:4000", []string{"6.6.6.6"}, "192.0.2.7"},
		{"a trusted peer's single hop", "10.1.2.3:4000", []string{"6.6.6.6"}, "6.6.6.6"},
		{"a proxy behind the proxy is walked past", "10.1.2.4:4000", []string{"203.0.113.9, 10.1.2.3"}, "203.0.113.9"},
		{"a forged first hop is never reached", "10.1.2.4:4000", []string{"203.0.113.9, 198.51.100.1"}, "198.51.100.1"},
		{"the hops of several lines are one chain", "10.1.2.4:4000", []string{"6.6.6.6", "203.0.113.9, 10.9.9.9"}, "203.0.113.9"},
		{"a listed address stands for itself alone", "192.0.2.99:4000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"its neighbour is not listed", "192.0.2.98:4000", []string{"203.0.113.9"}, "192.0.2.98"},
		{"every hop is a proxy: the farthest it vouches for", "10.1.2.4:4000", []string{"10.0.0.5, 10.1.2.3"}, "10.0.0.5"},
		{"a hop that is no address ends the walk", "10.1.2.4:4000", []string{"6.6.6.6, not-an-ip, 10.1.2.3"}, "10.1.2.3"},
		{"no header: the peer", "10.1.2.4:4000", nil, "10.1.2.4"},
		{"an empty header: the peer", "10.1.2.4:4000", []string{""}, "10.1.2.4"},
		{"an IPv4-mapped hop is the IPv4 address", "10.1.2.4:4000", []string{"::ffff:198.51.100.4"}, "198.51.100.4"},
		{"an IPv6 proxy", "[fd00::1]:4000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"an IPv4-mapped peer is read as IPv4", "[::ffff:10.1.2.3]:4000", []string{"6.6.6.6"}, "6.6.6.6"},
		{"a zone is no part of the peer's identity", "[fd00::1%eth0]:4000", []string{"2001:db8::7"}, "2001:db8::7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.peer
			for _, v := range c.forwarded {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientAddr(r, proxies); got != c.want {
				t.Errorf("clientAddr = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("no list trusts nobody", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.1.2.3:4000"
		r.Header.Set("X-Forwarded-For", "6.6.6.6")
		if got := clientAddr(r, nil); got != "10.1.2.3" {
			t.Errorf("clientAddr = %q, want the peer", got)
		}
	})
	t.Run("a peer that is no address is itself", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "@"
		if got := clientAddr(r, proxies); got != "@" {
			t.Errorf("clientAddr = %q, want the peer as written", got)
		}
	})
}

func TestTheProxyListIsReadWholeOrRefusedAtItsFirstBadEntry(t *testing.T) {
	for _, c := range []struct {
		list string
		want string
		bad  string
	}{
		{"10.0.0.0/8", "10.0.0.0/8", ""},
		{" 10.1.2.3 , ,fd00::/8,", "10.1.2.3/32,fd00::/8", ""},
		{"10.9.9.9/8", "10.0.0.0/8", ""},
		{"::ffff:10.1.2.3", "10.1.2.3/32", ""},
		{"::ffff:10.0.0.0/104", "10.0.0.0/8", ""},
		{"", "", ""},
		{"10.0.0.0/8, nonsense, 10.1.2.3", "", "nonsense"},
		{"10.0.0.0/33", "", "10.0.0.0/33"},
		{"::ffff:10.0.0.0/64", "", "::ffff:10.0.0.0/64"},
	} {
		got, bad := parseTrustedProxies(c.list)
		if got.String() != c.want || bad != c.bad {
			t.Errorf("parseTrustedProxies(%q) = %q, bad %q; want %q, bad %q", c.list, got.String(), bad, c.want, c.bad)
		}
	}
}
