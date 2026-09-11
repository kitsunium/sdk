// Package websocket — the RFC 6455 §4.2 opening handshake.
package websocket

import (
	"bufio"
	stdnet "net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// switchingProtocols is the status line every successful upgrade begins with.
// It is written by hand because by then the response belongs to us: net/http's
// ResponseWriter is out of the picture the moment the socket is hijacked.
const switchingProtocols string = "HTTP/1.1 101 Switching Protocols\r\n"

// crlf terminates every header line and, on its own, the header block.
const crlf string = "\r\n"

// nullOrigin is what a browser sends for a sandboxed iframe, a data: URL or a
// file:// page. It names an OPAQUE origin — one that is deliberately equal to
// no other — so it can never satisfy a same-origin rule.
const nullOrigin string = "null"

// httpsScheme is the only scheme an Origin may carry when this server
// terminated the request's TLS itself: the browser demonstrably connected over
// https, so a page it loaded over plain http is a different origin.
const httpsScheme string = "https"

// Upgrade completes the RFC 6455 opening handshake and returns the connection.
//
// On failure it has already written an HTTP response — a 426 carrying the
// version this server speaks, a 400, a 403 or a 405 as the fault requires — so
// the handler has nothing left to answer and should simply return.
//
// The socket is taken over from net/http, which means it is no longer the HTTP
// server's to close, nor this SDK's listener engine's. Both are told, and the
// connection is the handler's until it closes it.
func Upgrade(w http.ResponseWriter, r *http.Request, opts ...Option) (conn *Conn, err error) {
	cfg, cerr := resolve(opts)
	//: a refused option set never reaches the wire.
	if cerr != nil {
		//: the handler owes the client an answer, and this one is ours to give.
		http.Error(w, "websocket unavailable", http.StatusInternalServerError)
		//: the error already names the offending option.
		return nil, cerr
	}
	//: everything the RFC says about the REQUEST, before anything about this
	//: server's ability to serve it — a malformed handshake is the client's
	//: problem and deserves the more precise status.
	if verr := verifyHandshake(w, r, &cfg); verr != nil {
		//: the response has already been written by the verifier.
		return nil, verr
	}
	//: probed BEFORE the 101 is written, so a stack that cannot take the socket
	//: over answers with an error the client can read instead of a protocol
	//: switch onto a connection nobody owns.
	if !canHijack(w) {
		//: a wrapper that hides http.Hijacker, or HTTP/2.
		http.Error(w, "websocket unavailable", http.StatusInternalServerError)
		//: refuse now; nothing has been written that claims otherwise.
		return nil, errs.Wrap(corenet.WSUpgradeUnsupported, errs.WrapParams{},
			errs.String("why", "the ResponseWriter exposes no http.Hijacker"))
	}
	subprotocol, _ := chooseSubprotocol(cfg.subprotocols, r)
	socket, buffered, herr := hijack(w)
	//: the hijack itself failed, which the probe cannot rule out.
	if herr != nil {
		//: nothing was written, so an ordinary error response still works.
		http.Error(w, "websocket unavailable", http.StatusInternalServerError)
		//: report why the socket could not be taken over.
		return nil, herr
	}
	//: from here on the ResponseWriter is gone: writing to it would be a
	//: net/http error log and nothing on the wire. Every refusal below closes
	//: the socket instead, because that IS the answer now.
	if buffered.Buffered() > 0 {
		swallowErr(socket.Close())
		//: a client that pipelined frames onto the upgrade request has either
		//: broken the protocol or is trying to slip a second request past an
		//: intermediary that has not switched yet. Nothing here can tell which,
		//: and answering would make the guess for it.
		return nil, errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.Int("buffered", buffered.Buffered()),
			errs.String("why", "the client sent data before the handshake completed"))
	}
	//: written straight to the socket: the ResponseWriter is out of the picture
	//: from the hijack onwards, and its buffer has already been flushed.
	if werr := writeAccept(socket, r.Header.Get(corenet.WSKeyHeader), subprotocol); werr != nil {
		swallowErr(socket.Close())
		//: the client never saw a 101, so there is no connection to close
		//: politely — the socket is simply gone.
		return nil, werr
	}
	//: upgraded.
	return NewConn(socket, buffered, subprotocol, &cfg, corenet.DrainSignal(r.Context())), nil
}

