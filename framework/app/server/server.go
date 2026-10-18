// Package server makes a kit app in the server profile — the default — serve
// HTTP: its endpoints, its frontends and its health probes, on the SDK's
// server engine. A product that serves HTTP imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/app/server"
//
// A daemon or a CLI does not, and links none of the engine. A server that
// does not import it is refused at the start, naming the import.
//
// It also plugs in the process sampler — the SDK's runtime metrics, which the
// runtime graph a serving app answers carries (ADR 0172). Without this
// package the graph has no process, and the program initialises neither
// runtime/metrics nor the SDK's process package.
package server

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/internal/app/serverengine"
)

// Importing the package makes kit serve HTTP: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable turns HTTP on for every app in the server profile.
func enable() bool {
	serverengine.Enable()
	iapp.EnableServer()
	return true
}
