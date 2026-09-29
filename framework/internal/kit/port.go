// Package kit — ports: an operation a service needs and does not implement.
package kit

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// PortService is an operation a service needs and does not implement: a typed
// request, a typed response, a name. Declare it with [Service.Port], and
// call it with [PortService.Call] as an endpoint is called: validated, observed,
// drawn.
//
// What a port calls is decided when the app starts, by one rule the static
// analysis shares: the app's [Bind], else the one [Service.Implement] among
// the services the app mounts, else the port's own [Fallback]. The start
// refuses a port it cannot bind, with every other problem at once.
//
// A port is how a service reaches what it cannot import — a module its
// host's content, a service another that imports it — the consumer owning
// the contract.
type PortService[Req, Resp any] struct {
	nodeBase
	// fallback is the port's kit.Fallback, nil without one.
	fallback Operation[Req, Resp]
}

// PortConfigurer configures a port: [Fallback].
type PortConfigurer[Req, Resp any] interface {
	portConfigure(p *PortService[Req, Resp])
}

type portOption[Req, Resp any] func(p *PortService[Req, Resp])

// portConfigure sets the option on what it configures.
func (f portOption[Req, Resp]) portConfigure(p *PortService[Req, Resp]) { f(p) }

// Fallback gives a port an implementation from its own side, which it
// calls when the app binds nothing else: an endpoint — or, with ADR 0005, a
// command or a query — of the port's types. A port given here is refused
// when the app starts.
//
//	var Enforcer = Service.Port[Measure, Applied]("enforcer", kit.Fallback(QueueAPI))
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Fallback[Req, Resp any](op Operation[Req, Resp]) PortConfigurer[Req, Resp] {
	return portOption[Req, Resp](func(p *PortService[Req, Resp]) { p.fallback = op })
}

// Port declares a port of the service: an operation it needs, named name,
// taking Req and answering Resp, which another service implements
// ([Service.Implement]) or the app binds ([Bind]). Its node is
// "<service>/port/<name>"; its doc is its declaration's doc comment.
//
//go:noinline
func (s *Service) Port[Req, Resp any](name string, opts ...PortConfigurer[Req, Resp]) *PortService[Req, Resp] {
	p := NewPortService[Req, Resp]()
	p.kind, p.name, p.decl = model.KindPort, name, callerPos()
	for _, o := range opts {
		if o != nil {
			o.portConfigure(p)
		}
	}
	s.add(p, true)
	return p
}

// Call runs what the port is bound to: in a span on the port — the caller's
// calls edge — then in the bound operation's own span, through its whole
// pipeline, carrying the caller's user as [EndpointService.Call] does.
func (p *PortService[Req, Resp]) Call(ctx context.Context, req Req) (Resp, error) {
	a := p.app()
	if a == nil {
		var zero Resp
		return zero, unmounted(&p.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: p.id, from: currentNode(ctx), edge: model.EdgeCalls, op: model.OpCall, name: p.name})
	resp, err := p.run(ctx, a, req)
	sp.end(err)
	return resp, err
}

// run answers a call inside the port's span: a test's replacement first
// (kit.Replace), then what the app bound.
func (p *PortService[Req, Resp]) run(ctx context.Context, a *App, req Req) (Resp, error) {
	var zero Resp
	if fn := replaced[Req, Resp](ctx, a, p); fn != nil {
		return fn(ctx, req)
	}
	b, ok := a.bindingOf(p)
	op, typed := b.op.(Operation[Req, Resp])
	if !ok || !typed {
		// Unreachable once the app started: the start refuses a port it
		// cannot bind, and wire kept what it bound for the run.
		return zero, NewError(http.StatusInternalServerError, WireInternal, "internal error")
	}
	return op.perform(ctx, req)
}

// perform calls what the port is bound to.
func (p *PortService[Req, Resp]) perform(ctx context.Context, req Req) (Resp, error) {
	return p.Call(ctx, req)
}

// edgeKind is the kind of edge a call to the Port draws.
func (p *PortService[Req, Resp]) edgeKind() model.EdgeKind { return model.EdgeCalls }

// fallbackOp is the port's fallback, whatever its types; nil without one.
func (p *PortService[Req, Resp]) fallbackOp() node {
	if p.fallback == nil {
		return nil
	}
	return p.fallback
}

// signature is the handler an implementation of the port has: what the
// start's refusal of an unbound port says to write.
func (p *PortService[Req, Resp]) signature() string {
	return fmt.Sprintf("func(context.Context, %s) (%s, error)", reflect.TypeFor[Req](), reflect.TypeFor[Resp]())
}