// verifyHandshake applies RFC 6455 §4.2.1 to the request, writing the refusal
// itself so every caller answers the same way.
func verifyHandshake(w http.ResponseWriter, r *http.Request, cfg *config) error {
	//: §4.2.1/1 — the handshake is a GET. Anything else is not a handshake at
	//: all, and 405 is the answer HTTP already has for that.
	if r.Method != http.MethodGet {
		//: refuse.
		return refuse(w, http.StatusMethodNotAllowed, "method", r.Method,
			"the opening handshake must be a GET")
	}
	//: an upgrade replaces the protocol on the socket, which requires owning
	//: the socket. HTTP/2 multiplexes it, so there is nothing to take over;
	//: RFC 8441 defines a different mechanism entirely and is out of scope.
	if r.ProtoMajor != 1 || r.ProtoMinor < 1 {
		w.Header().Set(corenet.WSVersionHeader, corenet.WSVersion)
		http.Error(w, "websocket requires HTTP/1.1", http.StatusUpgradeRequired)
		//: refuse.
		return errs.Wrap(corenet.WSUpgradeUnsupported, errs.WrapParams{},
			errs.String("proto", r.Proto),
			errs.String("why", "the opening handshake requires HTTP/1.1; RFC 8441 is not implemented"))
	}
	//: §4.2.1/3-4 — both tokens, each possibly inside a comma-separated list
	//: and in any case, because a proxy is free to rewrite either.
	if !hasToken(r.Header.Values("Upgrade"), corenet.WSUpgradeToken) {
		//: refuse.
		return refuse(w, http.StatusBadRequest, "header", "Upgrade",
			"the request does not offer the websocket upgrade")
	}
	//: §4.2.1/4 — Connection must name the upgrade, or an intermediary would be
	//: free to strip the Upgrade header as hop-by-hop metadata.
	if !hasToken(r.Header.Values("Connection"), "upgrade") {
		//: refuse.
		return refuse(w, http.StatusBadRequest, "header", "Connection",
			"the Connection header does not carry the Upgrade token")
	}
	//: §4.4 — an unknown version is answered WITH the version this server
	//: speaks, which is what lets a client renegotiate instead of guessing.
	if version := r.Header.Get(corenet.WSVersionHeader); version != corenet.WSVersion {
		w.Header().Set(corenet.WSVersionHeader, corenet.WSVersion)
		http.Error(w, "unsupported websocket version", http.StatusUpgradeRequired)
		//: refuse.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", corenet.WSVersionHeader),
			errs.String("offered", version),
			errs.String("why", "this server speaks only version 13"))
	}
	//: §4.1 — the nonce is checked, not merely echoed through the digest: a key
	//: of the wrong length was not produced by a WebSocket client, and
	//: answering it with a 101 hands a socket to something that will never
	//: speak the protocol.
	if kerr := corenet.ValidateWSKey(r.Header.Get(corenet.WSKeyHeader)); kerr != nil {
		http.Error(w, "bad websocket handshake", http.StatusBadRequest)
		//: refuse.
		return kerr
	}
	//: last, because it is a policy question rather than a protocol one, and
	//: because 403 should mean "you are who you say and I still refuse".
	return verifyOrigin(w, r, cfg)
}

