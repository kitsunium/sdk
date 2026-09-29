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

// QualifiedService is the name a module gives a service it lists: the
// module's own name for the service named like it, "<module>.<service>"
// otherwise. The runtime and the analyzer both qualify with it.
func QualifiedService(module, service string) string {
	return core.QualifiedService(module, service)
}

// ModulePrefix is a module's default mount prefix: "/<name>/".
func ModulePrefix(name string) string {
	return core.ModulePrefix(name)
}

// UnderPrefix is a route declared at path, served under a module's mount
// prefix: "/reports" under "/moderation/" is "/moderation/reports", and
// under "/" it stays "/reports".
func UnderPrefix(prefix, path string) string {
	return core.UnderPrefix(prefix, path)
}
