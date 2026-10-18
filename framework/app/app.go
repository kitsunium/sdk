package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// App is the whole product: the services it mounts, served by one process.
// The same services can be mounted by several apps — one binary per service,
// or all of them in one — and each app draws exactly what it runs.
type App = iapp.App

// DiagnosticsError is returned by Start when declarations are wrong. It lists
// every problem at once, each with its position.
type DiagnosticsError = iapp.DiagnosticsError

// NewApp declares the product named name, made of services. A module's
// services come with the module: [App].With mounts it.
//
//go:noinline
func NewApp(name string, services ...*Service) *App {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.NewApp(name, services...)
}
