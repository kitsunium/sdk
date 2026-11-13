package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultMaxBodyBytes is the request-body bound a zero MaxBodyBytes selects:
// 1 MiB, the http1 profile's own (corenet.HTTP1DefaultBody) and the
// framework's.
const defaultMaxBodyBytes int64 = 1 << 20

// noBodyBound is the MaxBodyBytes value that states no bound.
const noBodyBound int64 = -1

// bodyTooLarge is the text of the 413 limitBody answers.
const bodyTooLarge string = "request body too large\n"

// httpBounds are the options that mean something only to the net/http
// adapter: the header phase's own deadline and the header size cap. Their zero
// value is "not set", and each keeps the behaviour the group had before the
// option existed.
type httpBounds struct {
	// readHeaderTimeout bounds the request line and the header fields; zero or
	// negative falls back to the group's read timeout.
	readHeaderTimeout time.Duration
	// maxHeaderBytes caps the request line and the header fields; zero or
	// negative leaves net/http's default.
	maxHeaderBytes int
	// maxBodyBytes bounds the request body, as the option was given: zero
	// selects defaultMaxBodyBytes and noBodyBound states none.
	maxBodyBytes int64
}

// bodyBytes resolves the request-body bound handed to the adapter: the
// default's 1 MiB for zero, the caller's n, or noBodyBound.
func (b httpBounds) bodyBytes() int64 {
	//: unset: the default bound, never an inert zero (ADR 0031).
	if b.maxBodyBytes == 0 {
		//: the default.
		return defaultMaxBodyBytes
	}
	//: the caller's bound, or noBodyBound.
	return b.maxBodyBytes
}

// valueRefusal judges the values of the options a group was given at Start: a
// MaxBodyBytes below -1 is neither a bound nor "none", and a Backlog below zero
// is neither a depth nor the default; each is refused naming the option rather
// than read as either (ADR 0031).
func (g *StreamGroup) valueRefusal() error {
	//: the option was given with a value outside its range.
	if g.kinds.has(corenet.OptionMaxBodyBytes) && g.http.maxBodyBytes < noBodyBound {
		//: refused by name.
		return g.valueInvalid(corenet.OptionMaxBodyBytes)
	}
	//: a negative depth is no depth.
	if g.kinds.has(corenet.OptionBacklog) && g.limits.Backlog < 0 {
		//: refused by name.
		return g.valueInvalid(corenet.OptionBacklog)
	}
	//: the PROXY protocol's value.
	if err := g.valueRefusalProxy(); err != nil {
		//: refused by name.
		return err
	}
	//: the buffers' sizes.
	return g.valueRefusalBuffers()
}

// valueRefusalProxy parses the group's ProxyProtocol, refusing a value outside
// its range, and a group with an address the trust cannot apply to: a Unix
// socket's peer has no address to believe.
func (g *StreamGroup) valueRefusalProxy() error {
	//: no such option.
	if !g.kinds.has(corenet.OptionProxyProtocol) {
		//: nothing to parse.
		return nil
	}
	policy, ok := parseProxyPolicy(g.proxyProtocol)
	//: an empty list, a range that does not parse, a negative bound.
	if !ok {
		//: refused by name.
		return g.valueInvalid(corenet.OptionProxyProtocol)
	}
	//: every address the group binds, the trust being by address.
	for _, addr := range g.addrs {
		//: a Unix socket's peer has no address.
		if !strings.HasPrefix(addr.Network, "tcp") {
			//: refused by name.
			return g.valueInvalid(corenet.OptionProxyProtocol)
		}
	}
	g.proxyPolicy = policy
	//: parsed.
	return nil
}

// valueInvalid is OptionValueInvalid naming the group and the option.
func (g *StreamGroup) valueInvalid(k corenet.OptionKind) error {
	return errs.Wrap(corenet.OptionValueInvalid, errs.WrapParams{},
		errs.String("group", g.name), errs.String("option", k.String()))
}

// reportedBodyBound is the request-body bound State reports for the group's
// listeners: the adapter's, and zero for a group net/http does not serve.
func (g *StreamGroup) reportedBodyBound() int64 {
	//: only the net/http adapter applies the bound.
	if g.httpAdapter == nil {
		//: nothing to report.
		return 0
	}
	//: the bound the adapter holds.
	return g.http.bodyBytes()
}

// limitBody bounds the request body next reads to n bytes: a Content-Length
// past n is answered 413 on the head, with Connection: close, before next
// runs and before a body byte is read; a body of unknown length is wrapped in
// http.MaxBytesReader, whose error next sees as *http.MaxBytesError; a request
// with no body is left as it is, at no allocation, and so is a CONNECT, whose
// body is a tunnel's stream rather than a request body. A bound below zero
// leaves the handler as it is.
func limitBody(next http.Handler, n int64) http.Handler {
	//: no bound, nothing to enforce.
	if n < 0 {
		//: the handler runs as the caller wrote it.
		return next
	}
	//: every request is judged on its way to the handler.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//: a CONNECT's body is a tunnel — an RFC 8441 WebSocket's frames on
		//: HTTP/2 —, not a request body: bounding its total would cut the
		//: tunnel once that many bytes crossed it (#967). The tunnel's own
		//: protocol bounds what it reads.
		if r.Method == http.MethodConnect {
			next.ServeHTTP(w, r)
			//: unbounded by this layer.
			return
		}
		//: the head already says the body is too large: nothing is read.
		if r.ContentLength > n {
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Length", strconv.Itoa(len(bodyTooLarge)))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			//: the refusal's text is a constant; a failed write only says the
			//: peer has gone.
			_, writeErr := w.Write([]byte(bodyTooLarge))
			swallowErr(writeErr)
			//: the handler never runs.
			return
		}
		//: a body of unknown length ends in the standard library's own error;
		//: a request with no body — net/http's NoBody — has nothing to bound,
		//: and is not given a reader to allocate (#966).
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
	})
}

// headerTimeout resolves the header phase's deadline: the option when it was
// set, the read timeout otherwise — which is what the adapter applied before
// the option existed.
func (b httpBounds) headerTimeout(read time.Duration) time.Duration {
	//: the caller bounded the header phase on its own.
	if b.readHeaderTimeout > 0 {
		//: the option wins.
		return b.readHeaderTimeout
	}
	//: unset: the read budget, as before.
	return read
}

// idleOrRead resolves http.Server.IdleTimeout: the idle budget when it was
// set, the read budget otherwise — the fallback net/http applied itself while
// the adapter still set ReadTimeout.
func idleOrRead(idle, read time.Duration) time.Duration {
	//: the caller bounded the wait between requests on its own.
	if idle > 0 {
		//: the option wins.
		return idle
	}
	//: unset: the read budget, as net/http did.
	return read
}

// headerBytes resolves the header cap handed to http.Server, where zero means
// net/http's own default.
func (b httpBounds) headerBytes() int {
	//: a positive cap is the caller's.
	if b.maxHeaderBytes > 0 {
		//: the option.
		return b.maxHeaderBytes
	}
	//: unset or non-positive: net/http's default applies.
	return 0
}
