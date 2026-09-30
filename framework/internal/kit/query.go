// Package kit — queries: an operation that reads and changes nothing.
package kit

import (
	"context"
	"reflect"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/authz"
)

// Query reads and changes nothing: a typed input, a typed result, and one
// handler — the one declared with it. Declare it with [Service.Query]; ask it
// with [Query.Ask] from any building block, which draws an asks edge from
// the caller; give it a route with [Query.Expose]. A query is internal until
// it is exposed: how one service reads what another keeps. The static
// analysis warns of a query whose code writes a store, fires a workflow,
// publishes, sends a mail or dispatches a command.
//
// A question runs, in this order: the caller's user when the query asks for
// one ([Auth], implied by [Query.Allow]), the input's validate tags, the
// policies, the authorization ([Query.Allow], then [Query.Authorize]), then
// the handler, on the caller's goroutine.
type Query[Q, R any] struct {
	nodeBase
	pipe pipeline[Q, R]
	opts queryOptions
	access[Q]
}

// QueryConfigurer configures a query: the options every operation takes —
// [Auth], [AuthOptional], [RateLimit], [RateLimitPerClient], [Timeout],
// [Bulkhead].
type QueryConfigurer interface {
	queryConfigure(o *queryOptions)
}

type queryOptions struct {
	callOptions
}

// Query declares a query of the service: an operation named name, run by
// handler, that reads and changes nothing. Its node is
// "<service>/query/<name>".
//
//	var MyOrders = Service.Query("my-orders", myOrders, kit.Auth()).Expose("GET /orders", kit.AnyUser())
//
//go:noinline
func (s *Service) Query[Q, R any](name string, handler func(context.Context, Q) (R, error), opts ...QueryConfigurer) *Query[Q, R] {
	q := NewQuery[Q, R]()
	for _, o := range opts {
		if o != nil {
			o.queryConfigure(&q.opts)
		}
	}
	q.kind, q.name, q.decl = model.KindQuery, name, callerPos()
	q.pipe = pipeline[Q, R]{handler: handler, policies: q.opts.policies}
	if p, _ := funcInfo(handler); p.file != "" {
		q.body = &p
	}
	s.add(q, true)
	if handler == nil {
		s.problem(q.decl, q.id, "query.nil", "query", q.id)
	}
	q.pipe.ready(s, q.decl, q.id, invalidInput(q.id))
	return q
}

// Allow makes the query need a permission, as data: the app's policy must
// let the caller — its user, with the attributes its auth data gives when it
// implements [AttrsProvider] — do action to resource. It implies [Auth]; given
// twice, both permissions are needed. The refusal is 403
// permission_denied, one sentence whatever the cause.
//
//go:noinline
func (q *Query[Q, R]) Allow(policy authz.Policy, action, resource string) *Query[Q, R] {
	q.allow(&q.nodeBase, callerPos(), policy, action, resource)
	return q
}

// Authorize gives the query a rule that needs the data: fn sees the input
// and, through its context, the caller, and returns nil, [Forbidden],
// [NotFound] — to hide what exists — or an authz.Check of its own. It runs
// last before the handler, after [Query.Allow]; its refusal is 403
// permission_denied, one sentence whatever the cause, and a NotFound or an
// Unauthenticated stays what it is.
//
//go:noinline
func (q *Query[Q, R]) Authorize(fn func(context.Context, Q) error) *Query[Q, R] {
	q.authorize(q.svc, callerPos(), q.id, fn)
	return q
}

// Expose declares an endpoint that asks the query, on route, with the
// options an endpoint takes ([RateLimitPerClient], [MaxBody], [Name]): named
// after the query, authenticated when the query asks for a user, the rest
// of the pipeline the query's. It answers 200 with the result, 204 for
// [EmptyValue].
//
// An exposed query says who may ask it: a permission ([Query.Allow]), a rule
// ([Query.Authorize]), or, when it declares neither, a marker that it is
// open on purpose — [Anyone], or [AnyUser] for any signed-in user. The
// start refuses an exposure that says none of them. A query nobody exposes
// is internal: the code that needs it asks it.
//
//go:noinline
func (q *Query[Q, R]) Expose(route string, opts ...ExposeConfigurer) *Query[Q, R] {
	expose[Q, R](q.svc, callerPos(), q, route, opts)
	return q
}

// Ask runs the query for the node ctx runs inside — in a span on the query,
// its caller's asks edge —, through its whole pipeline, and returns its
// result. The caller's user goes with it.
func (q *Query[Q, R]) Ask(ctx context.Context, in Q) (R, error) {
	a := q.app()
	if a == nil {
		var zero R
		return zero, unmounted(&q.nodeBase)
	}
	return inProcess(ctx, &callSpec{app: a, node: q, edge: model.EdgeAsks, op: model.OpAsk, name: q.name, auth: q.authMode(), payloads: true}, in, func(ctx context.Context) (R, error) {
		return q.pipe.invoke(ctx, a, q, in, q.steps(a))
	})
}

// steps are a query's own, inside its policies: its authorization, then
// its handler.
func (q *Query[Q, R]) steps(a *App) steps[Q] {
	if !q.guarded() {
		return nil
	}
	return func(ctx context.Context, in Q, handle func(context.Context) error) error {
		if err := q.check(ctx, a, q.id, in); err != nil {
			return err
		}
		return handle(ctx)
	}
}

// perform is a query's in-process run: Ask.
func (q *Query[Q, R]) perform(ctx context.Context, in Q) (R, error) {
	return q.Ask(ctx, in)
}

// edgeKind is the kind of edge a call to the Query draws.
func (q *Query[Q, R]) edgeKind() model.EdgeKind { return model.EdgeAsks }

// authMode is how the query asks for a user: a permission implies one.
func (q *Query[Q, R]) authMode() string { return q.modeOf(q.opts.auth) }

// describe fills the graph node out with what the Query declares, and returns
// its edges.
func (q *Query[Q, R]) describe(a *App, out *model.Node) []model.Edge {
	info := &model.QueryInfo{
		Input:       schemaOf(reflect.TypeFor[Q]()),
		Result:      schemaOf(reflect.TypeFor[R]()),
		Auth:        q.authMode(),
		Permissions: q.permissions(),
	}
	if mode := q.authMode(); mode != "" {
		info.Pipeline = append(info.Pipeline, authMechanic(a, mode))
	}
	if q.pipe.check != nil {
		info.Pipeline = append(info.Pipeline, validateMechanic())
	}
	for _, p := range q.opts.policies {
		info.Pipeline = append(info.Pipeline, p.mech)
	}
	info.Pipeline = append(info.Pipeline, q.mechanics()...)
	if q.ruleAt != nil && a != nil {
		info.Authorize = a.source(q.ruleAt)
	}
	out.Query = info
	return nil
}

// NewQuery is a query no service declares yet: [Service.Query] makes one
// and declares it, which is how a product gets one.
func NewQuery[Q, R any]() *Query[Q, R] { return &Query[Q, R]{} }