// verifyOrigin applies the connection's origin policy.
//
// The browser's same-origin policy does NOT apply to WebSocket: a page on any
// site may open a connection to this server, and the browser will attach the
// user's cookies to the handshake. The origin check is the only thing standing
// between that and cross-site WebSocket hijacking, so it is on by default and
// turned off by name.
func verifyOrigin(w http.ResponseWriter, r *http.Request, cfg *config) error {
	//: the caller has said, in as many words, that ambient credentials are not
	//: what authenticates this endpoint.
	if cfg.anyOrigin {
		//: no check to run.
		return nil
	}
	origin := r.Header.Get("Origin")
	//: no Origin header at all means no browser produced this request — a Go
	//: client, a CLI, a service. There is no ambient credential to abuse, so
	//: there is nothing for the check to protect.
	if origin == "" {
		//: nothing to compare.
		return nil
	}
	//: an explicit allowlist replaces the default rule entirely.
	if len(cfg.allowedOrigins) > 0 {
		//: compared on the WHOLE origin — scheme, host and port — because
		//: matching the host alone would accept http:// for an https server,
		//: which is precisely the downgrade this check exists to notice.
		if slicesContainsFold(cfg.allowedOrigins, origin) {
			//: allowed by name.
			return nil
		}
		//: refuse.
		return refuse(w, http.StatusForbidden, "origin", origin,
			"the origin is not in the allowlist")
	}
	//: a proxy conveyed a scheme this server cannot verify, so the default
	//: rule would compare host and port alone and accept the downgrade it
	//: exists to notice. The caller names the origins instead.
	if forwardedScheme(r) {
		//: refuse, naming what to configure.
		return refuse(w, http.StatusForbidden, "origin", origin,
			"a proxy forwarded this request, so the scheme the browser used is not visible here "+
				"and the default same-origin rule cannot see a downgrade; "+
				"use AllowOrigins to name the origins, or AllowAnyOrigin")
	}
	//: the default: same origin as the request itself, as far as this server
	//: can see the request's origin — see sameOrigin for where that stops.
	if sameOrigin(origin, r) {
		//: allowed.
		return nil
	}
	//: refuse.
	return refuse(w, http.StatusForbidden, "origin", origin,
		"the origin names another host, or plain http on a connection this server encrypted; "+
			"use AllowOrigins or AllowAnyOrigin to permit it")
}

// forwardedScheme reports that a proxy in front of this server announced the
// scheme the browser used, and that this server did not terminate TLS itself —
// so the announcement is the only source for the scheme, and it is one a client
// can write.
//
// Presence alone is read, never the value: a header a stranger wrote can then
// only make this check STRICTER, never looser. That is the whole reason it is
// safe to consult headers here at all. Where TLS ended in this process the
// scheme is known first-hand and the announcement is irrelevant.
func forwardedScheme(r *http.Request) bool {
	//: TLS ended here: the scheme is known, whatever a header claims.
	if r.TLS != nil {
		//: nothing forwarded that matters.
		return false
	}
	//: RFC 7239's header and the de-facto one every TLS-terminating proxy
	//: sets. Headers a FORWARD proxy adds on the client's side (Via,
	//: X-Forwarded-For) are deliberately not read: they say a request was
	//: relayed, not that a scheme was translated.
	for _, name := range []string{"Forwarded", "X-Forwarded-Proto"} {
		//: PRESENCE, which is not Header.Get: that returns "" both for a header
		//: nobody sent and for one sent empty, and a proxy that emits an empty
		//: value is still a proxy. Reading the map directly is the only way to
		//: tell the two apart, and the canonical key is what net/http stored it
		//: under.
		if _, sent := r.Header[textproto.CanonicalMIMEHeaderKey(name)]; sent {
			//: the scheme is announced rather than observed.
			return true
		}
	}
	//: no proxy announced a scheme.
	return false
}

// sameOrigin reports whether an Origin header names the request's own origin,
// as far as this server can see it.
//
// The host and port are always compared. The scheme is compared only when this
// server terminated TLS itself, because only then does it KNOW the browser used
// https — and a page loaded over plain http for the same host is exactly the
// downgrade an origin check exists to notice: anyone who can inject into that
// page gets an encrypted socket carrying the user's cookies.
//
// Behind a TLS-terminating proxy the request arrives in plaintext whatever the
// browser used, so the scheme is invisible here and is NOT compared: requiring
// the Origin's scheme to equal the request's would refuse every browser behind
// every proxy. [AllowOrigins] is what closes that gap, since it names the
// scheme outright. X-Forwarded-Proto is deliberately not consulted — where no
// proxy overwrites it, the client wrote it.
func sameOrigin(origin string, r *http.Request) bool {
	//: an opaque origin is equal to nothing, including itself.
	if strings.EqualFold(origin, nullOrigin) {
		//: never same-origin.
		return false
	}
	parsed, perr := url.Parse(origin)
	//: an Origin that is not a URL is not one this rule can reason about.
	if perr != nil || parsed.Host == "" {
		//: refuse rather than fall back to a looser comparison.
		return false
	}
	//: host and port together; DNS is case-insensitive, ports are not, but a
	//: port is digits either way so one fold comparison covers both. The
	//: scheme is a separate question with a separate answer.
	return strings.EqualFold(parsed.Host, r.Host) && schemeConsistent(parsed.Scheme, r)
}

