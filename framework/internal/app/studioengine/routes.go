package studioengine

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The Studio's routes (ADR 0180 §4, step 1): what the Studio reads about the
// running product, in dev — the graph, live events, traces, source,
// instances, items and the dev tools' reads. The Studio itself — its pages,
// its assets — is the kit tool's (ADR 0147 §1), which reads these routes;
// the framework links no user interface. Every route checks where a request
// comes from and its Host header, so a page on another site cannot read the
// product's source or live traffic through DNS rebinding. None of them acts
// on the product (D13): no mock, no fault, no run, no fire, no dispatch, no
// ask, no wake, no CPU capture. kit's core mounts them only in dev, once the
// app serves, and reserves /_kit/ itself otherwise.

// studioCSP is the content security policy of the Studio. It shares the
// product's origin and shows attacker-controllable data — titles, errors,
// identifiers — so it runs nothing that is not its own bundle.
const studioCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// tracesShown is how many of the latest traces the trace list gives.
const tracesShown int = 100

// methods are the methods a route of the Studio's prefix answers, HEAD
// aside: every one kit routes.
var methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// studio is the Studio's routes over one running app.
type studio struct {
	host plug.StudioHost
}

// mount registers the Studio's routes on mux, each behind guard.
func mount(mux *http.ServeMux, host plug.StudioHost) {
	s := studio{host: host}
	api := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.guard(h))
	}
	api("GET /_kit/api/graph", func(w http.ResponseWriter, r *http.Request) {
		answer(s, w, r, s.host.Graph(), nil)
	})
	api("GET /_kit/api/events", serveEvents(host))
	api("GET /_kit/api/traces", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		answer(s, w, r, s.host.Traces(tracesShown, q.Get("root"), q.Get("node")), nil)
	})
	api("GET /_kit/api/traces/{id}", s.serveTrace)
	api("GET /_kit/api/source", s.serveSource)
	api("GET /_kit/api/instances", s.serveInstances)
	api("GET /_kit/api/items", s.serveItems)
	s.mountDevTools(api)
	s.mountNotFound(api, "/_kit/api/", "no such Studio API")
	s.mountNotFound(api, "/_kit/", "the Studio is the kit tool's, not the product's: run kit dev")
}

// mountNotFound answers every method but HEAD under prefix with a not found
// that says message.
func (s studio) mountNotFound(api func(pattern string, h http.HandlerFunc), prefix, message string) {
	for _, method := range methods {
		api(method+" "+prefix, func(w http.ResponseWriter, r *http.Request) {
			s.host.Reply(w, r, nil, s.host.NotFound(message))
		})
	}
}

// serveTrace answers GET /_kit/api/traces/{id}: one trace, while the app
// keeps it.
func (s studio) serveTrace(w http.ResponseWriter, r *http.Request) {
	t, ok := s.host.Trace(r.PathValue("id"))
	if !ok {
		s.host.Reply(w, r, nil, s.host.NotFound("no such trace, or it was evicted"))
		return
	}
	answer(s, w, r, t, nil)
}

// serveInstances answers GET /_kit/api/instances?workflow=: a workflow's
// entities and where each is.
func (s studio) serveInstances(w http.ResponseWriter, r *http.Request) {
	list, ok := s.host.Instances(r.URL.Query().Get("workflow"))
	if !ok {
		s.host.Reply(w, r, nil, s.host.NotFound("no such workflow"))
		return
	}
	answer(s, w, r, list, nil)
}

// serveItems answers GET /_kit/api/items?store=: a store's latest entities,
// through the same redaction as payloads: a store of accounts holds password
// hashes and token digests, and personal data (ADR 0006), which the Studio
// has no reason to show.
func (s studio) serveItems(w http.ResponseWriter, r *http.Request) {
	items, ok := s.host.Items(r.URL.Query().Get("store"))
	if !ok {
		s.host.Reply(w, r, nil, s.host.NotFound("no such store"))
		return
	}
	answer(s, w, r, items, nil)
}

// guard refuses a request that does not come from this machine, or whose
// Host is not an allowed name. The Host check stops DNS rebinding — a page
// on another site reaching a local port through a name it controls; the
// address check stops everyone else on the network, whatever Host they send.
func (s studio) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.host.Remote() && !loopback(r.RemoteAddr) {
			s.host.Forbid(w, "the Studio answers this machine only; set KIT_STUDIO_REMOTE=on to reach it through a forwarded port")
			return
		}
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if !s.host.AllowedHost(host) {
			s.host.Forbid(w, "this host may not reach the Studio; add it to KIT_ALLOWED_HOSTS")
			return
		}
		w.Header().Set("Content-Security-Policy", studioCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// answer replies to r with v encoded as JSON, or with err as kit answers an
// error; a value that does not encode is kit's internal error, said as kit
// said it.
func answer[T any](s studio, w http.ResponseWriter, r *http.Request, v T, err error) {
	if err != nil {
		s.host.Reply(w, r, nil, err)
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		s.host.Fail(w, "the response could not be encoded")
		return
	}
	s.host.Reply(w, r, raw, nil)
}

// loopback reports whether a remote address is this machine.
func loopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
