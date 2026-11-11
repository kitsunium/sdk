package app

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// mappedPrefixBits is the length of the ::ffff:0:0/96 prefix that makes an
// IPv6 address an IPv4 one.
const mappedPrefixBits int = 96

// trustedProxies are the networks <PRODUCT>_TRUSTED_PROXIES lists: the peers
// whose X-Forwarded-For the product believes. A peer outside them is the
// client, whatever header it carries.
type trustedProxies []netip.Prefix

// String is the list as the settings show it: the networks, comma-separated.
func (t trustedProxies) String() string {
	nets := make([]string, len(t))
	for i, p := range t {
		nets[i] = p.String()
	}
	return strings.Join(nets, ",")
}

// contains reports whether ip is one of the listed proxies. A zone and an
// IPv4-mapped form are not part of an address's identity here, as they are
// not in a list written as 10.0.0.0/8.
func (t trustedProxies) contains(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, p := range t {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies reads the comma-separated list of CIDRs and addresses an
// operator wrote, a bare address standing for itself alone. An empty entry
// is skipped, so a trailing comma is no mistake; the first one that is
// neither an address nor a network is returned as bad.
func parseTrustedProxies(list string) (proxies trustedProxies, bad string) {
	for entry := range strings.SplitSeq(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		p, ok := parseProxyEntry(entry)
		if !ok {
			return nil, entry
		}
		proxies = append(proxies, p)
	}
	return proxies, ""
}

// parseProxyEntry is one entry of the list: a network or an address. An
// IPv4-mapped IPv6 network is the IPv4 one it names, since the peers it is
// compared with are read unmapped.
func parseProxyEntry(entry string) (netip.Prefix, bool) {
	if !strings.Contains(entry, "/") {
		a, err := netip.ParseAddr(entry)
		if err != nil {
			return netip.Prefix{}, false
		}
		a = a.Unmap().WithZone("")
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	p, err := netip.ParsePrefix(entry)
	if err != nil {
		return netip.Prefix{}, false
	}
	if a := p.Addr(); a.Is4In6() {
		if p.Bits() < mappedPrefixBits {
			return netip.Prefix{}, false
		}
		return netip.PrefixFrom(a.Unmap(), p.Bits()-mappedPrefixBits), true
	}
	return p.Masked(), true
}

// resolveTrust is the proxy trust <PRODUCT>_TRUST_PROXY and
// <PRODUCT>_TRUSTED_PROXIES give: the proxies, empty while the switch is
// off — a list alone trusts nobody —, or the refusal of a start that turns
// the switch on with no proxy to believe or a list it cannot read. A product
// reached by its clients as well as through its proxy could not tell them
// apart, and the header it believed would be the client's own.
func resolveTrust(name string, own func(stem string) string) (trustedProxies, error) {
	if own(varTrustProxy) != "on" {
		return nil, nil
	}
	variable := productVariable(name, varTrustedProxies)
	proxies, bad := parseTrustedProxies(own(varTrustedProxies))
	switch {
	case bad != "":
		return nil, failure(CodeAppConfig, "TRUSTED_PROXIES_INVALID",
			variable+" is a comma-separated list of addresses and networks, such as 10.0.0.0/8, 192.0.2.7", nil, errs.String("variable", variable), errs.String("entry", bad))
	case len(proxies) == 0:
		return nil, failure(CodeAppConfig, "TRUSTED_PROXIES_REQUIRED",
			productVariable(name, varTrustProxy)+" is on: "+variable+" must list the proxies whose X-Forwarded-For is believed", nil, errs.String("variable", variable))
	}
	return proxies, nil
}

// clientAddr is the client of r. A peer that is not one of the trusted
// proxies is the client, and its X-Forwarded-For is ignored. A trusted
// peer's header is walked from its right — the hop each proxy appended — to
// the first address outside the proxies, which is the client (RFC 7239's
// model): the hops before it came from the client and may be forged. A hop
// that is not an address ends the walk, at the last address it vouched for.
func clientAddr(r *http.Request, trusted trustedProxies) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	client := peer.Unmap()
	if len(trusted) == 0 || !trusted.contains(peer) {
		return client.String()
	}
	for _, text := range slices.Backward(forwardedHops(r)) {
		hop, err := netip.ParseAddr(text)
		if err != nil {
			break
		}
		client = hop.Unmap()
		if !trusted.contains(hop) {
			break
		}
	}
	return client.String()
}

// forwardedHops are the hops of every X-Forwarded-For line of r, left to
// right, trimmed; an empty one names no hop.
func forwardedHops(r *http.Request) []string {
	var hops []string
	for text := range strings.SplitSeq(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",") {
		if text = strings.TrimSpace(text); text != "" {
			hops = append(hops, text)
		}
	}
	return hops
}
