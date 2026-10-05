// Package studio serves, in dev, the read-only API the kit tool's Studio
// reads: the graph and its event stream, traces, instances and items, the
// source, the dev tools — the process, the databases, goroutines, the heap
// profile, logs, the mailbox, former values. A server imports it to be seen
// by the Studio:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/studio"
//
// Without it a product links none of those routes, the event stream or the
// profiler; in dev the start warns that the Studio was asked for.
package studio

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/internal/kit/studiokit"
)

// Importing the package mounts the Studio's API: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable turns the Studio's API on for every serving app in dev.
func enable() bool {
	studiokit.Enable()
	ikit.EnableStudio()
	return true
}