// schemeConsistent reports whether an Origin's scheme agrees with what this
// server can see of the scheme the browser used.
func schemeConsistent(scheme string, r *http.Request) bool {
	//: net/http populates r.TLS only on a connection it received as a
	//: *tls.Conn, i.e. when TLS ended in THIS process. Without it the request
	//: may still have been https up to a proxy, and nothing here can tell.
	if r.TLS == nil {
		//: invisible, so not compared — AllowOrigins is what names it.
		return true
	}
	//: the browser demonstrably used https; the same host over another scheme
	//: is another origin. Schemes are case-insensitive (RFC 3986 §3.1).
	return strings.EqualFold(scheme, httpsScheme)
}

// slicesContainsFold reports whether list holds want, case-insensitively.
func slicesContainsFold(list []string, want string) bool {
	//: an allowlist is short by nature, so a scan beats any index.
	for _, entry := range list {
		//: the comparison is folded because a browser is free to send the
		//: scheme and host in whatever case the page used.
		if strings.EqualFold(entry, want) {
			//: listed.
			return true
		}
	}
	//: not listed.
	return false
}

// refuse writes the HTTP refusal and returns the matching typed error.
func refuse(w http.ResponseWriter, status int, key, value, why string) error {
	http.Error(w, http.StatusText(status), status)
	//: one shape for every handshake refusal, so a caller can branch on the
	//: field that names what was wrong.
	return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
		errs.String(key, value),
		errs.Int("status", status),
		errs.String("why", why))
}

// hasToken reports whether any of the header values carries the token.
//
// A header like "Connection: keep-alive, Upgrade" is one value holding two
// tokens, and net/http will also hand back several values for a header sent
// twice. Both shapes are legal and both are produced by real proxies, so both
// are scanned.
func hasToken(values []string, token string) bool {
	//: one header may appear several times.
	for _, value := range values {
		//: and each appearance may be a comma-separated list.
		for entry := range strings.SplitSeq(value, ",") {
			//: tokens are case-insensitive, and whitespace around them is not
			//: part of them.
			if strings.EqualFold(strings.TrimSpace(entry), token) {
				//: present.
				return true
			}
		}
	}
	//: absent.
	return false
}

// chooseSubprotocol picks the server's most preferred subprotocol the client
// also offered, or "" when there is no overlap.
//
// The SERVER's order decides. A client advertises what it can speak; choosing
// among those is the server's call, or a client that listed a deprecated
// dialect first could pin the server to it forever.
func chooseSubprotocol(preferred []string, r *http.Request) (chosen string, agreed bool) {
	//: a server that speaks no subprotocol never negotiates one.
	if len(preferred) == 0 {
		//: nothing to choose from.
		return "", false
	}
	offered := r.Header.Values(corenet.WSProtocolHeader)
	//: server preference order, so the first match wins.
	for _, want := range preferred {
		//: subprotocol names are registered tokens and are compared exactly;
		//: unlike the header tokens above, their case is part of the name.
		if hasExactToken(offered, want) {
			//: agreed.
			return want, true
		}
	}
	//: §4.2.2 — no agreement means no Sec-WebSocket-Protocol header, which the
	//: RFC names as the way to say "none". Failing the handshake instead would
	//: be stricter than the protocol and would break every client that
	//: advertises an optional dialect.
	return "", false
}

