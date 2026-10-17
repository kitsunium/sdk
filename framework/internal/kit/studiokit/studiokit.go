package studiokit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// Enable plugs the Studio's event stream, its profiler and kit's
// catalogues after English into kit.
func Enable() {
	open := openEventStream
	plug.OpenEventStream.Store(&open)
	plug.StudioProfiler.Store(&profiler)
	plug.Words.Store(&words)
}
