package serverkit

import (
	"context"
	"io/fs"
	"net/http"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/net/server"
	"github.com/kitsunium/sdk/pkg/v1/net/static"
)

// httpServer is the SDK's server engine, one group of one listener.
type httpServer struct {
	srv *server.Server
}

// Enable plugs the HTTP engine and the frontends' file server into kit.
func Enable() {
	newServer, newFiles := newHTTPServer, newStaticFiles
	plug.NewHTTPServer.Store(&newServer)
	plug.NewStaticFiles.Store(&newFiles)
}

// newHTTPServer builds the engine cfg describes: its engine accepts, bounds
// and drains the connections, net/http speaks the protocol.
//
// IFACE-PLUGIN: kit reaches the engine through plug.HTTPServer; this is its
// one implementation.
func newHTTPServer(cfg plug.HTTPConfig) plug.HTTPServer {
	srv := server.New()
	srv.Group("http",
		server.Listen("tcp", cfg.Addr), server.Shards(cfg.Shards),
		server.ReadHeaderTimeout(cfg.ReadHeader), server.ReadTimeout(cfg.Read),
		server.WriteTimeout(cfg.Write), server.IdleTimeout(cfg.Idle),
		server.MaxHeaderBytes(cfg.MaxHeaderBytes),
	).HandleHTTP(cfg.Handler)
	return httpServer{srv: srv}
}

// newStaticFiles serves the tree fsys as a single-page application, under
// the content security policy csp.
func newStaticFiles(fsys fs.FS, csp string) (http.Handler, error) {
	return static.New(fsys, static.Config{ContentSecurityPolicy: csp, SinglePageApp: true})
}

// Start binds the address and serves, returning once bound.
func (h httpServer) Start(ctx context.Context) error { return h.srv.Start(ctx) }

// State says what the engine binds and serves.
func (h httpServer) State() plug.HTTPState {
	st := h.srv.State()
	out := plug.HTTPState{Active: st.ActiveConns, Total: st.TotalConns, Rejected: st.RejectedConns, Listeners: len(st.Listeners)}
	if len(st.Listeners) > 0 {
		out.Addr = st.Listeners[0].Address
	}
	return out
}

// Shutdown drains the engine within ctx.
func (h httpServer) Shutdown(ctx context.Context) error { return h.srv.Shutdown(ctx) }
