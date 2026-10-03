// Package kit — the daemon's HTTP server as the Studio sees it: its loop,
// its counters, and the middleware every request goes through.
package kit

import (
	"net/http"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
)

// middlewarePkg is the package that implements kit's own middleware.
const middlewarePkg string = "github.com/kitsunium/sdk/framework/internal/kit"

// httpLoopText says how kit's listener serves, for the Runtime view.
const httpLoopText = "sdk/v1/net/server: the SDK's engine accepts each connection, bounds and drains it, and hands it to net/http, which serves it on its own goroutine — the library's loop, not the product's"

// httpStats is the HTTP server's live counters: the requests kit's handler
// sees. The connections are the SDK engine's to count (Server.State).
type httpStats struct {
	inFlight atomic.Int64
	served   atomic.Int64
	loop     *loopState
}

// observeHTTP wraps the app's handler with the outermost middleware: the
// in-flight and served counters.
func (a *App) observeHTTP(h http.Handler) http.Handler {
	st := &httpStats{}
	a.mu.Lock()
	a.rt.http = st
	a.mu.Unlock()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.inFlight.Add(1)
		defer func() {
			st.inFlight.Add(-1)
			st.served.Add(1)
		}()
		// In dev, the goroutine serving the request is the HTTP loop's: the
		// goroutine view and the profiles name it.
		if a.hub != nil && a.hub.enabled {
			r = r.WithContext(a.asLoop(r.Context(), "http"))
		}
		h.ServeHTTP(w, r)
	})
}

// instrumentHTTP registers the accept loop of kit's listener, for the Runtime
// view. Its turns are the connections the engine accepted, read when the
// runtime is described: a stream per connection would be noise.
func (a *App) instrumentHTTP() {
	a.mu.Lock()
	st := a.rt.http
	a.mu.Unlock()
	if st == nil {
		return
	}
	l := a.loop("http", "", model.LoopHTTP, "on connection")
	a.mu.Lock()
	st.loop = l
	a.mu.Unlock()
}

// countAccepts brings the accept loop's turns up to the connections the
// engine has accepted so far: read when the runtime is described rather than
// streamed per connection.
func (a *App) countAccepts() {
	a.mu.Lock()
	srv, st := a.server, a.rt.http
	var l *loopState
	if st != nil {
		l = st.loop
	}
	a.mu.Unlock()
	if srv == nil || l == nil {
		return
	}
	accepted := int64(srv.State().Total)
	a.mu.Lock()
	l.Runs = accepted
	a.mu.Unlock()
}

// describeHTTP is the HTTP server, as the Runtime view shows it.
func (a *App) describeHTTP() *model.HTTPServer {
	a.mu.Lock()
	srv, addr, st := a.server, a.addr, a.rt.http
	a.mu.Unlock()
	if srv == nil || st == nil {
		return nil
	}
	state := srv.State()
	out := &model.HTTPServer{
		Library:    "sdk/v1/net/server",
		Loop:       httpLoopText,
		Addr:       addr,
		Middleware: a.middleware(),
		Timeouts: map[string]string{
			"readHeader": httpReadHeader.String(),
			"read":       httpRead.String(),
			"write":      httpWrite.String(),
			"idle":       httpIdle.String(),
		},
		Conns: map[string]int64{
			"active":   state.Active,
			"total":    int64(state.Total),
			"rejected": int64(state.Rejected),
		},
		InFlight: st.inFlight.Load(),
		Served:   st.served.Load(),
	}
	return out
}

// middleware is the chain every request goes through, outermost first, as
// App.Start builds it.
func (a *App) middleware() []model.Mechanic {
	chain := []model.Mechanic{
		{
			Kind: "observe", Label: "in-flight · served", Package: middlewarePkg,
			Doc: "Counts the requests being served and those served.",
		},
		{
			Kind: "recover", Label: "panic → 500", Package: middlewarePkg,
			Doc: "A panic escaping a handler is logged with its stack and answered 500, with nothing of it in the body.",
		},
		{
			Kind: "csrf", Label: "cross-origin protection", Package: "net/http",
			Doc: "http.CrossOriginProtection refuses a browser's cross-origin write (403) before any route runs.",
		},
		{
			Kind: "mux", Label: "ServeMux", Package: "net/http",
			Doc: "Routes by method and path pattern to an endpoint, a frontend, the health probes or the Studio.",
		},
	}
	if a.cfg.studio.on {
		remote := "off"
		if a.cfg.studio.remote {
			remote = "on"
		}
		chain = append(chain, model.Mechanic{
			Kind: "hostguard", Label: "host guard · /_kit/", Package: middlewarePkg,
			Doc:    "The Studio's routes answer a loopback client whose Host is allowed; the health probes do not ask.",
			Config: map[string]string{"only": "/_kit/", "remote": remote},
		})
	}
	return chain
}
