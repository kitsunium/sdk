package server

import (
	"context"
	"net/http"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// bearerScheme is the Authorization scheme Bearer reads, compared
// case-insensitively.
const bearerScheme = "bearer"

// authKey is the context key Bearer stores a verified request's auth under.
type authKey struct{}

// Bearer returns the authenticating middleware of a net/http group, the port the
// http1 chain's Bearer link calls: a request must carry "Authorization: Bearer
// <token>" that v verifies, or is answered 401 with WWW-Authenticate: Bearer and
// goes no further. What v sets on the auth — the subject, the claims — reaches
// the handler through AuthOf. The scheme matches case-insensitively, the token is
// what follows its spaces. A route that needs no token is mounted outside the
// middleware. A nil v is refused with corenet.HTTP1Misconfigured.
func Bearer(v corenet.HTTP1BearerVerifier) (corenet.Middleware[http.Handler], error) {
	//: no verifier would authenticate nobody, or everybody.
	if v == nil {
		//: refused.
		return nil, bearerMisconfigured("verifier")
	}
	//: the middleware wraps each handler it is given.
	return func(next http.Handler) http.Handler {
		//: the verifier is shared; each request gets an auth of its own.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var auth corenet.HTTP1AuthValue
			tok, ok := bearerOf(r.Header.Get("Authorization"))
			//: no token, or one the verifier refuses: 401, and no further.
			if !ok || !v.VerifyBearer([]byte(tok), &auth) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				w.WriteHeader(http.StatusUnauthorized)
				//: refused.
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, auth)))
		})
	}, nil
}

// AuthOf returns the auth Bearer's verifier set on the request ctx belongs to,
// and false when no Bearer middleware authenticated it.
func AuthOf(ctx context.Context) (corenet.HTTP1AuthValue, bool) {
	auth, ok := ctx.Value(authKey{}).(corenet.HTTP1AuthValue)
	//: the value, or the zero one with false.
	return auth, ok
}

// bearerOf returns the token of an "Authorization: Bearer <token>" value, and
// false when the value names another scheme or no token.
func bearerOf(field string) (string, bool) {
	//: shorter than the scheme and its space: no token.
	if len(field) <= len(bearerScheme) || !strings.EqualFold(field[:len(bearerScheme)], bearerScheme) || field[len(bearerScheme)] != ' ' {
		//: not a bearer credential.
		return "", false
	}
	tok := strings.TrimLeft(field[len(bearerScheme):], " ")
	//: a token, unless only spaces followed the scheme.
	return tok, tok != ""
}

// bearerMisconfigured names what the middleware refused.
func bearerMisconfigured(what string) error {
	//: once, at construction.
	return errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("link", "Bearer"), errs.String("option", what))
}
