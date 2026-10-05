package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Module is a set of services a product mounts as one: [NewModule] declares
// it, [App].With or [Mount] mounts it.
type Module = ikit.Module

// ModulePart is what a module is made of: its services, the modules it
// requires ([Requires]), and the migrations of the data it keeps outside
// kit's stores ([Migrations]).
type ModulePart = ikit.ModuleConfigurer

// NewModule declares a module named name — lower-case letters, digits and
// dashes, starting with a letter; "kit", "external" and "schema" are kit's — that doc
// describes, made of parts: its services, which it adopts, the modules it
// requires ([Requires]), and the migrations of the data it keeps outside
// kit's stores ([Migrations]), run on the database that keeps it. Declare it
// as a package-level variable of the one package a product imports:
//
//	var Module = kit.NewModule("moderation",
//		"Reports and moderation under the DSA.\n\nfr: Signalements et modération selon le DSA.",
//		intake.Service, desk.Service, kit.Requires(identity.Module))
//
// A service a module lists is its own: what it declared so far is renamed,
// and what it declares later is born qualified. Listing a service links its
// package; a service is a module's only, and a module's services take
// declarations from the module's own Go module only.
//
//go:noinline
func NewModule(name, doc string, parts ...ModulePart) *Module {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewModule(name, doc, parts...)
}

// Requires says the module needs other: an app that mounts the module and
// not other mounts other at its defaults, and starts it first. The Go import
// this takes makes a cycle impossible. A collaboration a module can do
// without is a port with a fallback instead ([Fallback]).
func Requires(other *Module) ModulePart {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Requires(other)
}