// describe fills the graph node out with what the Port declares, and returns
// its edges.
func (p *PortService[Req, Resp]) describe(a *App, out *model.Node) []model.Edge {
	info := &model.PortInfo{
		Request:  schemaOf(reflect.TypeFor[Req]()),
		Response: schemaOf(reflect.TypeFor[Resp]()),
	}
	if fb := p.fallbackOp(); fb != nil && !nilOperation(fb) {
		info.Fallback = fb.base().id
	}
	out.Port = info
	if a == nil {
		return nil
	}
	b, ok := a.bindingOf(p)
	if !ok && a.wired.Load() == nil {
		// Not started: the graph says what a start would decide.
		bindings, _ := a.resolvePorts()
		b, ok = bindings[p]
	}
	if !ok {
		return nil
	}
	info.Via = b.via
	if b.op == nil {
		return nil
	}
	info.Bound = b.op.base().id
	return []model.Edge{{From: p.id, To: info.Bound, Kind: edgeKindOf(b.op), Declared: true}}
}

// porter is a port, whatever its types.
type porter interface {
	node
	fallbackOp() node
	signature() string
}

// edgeKindOf is the edge a call of an operation draws.
func edgeKindOf(op node) model.EdgeKind {
	if k, ok := op.(interface{ edgeKind() model.EdgeKind }); ok {
		return k.edgeKind()
	}
	return model.EdgeCalls
}

// nilOperation reports whether op is a nil pointer given as an operation:
// a declaration read before it was made. Its node cannot be read.
func nilOperation(op node) bool {
	v := reflect.ValueOf(op)
	return !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil())
}

// Binding a port -------------------------------------------------------------

// bindDecl is one kit.Bind the app was given.
type bindDecl struct {
	port node
	op   node
	at   pos
	// scope is the module whose kit.Mount was given it: the port must be
	// one of that module's.
	scope *Module
}

// bindOption is kit.Bind's AppOption, and its MountOption.
type bindOption struct{ decl bindDecl }

// appConfigure sets the option on what it configures.
func (b *bindOption) appConfigure(o *appOptions) { o.binds = append(o.binds, b.decl) }

// mountConfigure sets the option on what it configures.
func (b *bindOption) mountConfigure(d *mountDecl) { d.binds = append(d.binds, b.decl) }

// Bind makes the app's port call op, whatever implements the port and
// whatever its fallback: an endpoint — or, with ADR 0005, a command or a
// query — of the port's types. A binding is code, the same in every
// environment; a binding to a port, or to a service the app does not mount,
// is refused when the app starts. Given twice for one port, the last wins.
//
//	app := kit.NewApp("vigie", desk.Service, posts.Service).With(kit.Bind(desk.Enforcer, posts.EnforceAPI))
//
// It is an option of [Mount] too, under the same name: there it binds a port
// of the module mounted, and another is refused.
//
//	app := kit.NewApp("shop", posts.Service).With(kit.Mount(moderation.Module, kit.Bind(moderation.Enforcer, posts.EnforceAPI)))
//
//go:noinline
func Bind[Req, Resp any](port *PortService[Req, Resp], op Operation[Req, Resp]) interface {
	AppConfigurer
	MountConfigurer
} {
	d := bindDecl{op: op, at: callerPos()}
	if port != nil {
		d.port = port
	}
	return &bindOption{decl: d}
}

// binding is what a port calls in one app, and how the start chose it.
type binding struct {
	// op is an Operation of the port's types; nil when a test replaced the
	// port.
	op  node
	via string
}

// wiring is what the start decided about the app's operations: what each
// port calls, and what a test replaced (replace.go). It is decided once the
// declarations are checked, and read by every call.
type wiring struct {
	bound    map[node]binding
	replaced map[node]*replacement
}

// wire decides what the app's operations run for this start, once its
// declarations are checked; unmount forgets it.
func (a *App) wire() {
	bindings, _ := a.resolvePorts()
	replaced, _ := a.replacements()
	a.wired.Store(&wiring{bound: bindings, replaced: replaced})
}

// portProblems is every problem the start's rule finds with the app's
// ports and bindings.
func (a *App) portProblems() []model.Diagnostic {
	_, problems := a.resolvePorts()
	return problems
}

// bindingOf is what the running app decided port p calls.
func (a *App) bindingOf(p node) (binding, bool) {
	w := a.wired.Load()
	if w == nil {
		return binding{}, false
	}
	b, ok := w.bound[p]
	return b, ok
}

// resolvePorts applies the rule the analyzer shares to every port of the
// services the app mounts — kit.Bind, else the one Service.Implement among
// the mounted services, else the port's fallback — and returns what each
// port calls, with every problem at once: a port bound to nothing, two
// implementations and no kit.Bind, a binding to a port or to a service the
// app does not mount. A port a test replaced needs no binding.
func (a *App) resolvePorts() (map[node]binding, []model.Diagnostic) {
	r := a.portRule()
	out := map[node]binding{}
	for _, p := range r.ports {
		if b, ok := r.bind(p); ok {
			out[p] = b
		}
	}
	return out, r.problems
}

// portRule is what the start's rule reads — the services the app mounts,
// their ports and implementations, the app's bindings and replacements — and
// the problems it finds.
type portRule struct {
	a               *App
	mounted         map[*Service]bool
	ports           []porter
	implementations map[node][]node
	// binds maps a port to what the app binds it to: nil when that binding
	// is refused, which still stands for the port.
	binds    map[node]node
	replaced map[node]*replacement
	problems []model.Diagnostic
}

