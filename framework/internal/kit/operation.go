package kit

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/validation"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// Operation takes a typed request and answers a typed response: an
// endpoint, a port — and, with ADR 0005, a command or a query. It is what
// [Fallback] and [Bind] give a port, and what [Replace] replaces in a test;
// a request or a response of other types does not compile. Its methods are
// unexported: only kit implements it.
//
// Every operation is a node of the graph, and runs in a span of its own
// through its whole pipeline — authentication, validation, policies, the
// Studio's mocks — whoever calls it.
type Operation[Req, Resp any] interface {
	node
	// perform runs the operation in process, on behalf of the node ctx runs
	// inside: what Endpoint.Call does for an endpoint and Port.Call for a
	// port — a span on the operation, its pipeline, its handler.
	perform(ctx context.Context, req Req) (Resp, error)
	// edgeKind is the edge a call of the operation draws, and the one a port
	// bound to it draws to it: calls, for an endpoint or a port.
	edgeKind() model.EdgeKind
}

// pipeline is what an operation runs around its handler, whoever calls it:
// the Studio's mock on its node first, then the request's validate tags,
// then the policies ([RateLimit], [Timeout], [Bulkhead]) around the steps of
// its kind and the handler — or a test's replacement —; a panic is answered
// as an internal error and logged with its stack.
type pipeline[Req, Resp any] struct {
	handler  func(context.Context, Req) (Resp, error)
	check    validation.Constraint[Req]
	policies []policy
	// noBody is set when the response is Empty: there is nothing to answer.
	noBody bool
}

// steps are the steps of an operation's kind, run inside its policies and
// around its handler: they call handle once they pass, and may end after it.
type steps[Req any] func(ctx context.Context, req Req, handle func(context.Context) error) error

// ready readies what every run checks before the handler: the request's
// validate tags. invalid says, for the operation, that the tags of a type
// cannot be read.
func (p *pipeline[Req, Resp]) ready(s *Service, decl pos, id string, invalid func(t reflect.Type, detail string) phrase) {
	reqType := reflect.TypeFor[Req]()
	if reqType.Kind() == reflect.Struct && hasRules(reqType, map[reflect.Type]bool{}) {
		c, err := validation.Struct[Req](validation.StructConfig{})
		if err != nil {
			s.problemSaid(decl, id, invalid(reqType, errs.PublicOf(err)))
		} else {
			p.check = c
		}
	}
	p.noBody = answersNothing[Resp]()
}

// answersNothing reports whether an operation's response is Empty: it has
// nothing to answer — 204 over HTTP.
func answersNothing[Resp any]() bool { return reflect.TypeFor[Resp]() == emptyType }

// emptyType is Empty's type.
var emptyType = reflect.TypeFor[EmptyValue]()

// validateMechanic is the step of a pipeline that checks the validate tags.
func validateMechanic() model.Mechanic {
	return model.Mechanic{Kind: "validate", Label: "validate", Package: "github.com/kitsunium/sdk/pkg/v1/app/validation"}
}

// validate checks req against its validate tags: nil, or the 400 that lists
// every violation.
func (p *pipeline[Req, Resp]) validate(req Req) error {
	if p.check == nil {
		return nil
	}
	report := p.check(validation.RootPath, req)
	if report.OK() {
		return nil
	}
	v := make([]ViolationMessage, len(report))
	for i, x := range report {
		v[i] = ViolationMessage{Path: x.Path, Rule: x.Rule, Message: x.Message}
	}
	return &Error{Status: http.StatusBadRequest, Code: WireInvalid, Message: "the request is invalid", Violations: v}
}

// invoke runs the operation on a decoded request.
func (p *pipeline[Req, Resp]) invoke(ctx context.Context, a *App, n node, req Req, kind steps[Req]) (Resp, error) {
	return p.run(ctx, a, n, req, kind)
}

// run validates the request, then runs the rest of the pipeline.
func (p *pipeline[Req, Resp]) run(ctx context.Context, a *App, n node, req Req, kind steps[Req]) (Resp, error) {
	if err := p.validate(req); err != nil {
		var zero Resp
		return zero, err
	}
	return p.around(ctx, a, n, req, kind)
}

// around runs the policies around the steps of the operation's kind and the
// handler: what a queued command's consumer runs, its input checked when it
// was dispatched.
func (p *pipeline[Req, Resp]) around(ctx context.Context, a *App, n node, req Req, kind steps[Req]) (resp Resp, err error) {
	handle := func(ctx context.Context) (err error) {
		resp, err = p.call(ctx, a, n, req)
		return err
	}
	op := func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = panicked(ctx, a, n.base().id, "handler panicked", r)
			}
		}()
		if kind == nil {
			return handle(ctx)
		}
		return kind(ctx, req, handle)
	}
	for _, pol := range slices.Backward(p.policies) {
		runner, next := pol.runner, op
		op = func(ctx context.Context) error { return runner.Run(ctx, next) }
	}
	err = op(ctx)
	return resp, err
}

// call runs the handler — or, in a test, its replacement, and only there:
// the rest of the pipeline is the operation's own.
func (p *pipeline[Req, Resp]) call(ctx context.Context, a *App, n node, req Req) (Resp, error) {
	handler := p.handler
	if fn := replaced[Req, Resp](ctx, a, n); fn != nil {
		handler = fn
	}
	return handler(ctx, req)
}

// panicked is the error a recovered panic answers: an internal error that
// says nothing of it, the panic and its stack logged.
func panicked[P any](ctx context.Context, a *App, node, what string, p P) error {
	if a != nil {
		logger.Error(ctx, a.log, what, logger.String("node", node), logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
	}
	return NewError(http.StatusInternalServerError, WireInternal, "internal error")
}

// callSpec is how an in-process run is drawn: the app it runs in and the
// operation's node, the edge its caller travels, its span's operation and
// name, the user it asks for, and — for a command or a query — its
// payloads, kept by its span in dev.
type callSpec struct {
	app      *App
	node     node
	edge     model.EdgeKind
	op, name string
	auth     string
	payloads bool
}

// ran is an operation's run behind its span: its response, or its error.
type ran[Resp any] func(context.Context) (Resp, error)

// inProcess runs an operation for the node ctx runs inside: a span on the
// operation's node — the caller's edge of the operation's kind —, then run,
// when the caller may (admitted).
func inProcess[Req, Resp any](ctx context.Context, call *callSpec, req Req, run ran[Resp]) (Resp, error) {
	ctx, sp := call.app.begin(ctx, &spanStart{node: call.node.base().id, from: currentNode(ctx), edge: call.edge, op: call.op, name: call.name})
	payloads := call.payloads && sp.detailed()
	if payloads {
		sp.request(req)
	}
	resp, err := admitted(ctx, call, run)
	if payloads {
		sp.replied(resp, err, answersNothing[Resp]())
	}
	sp.end(err)
	return resp, err
}

// admitted runs run, unless the operation asks for a user and the caller
// carries none: an in-process call carries its caller's and is not
// authenticated again — without one, it is refused like a request.
func admitted[Resp any](ctx context.Context, call *callSpec, run ran[Resp]) (Resp, error) {
	if _, ok := UserID(ctx); !ok && call.auth == model.AuthRequired {
		var zero Resp
		return zero, Unauthenticated("this " + string(call.node.base().kind) + " requires an authenticated caller")
	}
	return run(ctx)
}

// unmounted is the error of an operation whose service no app runs in this
// process.
func unmounted(n *nodeBase) error {
	return Unavailable(fmt.Sprintf("service %q is not running in this process", n.svc.name))
}
