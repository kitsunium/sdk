package server

import (
	"crypto/tls"
	"net/http"
	"slices"
	"time"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// protoHTTP1 is HTTP/1.1's ALPN identifier (RFC 7301).
	protoHTTP1 string = "http/1.1"
	// protoH2 is HTTP/2's ALPN identifier over TLS (RFC 9113 §3.2).
	protoH2 string = "h2"
)

// httpProtocols is what an HTTP group serves, computed once at Start from its
// Protocols option, its TLS identity and nothing else (ADR 0195): the ALPN
// list its listeners offer and the set net/http accepts. Both derive from the
// same list, so they cannot disagree.
type httpProtocols struct {
	// alpn is the ALPN list the group's TLS listeners offer, nil without TLS.
	alpn []string
	// accepted is http.Server.Protocols.
	accepted http.Protocols
}

// Protocols sets the HTTP versions an HTTP group serves, by ALPN identifier —
// "http/1.1" and "h2" — in the order its TLS listeners prefer them, unless the
// group's identity names NextProtos: the listeners then offer the identity's
// entries the list holds, in the identity's order (ADR 0195). Unset, a group
// with TLS serves both, h2 first, and a group without serves HTTP/1.1. It is
// judged at Start: an empty list, an empty, repeated or unknown identifier,
// "h2" without TLS on a group not given Plaintext, an identity
// whose NextProtos names a version the list excludes or none it includes, and
// any group that does not serve an http.Handler are refused with
// ProtocolsRefused. The http1 codec given TLS, which serves HTTP/1.1 alone,
// honours it when it names "http/1.1" alone and refuses any other list
// with ProtocolsRefused (ADR 0198); any other loop group refuses it with
// LoopOptionUnsupported unless its handler or a layer honours it
// (LoopOptionHonourer).
//
// A request that arrived over HTTP/2 has no http.Hijacker: a handler that
// hijacks its connection — a WebSocket upgrade written by hand, say — serves
// HTTP/1.1 clients only, and a TLS group that relies on it for every client
// says Protocols("http/1.1"), the behaviour it had before HTTP/2 was
// negotiated by default.
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
	//: the bounds the group was given, judged once for every engine.
	if err := g.http2Refusal(); err != nil {
		//: refused, the bound named.
		return err
	}
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
	//: the list itself; h2 in cleartext is prior knowledge, on a group declared
	//: Plaintext (ADR 0195 amended).
	if reason := checkProtocols(protos, tlsOn || g.kinds.has(corenet.OptionPlaintext)); reason != "" {
		//: refused by what is wrong with it.
		return g.protocolsErr(reason)
	}
	//: the versions, and the ALPN list a TLS group offers.
	return g.computeProtocols(protos, tlsOn)
}

