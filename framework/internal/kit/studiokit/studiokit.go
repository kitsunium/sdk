package studiokit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// Enable plugs the Studio's event stream and profiler into kit.
func Enable() {
	open := openEventStream
	plug.OpenEventStream.Store(&open)
	plug.StudioProfiler.Store(&profiler)
}
