package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Port is an operation a service needs and does not implement: a typed
// request, a typed response, a name. Declare it with [Service].Port, and
// call it with [Port].Call as an endpoint is called: validated, observed,
// drawn.
//
// What a port calls is decided when the app starts, by one rule the static
// analysis shares: the app's [Bind], else the one [Service].Implement among
// the services the app mounts, else the port's own [Fallback]. The start
// refuses a port it cannot bind, with every other problem at once.
//
// A port is how a service reaches what it cannot import — a module its
// host's content, a service another that imports it — the consumer owning
// the contract.
type Port[Req, Resp any] = iapp.PortService[Req, Resp]

// PortOption configures a port: [Fallback].
type PortOption[Req, Resp any] = iapp.PortConfigurer[Req, Resp]

// Fallback gives a port an implementation from its own side, which it
// calls when the app binds nothing else: an endpoint — or, with ADR 0005, a
// command or a query — of the port's types. A port given here is refused
// when the app starts.
//
//	var Enforcer = Service.Port[Measure, Applied]("enforcer", app.Fallback(QueueAPI))
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Fallback[Req, Resp any](op iapp.Operation[Req, Resp]) iapp.PortConfigurer[Req, Resp] {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Fallback[Req, Resp](op)
}

// Bind makes the app's port call op, whatever implements the port and
// whatever its fallback: an endpoint — or, with ADR 0005, a command or a
// query — of the port's types. A binding is code, the same in every
// environment; a binding to a port, or to a service the app does not mount,
// is refused when the app starts. Given twice for one port, the last wins.
//
//	a := app.NewApp("vigie", desk.Service, posts.Service).With(app.Bind(desk.Enforcer, posts.EnforceAPI))
//
// It is an option of [Mount] too, under the same name: there it binds a port
// of the module mounted, and another is refused.
//
//	a := app.NewApp("shop", posts.Service).With(app.Mount(moderation.Module, app.Bind(moderation.Enforcer, posts.EnforceAPI)))
//
//go:noinline
func Bind[Req, Resp any](port *iapp.PortService[Req, Resp], op iapp.Operation[Req, Resp]) interface {
	AppOption
	MountOption
} {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Bind[Req, Resp](port, op)
}