// portRule reads the app's services, ports, implementations, bindings and
// replacements.
func (a *App) portRule() *portRule {
	r := &portRule{a: a, mounted: map[*Service]bool{}, implementations: map[node][]node{}, binds: map[node]node{}}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		r.mounted[svc] = true
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if p, ok := n.(porter); ok {
				r.ports = append(r.ports, p)
			}
			if im, ok := n.(interface{ implemented() node }); ok && im.implemented() != nil {
				r.implementations[im.implemented()] = append(r.implementations[im.implemented()], n)
			}
		}
	}
	r.replaced, _ = a.replacements()
	for _, b := range a.opts.binds {
		r.readBind(&b)
	}
	return r
}

// refuse records a problem of node n, said where at is.
func (r *portRule) refuse(n node, at *pos, p phrase) {
	id := ""
	if n != nil {
		id = n.base().id
	}
	r.problems = append(r.problems, diagnosticOf("error", id, r.a.source(at), p))
}

// readBind takes one kit.Bind: the last one of a port wins, as kit.Set's
// value does.
func (r *portRule) readBind(b *bindDecl) {
	if b.port == nil {
		r.refuse(nil, &b.at, say("bind.nil-port"))
		return
	}
	if problem, clears := r.bindProblem(b); !problem.empty() {
		r.refuse(b.port, &b.at, problem)
		if clears {
			r.binds[b.port] = nil
		}
		return
	}
	r.binds[b.port] = b.op
}

// bindProblem is what is wrong with a binding, and whether it leaves its port
// bound to nothing; empty when the binding holds.
func (r *portRule) bindProblem(b *bindDecl) (problem phrase, clears bool) {
	port := b.port.base()
	switch {
	case b.op == nil || nilOperation(b.op):
		return say("bind.nil", "port", port.id), true
	case !r.mounted[port.svc]:
		return say("bind.port-unmounted", "port", port.id, "service", port.svc.name), false
	case b.scope != nil && port.svc.module != b.scope:
		return say("bind.scope", "port", port.id, "module", b.scope.name), true
	case b.op.base().kind == model.KindPort:
		return say("bind.port", "port", port.id, "op", b.op.base().id), true
	case !r.mounted[b.op.base().svc]:
		return say("bind.unmounted", "port", port.id, "op", b.op.base().id, "service", b.op.base().svc.name), true
	default:
		return phrase{}, false
	}
}

// bind decides what port p calls, and reports false when it cannot.
func (r *portRule) bind(p porter) (binding, bool) {
	fallback, refused := r.fallbackOf(p)
	if _, ok := r.replaced[p]; ok {
		return binding{via: model.ViaReplace}, true
	}
	if op, ok := r.binds[p]; ok {
		return binding{op: op, via: model.ViaBind}, op != nil
	}
	if op, ok, decided := r.implementation(p); decided {
		return binding{op: op, via: model.ViaImplement}, ok
	}
	id := p.base().id
	switch {
	case fallback != nil && !r.mounted[fallback.base().svc]:
		r.refuse(p, &p.base().decl, say("port.fallback-unmounted", "port", id, "op", fallback.base().id, "service", fallback.base().svc.name))
	case fallback != nil:
		return binding{op: fallback, via: model.ViaFallback}, true
	case !refused:
		r.refuse(p, &p.base().decl, say("port.unbound", "port", id, "handler", p.signature()))
	}
	return binding{}, false
}

// fallbackOf is p's fallback, nil without one. A fallback that cannot be
// one is refused whatever the port calls, and reported: the port is not said
// unbound on top of it.
func (r *portRule) fallbackOf(p porter) (fallback node, refused bool) {
	fb := p.fallbackOp()
	switch {
	case fb != nil && nilOperation(fb):
		r.refuse(p, &p.base().decl, say("port.fallback-nil", "port", p.base().id))
		return nil, true
	case fb != nil && fb.base().kind == model.KindPort:
		r.refuse(p, &p.base().decl, say("port.fallback-port", "port", p.base().id, "op", fb.base().id))
		return nil, true
	}
	return fb, false
}

// implementation is p's one implementation among the mounted services.
// decided is false without any; with two or more, the port is refused and
// ok is false.
func (r *portRule) implementation(p porter) (op node, ok, decided bool) {
	impls := r.implementations[p]
	switch len(impls) {
	case 0:
		return nil, false, false
	case 1:
		return impls[0], true, true
	}
	slices.SortFunc(impls, func(x, y node) int { return cmp.Compare(x.base().id, y.base().id) })
	names := make([]phrase, len(impls))
	for i, im := range impls {
		names[i] = plain(im.base().id)
	}
	r.refuse(p, &p.base().decl, say("port.implementations", "port", p.base().id, "implementations", listOf(names)))
	return nil, false, true
}

// NewPortService is a port no service declares yet: [Service.Port] makes
// one and declares it, which is how a product gets one.
func NewPortService[Req, Resp any]() *PortService[Req, Resp] { return &PortService[Req, Resp]{} }