// computeProtocols records what the group serves, from its checked list.
func (g *StreamGroup) computeProtocols(protos []string, tlsOn bool) error {
	var p httpProtocols
	p.accepted.SetHTTP1(slices.Contains(protos, protoHTTP1))
	p.accepted.SetHTTP2(slices.Contains(protos, protoH2))
	p.accepted.SetUnencryptedHTTP2(!tlsOn && slices.Contains(protos, protoH2))
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
		//: an empty identifier names no version, and "" would read as servable.
		if p == "" {
			//: refused.
			return "empty identifier"
		}
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

// HTTP2 sets the HTTP/2 bounds of a group serving HTTP, resolving ADR 0195's
// deferral: an HTTP group's net/http adapter and the http2 loop adapter serve
// the same value. A zero field is its default (corehttp2's), and a field out
// of its bound fails Start with corehttp2.LimitsRefused, its name in reason.
// A datagram group refuses it with PacketOptionUnsupported, a loop group
// whose handler does not honour it with LoopOptionUnsupported, any other
// group with ProtocolsRefused. State reports the bounds the listeners serve.
func HTTP2(v corehttp2.LimitsValue) GroupOption {
	//: applied in declaration order; judged at Start.
	return func(g *StreamGroup) {
		g.http2Limits = v
		g.kinds.add(corenet.OptionHTTP2)
	}
}

// http2Refusal judges the HTTP2 option: the bounds, on a group that can serve
// them. A group with no HTTP handler is refused here unless it is a loop
// group, whose handler answers for it.
func (g *StreamGroup) http2Refusal() error {
	//: not asked.
	if !g.kinds.has(corenet.OptionHTTP2) {
		//: nothing to judge.
		return nil
	}
	//: a handler of no HTTP at all.
	if g.httpAdapter == nil && g.loopHandler == nil && g.handler != nil {
		//: refused.
		return g.protocolsErr("handler")
	}
	_, err := g.http2Limits.Resolve()
	//: a bound out of its range, named by Resolve.
	if err != nil {
		//: refused, the group named.
		return errs.Wrap(err, errs.WrapParams{}, errs.String("group", g.name))
	}
	//: servable.
	return nil
}

// effectiveHTTP2 is the group's resolved bounds; http2Refusal has already
// refused a value that does not resolve.
func (g *StreamGroup) effectiveHTTP2() corehttp2.LimitsValue {
	//: the defaults when no option was given.
	l, err := g.http2Limits.Resolve()
	//: refused at Start, so unreachable: a server still serves the defaults.
	if err != nil {
		//: an unset value resolves.
		return corehttp2.LimitsValue{
			MaxStreams: corehttp2.DefaultMaxStreams, ReceivePerStream: corehttp2.DefaultReceivePerStream,
			ReceivePerConn: 4 * corehttp2.DefaultReceivePerStream, MaxFrame: corehttp2.DefaultMaxFrame,
			HeaderTable: corehttp2.DefaultHeaderTable, MaxHeaderList: corehttp2.DefaultMaxHeaderList,
		}
	}
	//: resolved.
	return l
}

// http2Bounds returns the resolved bounds for a State row, nil on a group
// serving no HTTP.
func (g *StreamGroup) http2Bounds() *corehttp2.LimitsValue {
	//: only HTTP groups and an HTTP/2 loop adapter hold bounds.
	if g.httpAdapter == nil && findHTTPCodec(g.loopChain) == nil {
		//: none.
		return nil
	}
	l, err := g.http2Limits.Resolve()
	//: a bad option fails Start; nothing to report past it.
	if err != nil {
		//: none.
		return nil
	}
	//: effective.
	return &l
}

// servesH2C reports whether the group's listeners serve HTTP/2 in cleartext.
func (g *StreamGroup) servesH2C() bool {
	//: an http.Handler group given no TLS whose list names h2.
	return g.httpAdapter != nil && g.identity.IsZero() && g.httpProtos.accepted.UnencryptedHTTP2()
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
// when it serves HTTP, never below TLS 1.2 (tlsFloor). A fresh value each
// call, so no listener shares one.
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
	//: the listener's own, at the floor.
	return tlsFloor(cfg)
}

// http2Config is the HTTP/2 bounds an HTTP group serves with: set, never
// left to net/http's defaults, so a release of the standard library cannot
// move them. silence is how long a connection may send no frame before the
// server PINGs it, and how long the PING's answer may take before the
// connection is closed: the one bound net/http's HTTP/2 server keeps while a
// stream is open, its idle timer stopped — a header block left unfinished
// behind an open stream is closed by it (ADR 0195 §D3). Zero sets none.
func http2Config(write corenet.DurationValue, silence time.Duration, l corehttp2.LimitsValue, count func(string)) *http.HTTP2Config {
	//: the same for every group; a write bound when the group has one.
	return &http.HTTP2Config{
		MaxConcurrentStreams:          l.MaxStreams,
		MaxReadFrameSize:              l.MaxFrame,
		MaxDecoderHeaderTableSize:     l.HeaderTable,
		MaxEncoderHeaderTableSize:     l.HeaderTable,
		MaxReceiveBufferPerStream:     l.ReceivePerStream,
		MaxReceiveBufferPerConnection: l.ReceivePerConn,
		WriteByteTimeout:              write.Duration(),
		SendPingTimeout:               silence,
		PingTimeout:                   silence,
		//: every error net/http's HTTP/2 counts reaches State (#966).
		CountError: count,
	}
}

// h2Silence is the group's HTTP/2 silence bound: the header phase's
// (ReadHeaderTimeout, else ReadTimeout), else IdleTimeout; zero when the
// group bounds none of them.
func h2Silence(b httpBounds, t corenet.TimeoutsValue) time.Duration {
	//: the header phase's bound, which a stalled header block is.
	if d := b.headerTimeout(t.Read.Duration()); d > 0 {
		//: it.
		return d
	}
	//: the wait between requests, or nothing.
	return t.Idle.Duration()
}
