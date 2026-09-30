// Package kit — exposures: an operation an endpoint serves over HTTP.
package kit

import (
	"reflect"

	"github.com/kitsunium/sdk/framework/model"
)

// One line to HTTP: Command.Expose and Query.Expose declare an endpoint
// that dispatches the command, or asks the query. It is an endpoint like any
// other — decoded as strictly, answered by describe, routed, listed with the
// HTTP connector's routes — named after its operation, with a declared
// dispatches or asks edge to it. Its authentication is the operation's; the
// rest of the pipeline — validation, key, authorization — is the
// operation's own, so an exposed run and an in-process one are checked
// alike.
//
// An operation reached over HTTP says who may run it: its permissions
// (Allow), its rule (Authorize), or, on the exposure, that it is open on
// purpose — Anyone, AnyUser. The start refuses an exposure that says none of
// them (check, below); an operation nobody exposes is internal, run by the
// code that dispatches or asks it.

// exposureOption is an ExposeConfigurer of exposures alone: an endpoint does
// not take it.
type exposureOption func(o *endpointOptions)

// exposeConfigure applies the option to an exposure's options.
func (f exposureOption) exposeConfigure(o *endpointOptions) { f(o) }

// Anyone says that an exposure is open to everyone, on purpose: its
// operation declares no permission and no rule, and whoever reaches the
// route may run it — a public form, say. It says so on the exposure's node,
// where the diagram and the API page read it. An exposure whose operation
// declares no rule refuses the start unless it says this, or [AnyUser].
//
//	var Report = Service.Command("report", file).Expose("POST /reports", kit.Anyone())
//
// It adds no authentication: an operation that asks for a user ([Auth])
// takes [AnyUser] instead, and one with [AuthOptional] still knows the
// caller who signed in.
func Anyone() ExposeConfigurer { return accessMarker(model.AccessAnyone) }

// AnyUser says that an exposure is open to any signed-in user, on purpose:
// its operation asks for a user ([Auth]) and declares no permission and no
// rule — its handler reads [UserID] and keeps to what that user owns.
//
//	var MyOrders = Service.Query("my-orders", myOrders, kit.Auth()).Expose("GET /orders", kit.AnyUser())
//
// It adds no authentication of its own: an exposure authenticates as its
// operation asks, so the operation declares [Auth].
func AnyUser() ExposeConfigurer { return accessMarker(model.AccessAnyUser) }

// accessMarker records how an exposure says it is open; given both
// markers, the exposure is refused.
func accessMarker(access string) ExposeConfigurer {
	return exposureOption(func(o *endpointOptions) {
		if o.access != "" && o.access != access {
			o.clash = true
		}
		o.access = access
	})
}

// markerOf names the marker that set access, as a product writes it.
func markerOf(access string) string {
	if access == model.AccessAnyUser {
		return "kit.AnyUser()"
	}
	return "kit.Anyone()"
}

// expose declares on s the endpoint of route that runs op: named after it,
// or by kit.Name; answering 202 when op is a queued command.
func expose[Req, Resp any](s *Service, decl pos, op authModeSource[Req, Resp], route string, opts []ExposeConfigurer) {
	q, queues := op.(interface{ queued() bool })
	e := NewEndpointService[Req, Resp]()
	e.exposes, e.accepted = op, queues && q.queued()
	for _, o := range opts {
		if o != nil {
			o.exposeConfigure(&e.opts)
		}
	}
	e.pipe = pipeline[Req, Resp]{handler: op.perform, policies: e.opts.policies}
	e.pipe.noBody = answersNothing[Resp]()
	e.kind, e.decl = model.KindEndpoint, decl
	e.name = firstNonEmpty(e.opts.name, op.base().name)
	method, path, wildcards, problem := parseRoute(route)
	e.method, e.path = method, path
	s.add(e, true)
	if !problem.empty() {
		s.problemSaid(decl, e.id, problem)
		return
	}
	if e.opts.auth != "" {
		s.problem(decl, e.id, "expose.auth", "node", op.base().id)
	}
	if e.opts.clash {
		s.problem(decl, e.id, "expose.markers", "node", op.base().id)
	}
	dec, problems := newDecoder(reflect.TypeFor[Req](), wildcards)
	e.dec = dec
	for _, p := range problems {
		s.problem(decl, e.id, "endpoint.problem", "route", route, "problem", p)
	}
}

// check says, once every builder of its operation ran — Allow and
// Authorize may follow Expose in a chain —, whether an exposure says who
// may run its operation: a permission or a rule of the operation, else one
// marker that agrees with the user the operation asks for. The start
// refuses the rest at the exposure's line; an endpoint that exposes
// nothing has nothing to say, and two markers are said where they are
// given (expose).
func (e *EndpointService[Req, Resp]) check() []phrase {
	if e.exposes == nil || e.opts.clash {
		return nil
	}
	if p := accessProblem(e.exposes, e.opts.access); !p.empty() {
		return []phrase{p}
	}
	return nil
}

// accessProblem is what the start refuses of an exposure of op marked with
// access ("" for none): no rule and no marker, a marker beside a rule, or a
// marker the user op asks for contradicts. The empty phrase lets it through.
func accessProblem(op exposed, access string) phrase {
	id := op.base().id
	switch {
	case access == "" && op.guarded():
		return phrase{}
	case access == "":
		return say("expose.open", "node", id)
	case op.guarded():
		return say("expose.marked", "node", id, "marker", markerOf(access))
	}
	required := op.authMode() == model.AuthRequired
	if access == model.AccessAnyone && required {
		return say("expose.anyone", "node", id)
	}
	if access == model.AccessAnyUser && !required {
		return say("expose.any-user", "node", id)
	}
	return phrase{}
}
