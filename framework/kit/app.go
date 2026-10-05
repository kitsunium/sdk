package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// App is the whole product: the services it mounts, served by one process.
// The same services can be mounted by several apps — one binary per service,
// or all of them in one — and each app draws exactly what it runs.
type App = ikit.App

// DiagnosticsError is returned by Start when declarations are wrong. It lists
// every problem at once, each with its position.
type DiagnosticsError = ikit.DiagnosticsError

// NewApp declares the product named name, made of services. A module's
// services come with the module: [App].With mounts it.
//
//go:noinline
func NewApp(name string, services ...*Service) *App {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewApp(name, services...)
}
