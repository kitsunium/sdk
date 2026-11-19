package server

import (
	stdnet "net"
	"net/http"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// LoopbackHost returns the middleware of a net/http group that serves only on
// the loopback: a request whose Host does not name it — localhost, an address
// of 127.0.0.0/8 or ::1, with or without a port — is answered 403 and goes no
// further. A listener bound to 127.0.0.1 is still reached by a browser through a
// name an attacker's DNS points there (DNS rebinding), and that request carries
// the attacker's name as its Host; this is the check that refuses it.
func LoopbackHost() corenet.Middleware[http.Handler] {
	//: the middleware wraps each handler it is given.
	return func(next http.Handler) http.Handler {
		//: nothing is shared but next.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			//: a name or an address outside the loopback: refused.
			if !isLoopbackHost(r.Host) {
				w.WriteHeader(http.StatusForbidden)
				//: refused.
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isLoopbackHost reports whether a Host field names the loopback.
func isLoopbackHost(host string) bool {
	//: a port, when there is one, is not part of the name.
	if h, _, err := stdnet.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	//: the one name the loopback has everywhere.
	if strings.EqualFold(host, "localhost") {
		//: loopback.
		return true
	}
	ip := stdnet.ParseIP(host)
	//: an address, and one of the loopback's.
	return ip != nil && ip.IsLoopback()
}
