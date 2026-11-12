package server

import (
	"context"
	"crypto/tls"
	stdnet "net"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The ALPN lists a TLS codec group offers when Protocols names none.
var (
	// alpnHTTP1 is the one protocol the http1 codec's TLS path offers: the
	// profile is HTTP/1.1 only (ADR 0187 §D5).
	alpnHTTP1 = []string{"http/1.1"}
	// alpnBoth is what the h2 adapter's offers: h2 first, as an HTTP
	// group's (ADR 0195 §D1).
	alpnBoth = []string{"h2", "http/1.1"}
)

// codecUnwrapper is what the http1 adapter exposes (http1.LoopHandler.Unwrap):
// the HTTP1Handler it serves, so a loop group knows its handler speaks the
// codec and may take TLS on the portable backend.
type codecUnwrapper interface {
	// Unwrap returns the HTTP1Handler the adapter serves.
	Unwrap() corenet.HTTP1Handler
}

// tlsCodec reports whether the group is the http1 codec given TLS (ADR 0187
// §D5, ADR 0198): crypto/tls handshakes on its own goroutine, then the
// cleartext goes through the codec on the portable backend's lanes.
func (g *StreamGroup) tlsCodec() bool {
	_, codec := g.loopHandler.(codecUnwrapper)
	//: the codec, and TLS asked.
	return codec && g.kinds.has(corenet.OptionTLS)
}

// loopTLSRefusal judges a loop group given TLS: a raw loop handler is
// refused with LoopTLSUnsupported, as ever; the codec, by tlsCodecRefusal.
func (g *StreamGroup) loopTLSRefusal() error {
	//: no codec: no TLS on the loop.
	if !g.tlsCodec() {
		//: refused by name.
		return errs.Wrap(corenet.LoopTLSUnsupported, errs.WrapParams{}, errs.String("group", g.name))
	}
	//: the TLS path's own refusals.
	return g.tlsCodecRefusal()
}

// tlsCodecRefusal judges a TLS codec group (R6): an identity that names
// nothing, an explicit native backend — TLS runs on the portable backend
// alone — and a loop middleware are refused with LoopTLSUnsupported, the
// option named; nothing is served without TLS or elsewhere instead. A
// Protocols list naming a version the codec does not serve — h2 for the
// http1 adapter —, and an identity whose NextProtos names such a version or
// nothing the codec serves, are refused with ProtocolsRefused, as an HTTP
// group's would be (ADR 0195). The h2 adapter serves both (#964).
func (g *StreamGroup) tlsCodecRefusal() error {
	//: by the first contradiction.
	switch {
	case g.identity.IsZero():
		//: TLS with nothing to present.
		return g.tlsErr("TLS")
	case g.loopSet && g.loop.Backend != corenet.LoopBackendAuto && g.loop.Backend != corenet.LoopBackendPortable:
		//: a native backend has no TLS (D-N3).
		return g.tlsErr("Loop")
	case g.kinds.has(corenet.OptionLoopMiddleware):
		//: a loop-only option with TLS.
		return g.tlsErr(corenet.OptionLoopMiddleware.String())
	default:
		//: a Protocols list the codec cannot serve.
		if reason := g.codecProtocols(); reason != "" {
			//: refused, the reason named.
			return g.protocolsErr(reason)
		}
		//: the identity's ALPN against the versions the codec serves — the
		//: Protocols list's, which codecProtocols judged, or its default.
		alpn, reason := offered(g.identity.ServerConfig().NextProtos, g.codecVersions())
		//: a version the codec does not serve, or none it does.
		if reason != "" {
			//: refused, the reason named.
			return g.protocolsErr(reason)
		}
		//: computed once, offered by every listener.
		g.httpProtos.alpn = alpn
		//: servable.
		return nil
	}
}

// multiVersion reports whether the group's handler is an adapter serving more
// than HTTP/1.1 — the h2 adapter, a corenet.LoopHTTPCodec.
func (g *StreamGroup) multiVersion() bool {
	_, ok := g.loopHandler.(corenet.LoopHTTPCodec)
	//: the h2 adapter.
	return ok
}

// codecVersions is the versions a TLS codec group serves, in the order its
// ALPN offers them: the Protocols list when set, which codecProtocols judged;
// otherwise h2 then HTTP/1.1 for the h2 adapter, HTTP/1.1 for the http1 one.
func (g *StreamGroup) codecVersions() []string {
	//: as given.
	if g.kinds.has(corenet.OptionProtocols) {
		//: judged already.
		return g.protos
	}
	//: the h2 adapter's default, an HTTP group's.
	if g.multiVersion() {
		//: both, h2 first.
		return alpnBoth
	}
	//: the http1 codec's one version.
	return alpnHTTP1
}

// codecProtocols returns why the group's Protocols list cannot be served by
// the codec, or "" when it is unset or names what the codec serves: the list
// is judged as an HTTP group's with TLS (checkProtocols), then h2, which the
// http1 codec does not serve, is refused rather than dropped from it; the h2
// adapter serves it.
func (g *StreamGroup) codecProtocols() string {
	//: unset: HTTP/1.1, the codec's one version.
	if !g.kinds.has(corenet.OptionProtocols) {
		//: nothing to judge.
		return ""
	}
	//: the list itself: empty, repeated or unknown identifiers.
	if reason := checkProtocols(g.protos, true); reason != "" {
		//: refused by what is wrong with it.
		return reason
	}
	//: h2 named, alone or beside HTTP/1.1, to the http1 codec, which serves
	//: HTTP/1.1 alone.
	if !g.multiVersion() && slices.Contains(g.protos, protoH2) {
		//: refused, the identifier named.
		return protoH2
	}
	//: HTTP/1.1 alone.
	return ""
}

// tlsErr is LoopTLSUnsupported naming the group and option.
func (g *StreamGroup) tlsErr(option string) error {
	//: by name.
	return errs.Wrap(corenet.LoopTLSUnsupported, errs.WrapParams{},
		errs.String("group", g.name), errs.String("option", option))
}

// loopLayers wraps a TLS codec group's listeners in TLS, offering the
// group's ALPN list, never below TLS 1.2 (tlsFloor); any other loop group's
// are returned as they are.
func (g *StreamGroup) loopLayers(lns []stdnet.Listener) []stdnet.Listener {
	//: plaintext.
	if !g.tlsCodec() {
		//: as bound.
		return lns
	}
	out := make([]stdnet.Listener, len(lns))
	//: each one.
	for i, ln := range lns {
		out[i] = tls.NewListener(ln, tlsFloor(g.codecTLS()))
	}
	//: wrapped.
	return out
}

// loopBackend is the backend the group's code asks for: the portable one for
// a TLS codec group, the only one with TLS; the Loop option's otherwise.
func (g *StreamGroup) loopBackend() corenet.LoopBackend {
	//: TLS runs on the netpoller.
	if g.tlsCodec() {
		//: portable.
		return corenet.LoopBackendPortable
	}
	//: as asked.
	return g.loop.Backend
}

// tlsCodecHonours reports the kinds a TLS codec group honours beyond the
// loop's own: TLS, its handshake bound, and Protocols, which tlsCodecRefusal
// keeps to the versions the codec serves.
func tlsCodecHonours(k corenet.OptionKind) bool {
	//: the TLS path's three.
	return k == corenet.OptionTLS || k == corenet.OptionHandshakeTimeout || k == corenet.OptionProtocols
}

// codecTLS is the configuration of a TLS codec group's listeners: the
// identity's, offering the group's ALPN list — the identity's NextProtos and
// the Protocols list kept to what the codec serves at Start by
// tlsCodecRefusal — a fresh value each call; loopLayers floors it (tlsFloor).
func (g *StreamGroup) codecTLS() *tls.Config {
	cfg := g.identity.ServerConfig()
	cfg.NextProtos = g.httpProtos.alpn
	//: the listener's own.
	return cfg
}

// tlsFloor raises cfg's MinVersion to TLS 1.2 when it is lower, zero — the
// standard library's default — included, and returns cfg: no listener of the
// server negotiates a version RFC 8996 deprecates, whatever configuration it
// was built from. The identity already refuses such a floor at construction;
// this keeps the listener's own guarantee from resting on it.
func tlsFloor(cfg *tls.Config) *tls.Config {
	//: below TLS 1.2, or left to the library.
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	//: at the floor or above.
	return cfg
}

// handshakeBound is the group's HandshakeTimeout, or the domain's default.
func (g *StreamGroup) handshakeBound() time.Duration {
	//: set by the group.
	if d := g.timeouts.Handshake.Duration(); d > 0 {
		//: its own.
		return d
	}
	//: never unbounded.
	return defaultHandshakeTimeout
}

// handshake negotiates c, a TLS connection, within d, and reports whether it
// completed: a failed or stalled negotiation closes c.
func handshake(c stdnet.Conn, d time.Duration) bool {
	tc, ok := c.(*tls.Conn)
	//: not TLS: nothing to negotiate.
	if !ok {
		//: ready.
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	//: negotiated, or given up.
	if err := tc.HandshakeContext(ctx); err != nil {
		swallowErr(c.Close())
		//: closed.
		return false
	}
	//: ready for the codec.
	return true
}
