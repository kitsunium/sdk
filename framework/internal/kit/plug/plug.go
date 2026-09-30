//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/internal/kit/plug .

// Package plug is where kit's opt-in subsystems plug in: the HTTP engine and
// the frontends' file server (framework/kit/server), the Studio's event
// stream and its profiler (framework/kit/studio). kit reads what they
// registered through the hooks declared here, and imports none of the SDK
// packages behind them: a product that does not import a subsystem neither
// links nor initialises its packages.
//
// The package imports the standard library and the framework's model only,
// so that kit and every subsystem can import it.
package plug

import (
	"context"
	"io/fs"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

const (
	// LabelNode is the pprof label naming the node a goroutine works for.
	LabelNode string = "kit_node"
	// LabelLoop is the pprof label naming the loop a goroutine belongs to.
	LabelLoop string = "kit_loop"
)

var (
	// NewHTTPServer builds the HTTP engine an app in the server profile
	// serves on; nil until framework/kit/server is imported.
	NewHTTPServer atomic.Pointer[func(cfg HTTPConfig) HTTPServer]
	// NewStaticFiles builds the file server of a frontend's tree; nil until
	// framework/kit/server is imported.
	NewStaticFiles atomic.Pointer[func(fsys fs.FS, csp string) (http.Handler, error)]
	// OpenEventStream opens a Server-Sent Events stream on w; nil until
	// framework/kit/studio is imported.
	OpenEventStream atomic.Pointer[func(w http.ResponseWriter, r *http.Request) (EventStream, error)]
	// StudioProfiler is the Studio's profiler; nil until framework/kit/studio
	// is imported.
	StudioProfiler atomic.Pointer[ProfilerConfig]
)

// HTTPConfig is what kit asks of the HTTP engine: where to listen, its one
// socket, its bounds, and the handler.
type HTTPConfig struct {
	// Addr is where to listen, "host:port".
	Addr string
	// Shards is how many sockets the engine opens on Addr.
	Shards int
	// ReadHeader, Read, Write and Idle bound a request's phases.
	ReadHeader, Read, Write, Idle time.Duration
	// MaxHeaderBytes bounds a request's header.
	MaxHeaderBytes int
	// Handler answers every request.
	Handler http.Handler
}

// HTTPState is what the HTTP engine says of itself once it runs: the
// address it bound, and its connections.
type HTTPState struct {
	// Addr is the address the engine bound, "" before it did.
	Addr string
	// Listeners is how many sockets it listens on.
	Listeners int
	// Active are the connections being served, Total those accepted since
	// the start, Rejected those refused.
	Active          int64
	Total, Rejected uint64
}

// ProfilerConfig is the Studio's profiler: the live heap folded onto the graph's
// nodes, and every goroutine grouped.
type ProfilerConfig struct {
	// Heap folds the live heap onto g's nodes; source says where a function
	// of a module lies, nil when it knows none.
	Heap func(g *model.Graph, source SourceFunc) (*model.Profile, error)
	// Goroutines groups every goroutine of the process, read at at.
	Goroutines func(at time.Time) (*model.Goroutines, error)
}

// SourceFunc says where the function fn, at file:line, lies in the product,
// nil when it knows none.
type SourceFunc func(file string, line int, fn string) *model.Source

// HTTPServer is the HTTP engine kit starts, reads and stops.
type HTTPServer interface {
	// Start binds the address and serves, returning once bound.
	Start(ctx context.Context) error
	// State says what the engine binds and serves.
	State() HTTPState
	// Shutdown drains the engine within ctx.
	Shutdown(ctx context.Context) error
}

// EventStream is one Server-Sent Events stream: the Studio's live events.
type EventStream interface {
	// Send writes one event's data.
	Send(data string) error
	// Done is closed when the client left.
	Done() <-chan struct{}
	// Close ends the stream.
	Close() error
}
