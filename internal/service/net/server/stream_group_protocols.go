package server

import (
	"crypto/tls"
	"net/http"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// protoHTTP1 is HTTP/1.1's ALPN identifier (RFC 7301).
	protoHTTP1 string = "http/1.1"
	// protoH2 is HTTP/2's ALPN identifier over TLS (RFC 9113 §3.2).
	protoH2 string = "h2"
	// h2MaxConcurrentStreams is how many streams one HTTP/2 connection may have
	// open at once: net/http's own floor, set rather than left implicit.
	h2MaxConcurrentStreams int = 100
	// h2MaxReadFrameSize is the largest frame read, the protocol's minimum
	// (RFC 9113 §4.2): a peer cannot make one frame cost more.
	h2MaxReadFrameSize int = 16 << 10
	// h2HeaderTableSize bounds both HPACK tables, the protocol's default
	// (RFC 7541 §4.2).
	h2HeaderTableSize int = 4 << 10
	// h2ReceivePerStream is the flow-control window a stream's body may hold
	// unread: what a handler that never reads its body costs at most.
	h2ReceivePerStream int = 256 << 10
	// h2ReceivePerConn is the window of a whole connection, four streams'
	// worth, so one unread stream never stops the others.
	h2ReceivePerConn int = 4 * h2ReceivePerStream
)

// httpProtocols is what an HTTP group serves, computed once at Start from its
// Protocols option, its TLS identity and nothing else (ADR 0192): the ALPN
// list its listeners offer and the set net/http accepts. Both derive from the
// same list, so they cannot disagree.
type httpProtocols struct {
	// alpn is the ALPN list the group's TLS listeners offer, nil without TLS.
	alpn []string
	// accepted is http.Server.Protocols.
	accepted http.Protocols
}

// Protocols sets the HTTP versions an HTTP group serves, by ALPN identifier —
// "http/1.1" and "h2" — in the order its TLS listeners prefer them (ADR
// 0192). Unset, a group with TLS serves both, h2 first, and a group without
// serves HTTP/1.1. It is judged at Start: an empty list, a repeated or unknown
// identifier, "h2" without TLS (cleartext HTTP/2 is not served), an identity
// whose NextProtos names a version the list excludes or none it includes, and
// any group that does not serve an http.Handler are refused with
// ProtocolsRefused; a loop group refuses it with LoopOptionUnsupported.
func Protocols(protos ...string) GroupOption {
	kept := slices.Clone(protos)
	//: the option is applied by the constructor, in declaration order; it is
	//: judged at Start, once the handler and the identity are known.
	return func(g *StreamGroup) {
		g.protos = kept
		g.kinds.add(corenet.OptionProtocols)
	}
}

// protocolsRefusal computes the group's httpProtocols, once, and returns the
// refusal of a combination it cannot serve. A group that serves no
// http.Handler computes nothing; given Protocols it is refused.
func (g *StreamGroup) protocolsRefusal() error {
	set := g.kinds.has(corenet.OptionProtocols)
	//: the option means something only to net/http's adapter.
	if g.httpAdapter == nil {
		//: refused rather than dropped (R6); nothing to compute otherwise.
		if set {
			//: the option the group cannot apply.
			return g.protocolsErr("handler")
		}
		//: nothing asked.
		return nil
	}
	tlsOn := !g.identity.IsZero()
	protos := g.protos
	//: unset: both over TLS, h2 first, HTTP/1.1 alone in cleartext.
	if !set {
		protos = []string{protoHTTP1}
		//: TLS negotiates h2 by ALPN.
		if tlsOn {
			protos = []string{protoH2, protoHTTP1}
		}
	}
	//: the list itself.
	if reason := checkProtocols(protos, tlsOn); reason != "" {
		//: refused by what is wrong with it.
		return g.protocolsErr(reason)
	}
	var p httpProtocols
	p.accepted.SetHTTP1(slices.Contains(protos, protoHTTP1))
	p.accepted.SetHTTP2(slices.Contains(protos, protoH2))
	//: a cleartext group negotiates nothing.
	if tlsOn {
		alpn, reason := offered(g.identity.ServerConfig().NextProtos, protos)
		//: the identity names a version the group does not serve, or none it does.
		if reason != "" {
			//: refused, never narrowed in silence.
			return g.protocolsErr(reason)
		}
		p.alpn = alpn
	}
	g.httpProtos = p
	//: computed.
	return nil
}

