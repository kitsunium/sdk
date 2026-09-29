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

// exposed is an operation an endpoint exposes, whatever its types.
type exposed interface {
	node
	// authMode is how the operation asks for a user: its exposure
	// authenticates the request so.
	authMode() string
	edgeKind() model.EdgeKind
}

// authModeSource is an operation an endpoint of its types can expose.
type authModeSource[Req, Resp any] interface {
	Operation[Req, Resp]
	authMode() string
}

// expose declares on s the endpoint of route that runs op: named after it,
// or by kit.Name; answering 202 when op is a queued command.
func expose[Req, Resp any](s *Service, decl pos, op authModeSource[Req, Resp], route string, opts []EndpointConfigurer) {
	q, queues := op.(interface{ queued() bool })
	e := NewEndpointService[Req, Resp]()
	e.exposes, e.accepted = op, queues && q.queued()
	for _, o := range opts {
		if o != nil {
			o.endpointConfigure(&e.opts)
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
	switch {
	case e.opts.private:
		s.problem(decl, e.id, "expose.private", "node", op.base().id)
	case e.opts.auth != "":
		s.problem(decl, e.id, "expose.auth", "node", op.base().id)
	}
	dec, problems := newDecoder(reflect.TypeFor[Req](), wildcards)
	e.dec = dec
	for _, p := range problems {
		s.problem(decl, e.id, "endpoint.problem", "route", route, "problem", p)
	}
}
