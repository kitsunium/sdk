package server

import (
	"net/http"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// CORS returns the cross-origin middleware of a net/http group: a preflight —
// OPTIONS with Origin and Access-Control-Request-Method — is answered 204 with the
// policy's fields and goes no further, and every other request gets the policy's
// fields and reaches next. An origin compared byte for byte and not listed
// receives no CORS field and is still served, the browser being what enforces it.
// A value the policy refuses is corenet.CORSMisconfigured.
func CORS(cfg corenet.CORSValue) (corenet.Middleware[http.Handler], error) {
	policy, err := corenet.NewCORSPolicy(cfg)
	//: an origin, method, field or age it cannot honour: refused here, not per request.
	if err != nil {
		//: as the policy refused it.
		return nil, err
	}
	//: the middleware wraps each handler it is given.
	return func(next http.Handler) http.Handler {
		//: the policy is shared, read only.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			method := r.Header.Get("Access-Control-Request-Method")
			//: a preflight is answered here: nothing behind it has its credentials.
			if origin != "" && method != "" && r.Method == http.MethodOptions {
				setCORSFields(w.Header(), policy.Preflight([]byte(origin), []byte(method)))
				w.WriteHeader(http.StatusNoContent)
				//: answered.
				return
			}
			setCORSFields(w.Header(), policy.Actual([]byte(origin)))
			next.ServeHTTP(w, r)
		})
	}, nil
}

// setCORSFields adds an answer's fields to h, keeping a Vary the handler set.
func setCORSFields(h http.Header, a corenet.CORSAnswerValue) {
	//: each field, by name.
	for _, f := range a.Fields {
		h.Add(f.Name, f.Value)
	}
}