// checkProtocols returns why protos cannot be served, or "" when it can.
func checkProtocols(protos []string, tlsOn bool) string {
	//: a group that serves no version serves nothing.
	if len(protos) == 0 {
		//: refused.
		return "empty"
	}
	//: each identifier once, and one of the two.
	for i, p := range protos {
		//: a version the group serves, named once.
		if (p != protoHTTP1 && p != protoH2) || slices.Contains(protos[:i], p) {
			//: refused, the identifier named.
			return p
		}
	}
	//: cleartext HTTP/2 (h2c) is not served.
	if !tlsOn && slices.Contains(protos, protoH2) {
		//: refused.
		return "h2 without TLS"
	}
	//: servable.
	return ""
}

// offered is the ALPN list a TLS group offers: protos when the identity
// names none, else the identity's NextProtos that protos holds, in the
// identity's order. An identity naming an HTTP version protos excludes, or
// nothing protos holds, returns the reason.
func offered(next, protos []string) ([]string, string) {
	//: no ALPN of its own: the group's list, in its order.
	if len(next) == 0 {
		//: offered as given.
		return slices.Clone(protos), ""
	}
	alpn := make([]string, 0, len(next))
	//: the identity's order is the operator's preference.
	for _, n := range next {
		//: an HTTP version the group does not serve would be negotiated, then refused.
		if (n == protoHTTP1 || n == protoH2) && !slices.Contains(protos, n) {
			//: refused, the identifier named.
			return nil, "NextProtos " + n
		}
		//: the versions both name.
		if slices.Contains(protos, n) {
			alpn = append(alpn, n)
		}
	}
	//: nothing in common: every handshake would fail.
	if len(alpn) == 0 {
		//: refused.
		return nil, "NextProtos"
	}
	//: the intersection.
	return alpn, ""
}

// protocolsErr wraps ProtocolsRefused with the group and the reason.
func (g *StreamGroup) protocolsErr(reason string) error {
	//: once, at Start.
	return errs.Wrap(corenet.ProtocolsRefused, errs.WrapParams{},
		errs.String("group", g.name), errs.String("reason", reason))
}

// serverTLS returns the TLS configuration of one of the group's listeners,
// nil without an identity: the identity's, offering the group's ALPN list
// when it serves HTTP. A fresh value each call, so no listener shares one.
func (g *StreamGroup) serverTLS() *tls.Config {
	//: plaintext.
	if g.identity.IsZero() {
		//: no TLS layer.
		return nil
	}
	cfg := g.identity.ServerConfig()
	//: an HTTP group offers what it computed; any other keeps the identity's.
	if g.httpAdapter != nil {
		cfg.NextProtos = slices.Clone(g.httpProtos.alpn)
	}
	//: the listener's own.
	return cfg
}

// http2Config is the HTTP/2 bounds every HTTP group serves with: set, never
// left to net/http's defaults, so a release of the standard library cannot
// move them.
func http2Config(write corenet.DurationValue) *http.HTTP2Config {
	//: the same for every group; a write bound when the group has one.
	return &http.HTTP2Config{
		MaxConcurrentStreams:          h2MaxConcurrentStreams,
		MaxReadFrameSize:              h2MaxReadFrameSize,
		MaxDecoderHeaderTableSize:     h2HeaderTableSize,
		MaxEncoderHeaderTableSize:     h2HeaderTableSize,
		MaxReceiveBufferPerStream:     h2ReceivePerStream,
		MaxReceiveBufferPerConnection: h2ReceivePerConn,
		WriteByteTimeout:              write.Duration(),
	}
}
