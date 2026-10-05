// Modules: the services a module adopts, their qualified names, and the files
// of a module.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

type (
	// Module is a module the app mounts: a Go module's services, released and
	// mounted together like a bundle (ADR 0008). Everything a module declares is
	// qualified with its name: its services are "<module>.<service>" — the one
	// named like the module is "<module>" —, so its nodes are
	// "<module>.<service>/<kind>/<name>" and carry [Node].Module.
	Module = core.ModuleMessage
)

type (
	// File is one file a graph points at: its path, relative to its root — the
	// product's module, or the Go module [Source].GoModule names.
	File = core.FileMessage
)

// qualifiedService is QualifiedService's body: decl_gen.go writes QualifiedService, from the
// design, as one call of it.
func qualifiedService(module, service string) string {
	return core.QualifiedService(module, service)
}

// modulePrefix is ModulePrefix's body: decl_gen.go writes ModulePrefix, from the
// design, as one call of it.
func modulePrefix(name string) string {
	return core.ModulePrefix(name)
}

// underPrefix is UnderPrefix's body: decl_gen.go writes UnderPrefix, from the
// design, as one call of it.
func underPrefix(prefix, path string) string {
	return core.UnderPrefix(prefix, path)
}
