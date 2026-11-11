package server

import (
	stdnet "net"
	"net/http"
	"strconv"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The kinds of key RateLimit may read.
const (
	// httpRateKeyNone is the zero HTTPRateKeyValue, refused.
	httpRateKeyNone uint8 = iota
	// httpRateKeyRemote keys on the peer address.
	httpRateKeyRemote
	// httpRateKeyHeader keys on a request field.
	httpRateKeyHeader
)

// HTTPRateKeyValue is what RateLimit keys a request on, built by RateKeyRemoteAddr or
// RateKeyHeader; the zero HTTPRateKeyValue is refused.
type HTTPRateKeyValue struct {
	// kind is the kind of key, zero for none.
	kind uint8
	// header is the field name, canonicalized.
	header string
}

// RateKeyRemoteAddr keys a request on its peer address, without the port. Behind a
// proxy that is the proxy's: then RateKeyHeader, on a field the proxy sets.
func RateKeyRemoteAddr() HTTPRateKeyValue {
	//: a key on the socket's peer.
	return HTTPRateKeyValue{kind: httpRateKeyRemote}
}

// RateKeyHeader keys a request on its first field name, canonicalized. A field is
// only as trustworthy as whatever sets it in front of this server.
func RateKeyHeader(name string) HTTPRateKeyValue {
	//: a key on a field.
	return HTTPRateKeyValue{kind: httpRateKeyHeader, header: http.CanonicalHeaderKey(name)}
}

// RateLimit returns the rate-limit middleware of a net/http group, the port the
// http1 chain's link calls: a request over its key's budget is answered 429 with
// Retry-After and Connection: close. key says what a request is keyed on:
// RateKeyRemoteAddr, or RateKeyHeader for a value a trusted proxy sets. A nil
// limiter or a zero key is refused with corenet.HTTP1Misconfigured.
func RateLimit(l corenet.HTTP1Limiter, key HTTPRateKeyValue) (corenet.Middleware[http.Handler], error) {
	//: a middleware that limits by nothing.
	if l == nil {
		//: refused.
		return nil, rateMisconfigured("limiter")
	}
	//: a key the middleware cannot read.
	if key.kind == httpRateKeyNone || (key.kind == httpRateKeyHeader && key.header == "") {
		//: refused.
		return nil, rateMisconfigured("key")
	}
	//: the limiter's clock: a monotonic count from here.
	epoch := clock.System.Now()
	//: the middleware wraps each handler it is given.
	return func(next http.Handler) http.Handler {
		//: read per request here: net/http has no batch to share a stamp over.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			now := clock.System.Now().Sub(epoch).Nanoseconds()
			ok, after := l.Allow(rateKeyOf(r, key), now)
			//: over budget: refused, and no handler runs.
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(after))
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusTooManyRequests)
				//: refused.
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// rateKeyOf reads the bytes key names off r.
func rateKeyOf(r *http.Request, key HTTPRateKeyValue) []byte {
	//: a field's value.
	if key.kind == httpRateKeyHeader {
		//: the value, absent or not.
		return []byte(r.Header.Get(key.header))
	}
	host, _, err := stdnet.SplitHostPort(r.RemoteAddr)
	//: a transport that gives no port (a unix socket, a test).
	if err != nil {
		//: the whole address.
		return []byte(r.RemoteAddr)
	}
	//: the address alone, so the same client keeps one bucket across connections.
	return []byte(host)
}

// rateMisconfigured names what the middleware refused.
func rateMisconfigured(what string) error {
	//: once, at construction.
	return errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("link", "RateLimit"), errs.String("option", what))
}
