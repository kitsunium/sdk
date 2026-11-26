package server

import (
	"crypto/tls"
	"slices"
)

// alpnRefusal is the ALPN list a refused handshake is answered with: the
// empty name, which no ClientHello can carry — RFC 7301 §3.1 sizes a
// ProtocolName 1 to 255 bytes, and crypto/tls refuses an empty one when it
// parses the hello — so it never matches what a client offers, and it is not
// h2, the one name crypto/tls makes an exception for. crypto/tls then
// answers no_application_protocol, as RFC 7301 §3.2 requires.
var alpnRefusal = []string{""}

// alpnStrict makes cfg refuse, with no_application_protocol, a client whose
// ALPN offer names nothing cfg.NextProtos holds, when the list holds h2
// without http/1.1 (#964). That is the one case crypto/tls lets through: a
// client offering http/1.1 to a server offering h2 completes the handshake
// with no protocol (Go issue 46310, crypto/tls's negotiateALPN), which RFC
// 7301 §3.2 does not allow. Any other list, a client that offers no ALPN at
// all — RFC 7301 leaves it unnegotiated — and one that shares a name with
// the list keep the standard library's own negotiation, with nothing
// allocated per handshake. cfg is the listener's own (serverTLS, codecTLS);
// it is returned.
func alpnStrict(cfg *tls.Config) *tls.Config {
	offered := cfg.NextProtos
	//: the exception needs h2 offered and http/1.1 not.
	if !slices.Contains(offered, protoH2) || slices.Contains(offered, protoHTTP1) {
		//: crypto/tls refuses every mismatch itself.
		return cfg
	}
	refuse := cfg.Clone()
	refuse.NextProtos = alpnRefusal
	cfg.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		//: no offer, or a shared name: the listener's own configuration.
		if len(hello.SupportedProtos) == 0 || sharesProtocol(offered, hello.SupportedProtos) {
			//: unchanged.
			return nil, nil
		}
		//: nothing in common: refused at the handshake.
		return refuse, nil
	}
	//: the listener's, refusing past the exception.
	return cfg
}

// sharesProtocol reports whether the client's offer names a protocol the
// listener offers.
func sharesProtocol(offered, client []string) bool {
	//: each the client offers.
	for _, p := range client {
		//: one in common.
		if slices.Contains(offered, p) {
			//: negotiable.
			return true
		}
	}
	//: none.
	return false
}
