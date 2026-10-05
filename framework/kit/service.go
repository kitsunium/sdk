package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Service is a bounded context of the product: it owns its building blocks —
// endpoints, stores, topics, subscriptions, workflows, jobs, frontends — and
// is the unit an App mounts. Declare one per package, as a package-level
// variable, and declare its building blocks through its methods:
//
//	var Service = kit.NewService("todos", "The todo list.")
//	var Todos = Service.Store("todos", func(t Todo) string { return t.ID })
//	var _ = Service.Endpoint("GET /todos", List)
//
// Declaring never fails: a mistake — a malformed route, a duplicate name — is
// recorded with its source position and reported, all at once, when the App
// starts. The diagram shows it too.
type Service = ikit.Service

// NewService declares a service. name must be lower-case letters, digits and
// dashes, starting with a letter; doc is one sentence saying what it owns.
//
//go:noinline
func NewService(name, doc string) *Service {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewService(name, doc)
}
