//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/internal/kit/studiokit .

// Package studiokit is what framework/kit/studio plugs into kit: the
// Studio's event stream (the SDK's net/sse) and its profiler (the SDK's
// profiling). kit imports neither: a product that does not import the
// Studio neither links nor initialises them.
package studiokit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// Enable plugs the Studio's event stream and profiler into kit.
func Enable() {
	open := openEventStream
	plug.OpenEventStream.Store(&open)
	plug.StudioProfiler.Store(&profiler)
}
