//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/kit/server .

// Package server makes a kit app in the server profile — the default — serve
// HTTP: its endpoints, its frontends and its health probes, on the SDK's
// server engine. A product that serves HTTP imports it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/server"
//
// A daemon or a CLI does not, and links none of the engine. A server that
// does not import it is refused at the start, naming the import.
package server

import ikit "github.com/kitsunium/sdk/framework/internal/kit"

// Importing the package makes kit serve HTTP: a blank package-level value
// rather than an init, the SDK's convention.
var _ = enable()

// enable turns HTTP on for every app in the server profile.
func enable() bool {
	ikit.EnableServer()
	return true
}