// hasExactToken reports whether any header value carries the token verbatim.
func hasExactToken(values []string, token string) bool {
	//: one header may appear several times.
	for _, value := range values {
		//: and each appearance may be a comma-separated list.
		for entry := range strings.SplitSeq(value, ",") {
			//: exact, because a subprotocol name is an identifier the two ends
			//: agreed on out of band.
			if strings.TrimSpace(entry) == token {
				//: offered.
				return true
			}
		}
	}
	//: not offered.
	return false
}

// canHijack reports whether the response's connection can be taken over,
// walking the same unwrap chain http.ResponseController does.
//
// It exists because there is no way to ASK http.ResponseController: calling
// Hijack to find out is the act itself, and it cannot be undone. A middleware
// that wraps the response to count bytes is the normal shape of an HTTP stack,
// so the chain walk is not optional.
func canHijack(w http.ResponseWriter) bool {
	//: walk the wrapper chain the same way the controller does.
	for {
		//: the type decides whether this link can hijack, or whether there is
		//: another link underneath it to look at.
		switch unwrapped := w.(type) {
		//: the link that owns the socket.
		case http.Hijacker:
			//: this link owns the socket, so the chain does.
			return true
		//: a wrapper that exposes what it wraps.
		case interface{ Unwrap() http.ResponseWriter }:
			//: keep looking underneath — the hijacker may be further down.
			w = unwrapped.Unwrap()
		//: an opaque writer with no hijacker and nothing beneath it.
		default:
			//: nothing in the chain can hand over the socket.
			return false
		}
	}
}

// hijack takes the socket over and returns it with the reader net/http was
// using.
//
// The reader is returned rather than discarded because net/http may have
// buffered bytes into it while parsing the request. Reading from the socket
// directly afterwards would silently skip them — which on a protocol whose
// every frame is length-prefixed means every subsequent frame is misparsed.
func hijack(w http.ResponseWriter) (socket stdnet.Conn, buffered *bufio.Reader, err error) {
	socket, rw, herr := http.NewResponseController(w).Hijack()
	//: the probe said the chain could hijack, but the act can still fail — a
	//: connection net/http has already given up on, for instance.
	if herr != nil {
		//: report why the socket could not be taken over.
		return nil, nil, errs.Wrap(corenet.WSUpgradeUnsupported, errs.WrapParams{},
			errs.String("cause", herr.Error()))
	}
	//: net/http has written nothing for us, but its buffer belongs to the
	//: response we are about to replace; flushing it now means the bytes we
	//: write next cannot end up behind anything it still held.
	swallowErr(rw.Flush())
	//: the socket and the reader that owns whatever it has already read. The
	//: caller inspects the reader before trusting the upgrade — see Upgrade.
	return socket, rw.Reader, nil
}

// writeAccept puts the 101 response on the socket.
func writeAccept(socket stdnet.Conn, key, subprotocol string) error {
	var response []byte
	response = append(response, switchingProtocols...)
	response = append(response, "Upgrade: "+corenet.WSUpgradeToken+crlf...)
	response = append(response, "Connection: Upgrade"+crlf...)
	//: §4.2.2 step 5 — the proof that this server parsed the handshake rather
	//: than replaying it, which is what stops a cached 101 from passing for a
	//: live upgrade.
	response = append(response, corenet.WSAcceptHeader+": "+corenet.WSAcceptKey(key)+crlf...)
	//: the single agreed subprotocol, echoed only when there is one. §4.2.2
	//: allows exactly one value here, never the client's whole list.
	if subprotocol != "" {
		response = append(response, corenet.WSProtocolHeader+": "+subprotocol+crlf...)
	}
	//: Sec-WebSocket-Extensions is deliberately absent. Omitting it is how a
	//: server says it negotiated NO extension — permessage-deflate included —
	//: and the frame reader enforces the same answer by refusing any reserved
	//: bit an extension would have used.
	response = append(response, crlf...)
	//: the socket is ours from here; a failed write means there was never a
	//: connection to have.
	if _, werr := socket.Write(response); werr != nil {
		//: report the failure with the same code the probe would have.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("cause", werr.Error()),
			errs.String("why", "the 101 response could not be written"))
	}
	//: switched.
	return nil
}
