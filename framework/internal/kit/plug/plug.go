package plug

import (
	"io/fs"
	"net/http"
	"sync/atomic"
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
