// Package kit — endpoints: an HTTP route and the operation it serves.
package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/resilience"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// EmptyValue is the request or the response of an endpoint that has none. An
// endpoint answering EmptyValue replies 204 No Content.
type EmptyValue struct{}

// EndpointService is an HTTP endpoint with a typed request and a typed response:
// what HTTP itself is about — a webhook a third party calls, say —, the
// exposure of a command or a query ([Command.Expose], [Query.Expose]), or
// the implementation of a port ([Service.Implement]). A business operation
// is a command or a query, which other code runs with [Command.Dispatch]
// and [Query.Ask] and HTTP reaches through its exposure.
type EndpointService[Req, Resp any] struct {
	nodeBase
	method string
	path   string
	opts   endpointOptions
	dec    *decoder
	// pipe is what every run of the endpoint goes through (operation.go).
	pipe pipeline[Req, Resp]
	// authn is the app's auth handler, resolved when the endpoint is mounted.
	authn authenticator
	// impl is the port the endpoint implements (Service.Implement); nil for
	// an endpoint with a route. routeless is set for every implementation,
	// even one of a nil port: it has no route, and the port reaches it.
	impl      node
	routeless bool
	// exposes is the command or the query the endpoint exposes (Expose):
	// its authentication is the operation's. accepted answers 202 for a
	// queued command.
	exposes  exposed
	accepted bool
}

// EndpointConfigurer configures an endpoint; each is an [ExposeConfigurer] too.
type EndpointConfigurer interface {
	ExposeConfigurer
	endpointConfigure(o *endpointOptions)
}

type endpointOptions struct {
	callOptions
	name    string
	maxBody int64
	// access is how an exposure says it is open on purpose — Anyone,
	// AnyUser: model.AccessAnyone, model.AccessAnyUser —; clash is set once
	// both were given (expose.go).
	access string
	clash  bool
}

// callOptions are what an endpoint, a command and a query all take: the
// authentication they ask for, and the policies in front of their handler.
type callOptions struct {
	// auth is model.AuthRequired, model.AuthOptional or "".
	auth     string
	policies []policy
}

// OperationOption configures an endpoint, a command or a query alike: the
// authentication it asks for ([Auth], [AuthOptional]) and the policies in
// front of its handler ([RateLimit], [RateLimitPerClient], [Timeout],
// [Bulkhead]).
type OperationOption interface {
	EndpointConfigurer
	CommandConfigurer
	QueryConfigurer
}

// callOption is an OperationOption.
type callOption func(o *callOptions)

// endpointConfigure sets the option on what it configures.
func (f callOption) endpointConfigure(o *endpointOptions) { f(&o.callOptions) }

// exposeConfigure sets the option on what it configures.
func (f callOption) exposeConfigure(o *endpointOptions) { f(&o.callOptions) }

// commandConfigure sets the option on what it configures.
func (f callOption) commandConfigure(o *commandOptions) { f(&o.callOptions) }

// queryConfigure sets the option on what it configures.
func (f callOption) queryConfigure(o *queryOptions) { f(&o.callOptions) }

// policy is a mechanic an endpoint runs in front of its handler.
type policy struct {
	mech   model.Mechanic
	runner resilience.Runner
}

type endpointOption func(o *endpointOptions)

// endpointConfigure sets the option on what it configures.
func (f endpointOption) endpointConfigure(o *endpointOptions) { f(o) }

// exposeConfigure sets the option on what it configures.
func (f endpointOption) exposeConfigure(o *endpointOptions) { f(o) }

// Name overrides the endpoint's name, which otherwise is its handler's
// function name, or its route when the handler is a function literal.
func Name(name string) EndpointConfigurer {
	return endpointOption(func(o *endpointOptions) { o.name = name })
}

// MaxBody bounds the request body the endpoint reads. The default is
// [DefaultMaxBody].
func MaxBody(bytes int64) EndpointConfigurer {
	return endpointOption(func(o *endpointOptions) { o.maxBody = bytes })
}

// RateLimit puts a token bucket in front of the handler: perSecond tokens a
// second, up to burst at once. An empty bucket answers 429 at once. It is the
// SDK's resilience rate limiter.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RateLimit(perSecond float64, burst int) OperationOption {
	return callOption(func(o *callOptions) {
		o.policies = append(o.policies, policy{
			mech: model.Mechanic{
				Kind:    "ratelimit",
				Label:   fmt.Sprintf("%s/s burst %d", strconv.FormatFloat(perSecond, 'f', -1, 64), burst),
				Package: "github.com/kitsunium/sdk/pkg/v1/resilience",
				Config:  map[string]string{"rate": strconv.FormatFloat(perSecond, 'f', -1, 64), "burst": strconv.Itoa(burst)},
			},
			runner: resilience.NewRateLimiter(resilience.RateLimiterConfig{Rate: perSecond, Burst: burst}),
		})
	})
}

// Timeout cancels the handler's context after d and answers 504. It is the
// SDK's resilience timeout.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Timeout(d time.Duration) OperationOption {
	return callOption(func(o *callOptions) {
		o.policies = append(o.policies, policy{
			mech: model.Mechanic{
				Kind:    "timeout",
				Label:   d.String(),
				Package: "github.com/kitsunium/sdk/pkg/v1/resilience",
				Config:  map[string]string{"after": d.String()},
			},
			runner: resilience.NewTimeout(d),
		})
	})
}

// Bulkhead caps how many requests the handler serves at once; the overflow
// answers 503. It is the SDK's resilience bulkhead.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Bulkhead(maxConcurrent int) OperationOption {
	return callOption(func(o *callOptions) {
		o.policies = append(o.policies, policy{
			mech: model.Mechanic{
				Kind:    "bulkhead",
				Label:   fmt.Sprintf("%d at once", maxConcurrent),
				Package: "github.com/kitsunium/sdk/pkg/v1/resilience",
				Config:  map[string]string{"max": strconv.Itoa(maxConcurrent)},
			},
			runner: resilience.NewBulkhead(maxConcurrent),
		})
	})
}

var (
	// OPTIONS is not offered: cross-origin protection lets it through, so a
	// preflight from any site would reach the handler.
	httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	// wildcardRe finds the wildcards of a route: {id}, {path...}.
	wildcardRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\{([^{}]*)\}`) })
	// wildcardOK is a wildcard's legal name.
	wildcardOK = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.\.\.)?$`) })
)

// parseRoute splits "METHOD /path/{param}" and returns the wildcards' names.
func parseRoute(route string) (method, path string, wildcards []string, problem phrase) {
	method, path, ok := strings.Cut(route, " ")
	if !ok || !slices.Contains(httpMethods, method) {
		return "", "", nil, say("route.method", "route", route)
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t?#") {
		return "", "", nil, say("route.path", "route", route)
	}
	if path == "/_kit" || strings.HasPrefix(path, "/_kit/") {
		return "", "", nil, say("route.reserved", "route", route)
	}
	for _, m := range wildcardRe().FindAllStringSubmatch(path, -1) {
		if !wildcardOK().MatchString(m[1]) {
			return "", "", nil, say("route.wildcard", "route", route, "wildcard", m[1])
		}
		wildcards = append(wildcards, strings.TrimSuffix(m[1], "..."))
	}
	return method, path, wildcards, phrase{}
}

// Endpoint declares an HTTP endpoint on route — "METHOD /path/{param}" — run
// by handler. The request is decoded from the path, the query, the headers
// (fields tagged `path:"…"`, `query:"…"`, `header:"…"`) and the JSON body
// (every other field), then checked against its `validate` tags, then handed
// to the policies ([RateLimit], [Timeout], [Bulkhead]) and the handler. The
// response is JSON, or 204 No Content when it is [EmptyValue].
//
// An endpoint is for what HTTP itself is about — a webhook a third party
// calls. A business operation is a command or a query, exposed on its route
// ([Command.Expose], [Query.Expose]), and run in process by the code that
// needs it ([Command.Dispatch], [Query.Ask]).
//
// The endpoint's name, and so its node ID, is the handler's function name.
//
//go:noinline
func (s *Service) Endpoint[Req, Resp any](route string, handler func(context.Context, Req) (Resp, error), opts ...EndpointConfigurer) *EndpointService[Req, Resp] {
	decl := callerPos()
	e := NewEndpointService[Req, Resp]()
	for _, o := range opts {
		o.endpointConfigure(&e.opts)
	}
	e.pipe = pipeline[Req, Resp]{handler: handler, policies: e.opts.policies}
	e.kind = model.KindEndpoint
	e.decl = decl
	hp, short := funcInfo(handler)
	if hp.file != "" {
		e.body = &hp
	}
	e.name = firstNonEmpty(e.opts.name, short, route)
	method, path, wildcards, problem := parseRoute(route)
	e.method, e.path = method, path
	s.add(e, e.opts.name != "" || short != "")
	if !problem.empty() {
		s.problemSaid(decl, e.id, problem)
		return e
	}
	if handler == nil {
		s.problem(decl, e.id, "endpoint.nil", "route", route)
	}
	dec, problems := newDecoder(reflect.TypeFor[Req](), wildcards)
	e.dec = dec
	for _, p := range problems {
		s.problem(decl, e.id, "endpoint.problem", "route", route, "problem", p)
	}
	e.pipe.ready(s, decl, e.id, invalidRoute(route))
	return e
}

// invalidRoute says that the validate tags of an endpoint's request cannot
// be read; label names it: its route, an implementation's name.
func invalidRoute(label string) func(reflect.Type, string) phrase {
	return func(t reflect.Type, detail string) phrase {
		return say("endpoint.validate", "route", label, "type", t, "detail", detail)
	}
}

// Implement declares that the service implements port: an endpoint of the
// service with no route, which only the port reaches, in process, when the
// app binds nothing else and this is the one implementation among the
// services it mounts. It runs as every endpoint does — the request's
// validate tags, the options' policies, the caller's user — and is named
// like one: its [Name], else its handler's name, else, for a function
// literal, the port's name. A handler whose request or response is not the
// port's does not compile. A host implements a module's port on its own
// service: it never adds a node to the module's.
//
//	var _ = posts.Service.Implement(desk.Enforcer, Enforce)
//
//go:noinline
func (s *Service) Implement[Req, Resp any](port *PortService[Req, Resp], handler func(context.Context, Req) (Resp, error), opts ...EndpointConfigurer) *EndpointService[Req, Resp] {
	decl := callerPos()
	e := NewEndpointService[Req, Resp]()
	for _, o := range opts {
		o.endpointConfigure(&e.opts)
	}
	e.pipe = pipeline[Req, Resp]{handler: handler, policies: e.opts.policies}
	e.routeless = true
	e.kind, e.decl = model.KindEndpoint, decl
	hp, short := funcInfo(handler)
	if hp.file != "" {
		e.body = &hp
	}
	portName := "implementation"
	if port != nil {
		e.impl, portName = port, port.name
	}
	e.name = firstNonEmpty(e.opts.name, short, portName)
	s.add(e, e.opts.name != "" || short != "")
	if port == nil {
		s.problem(decl, e.id, "implement.nil", "service", s.name)
	}
	if handler == nil {
		s.problem(decl, e.id, "endpoint.nil", "route", e.name)
	}
	e.pipe.ready(s, decl, e.id, invalidRoute(e.name))
	return e
}

// implemented is the port the endpoint implements, or nil.
func (e *EndpointService[Req, Resp]) implemented() node { return e.impl }

// spanName names an in-process run of the endpoint: its route, or an
// implementation's name.
func (e *EndpointService[Req, Resp]) spanName() string {
	if e.method == "" {
		return e.name
	}
	return e.route(e.app())
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// hasRules reports whether t carries any validate tag.
func hasRules(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = elementOf(t)
	if t.Kind() != reflect.Struct || seen[t] || t == timeType {
		return false
	}
	seen[t] = true
	for f := range t.Fields() {
		if f.Tag.Get("validate") != "" || hasRules(f.Type, seen) {
			return true
		}
	}
	return false
}

// elementOf is what t points at or holds, through pointers, slices and
// arrays.
func elementOf(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		default:
			return t
		}
	}
}

// maxBody is the largest request body the endpoint reads: its own bound, else
// DefaultMaxBody.
func (e *EndpointService[Req, Resp]) maxBody() int64 {
	if e.opts.maxBody > 0 {
		return e.opts.maxBody
	}
	return DefaultMaxBody
}

// Call runs the endpoint in-process, from another building block: the same
// validation, the same policies, the same handler, and a calls edge in the
// diagram from the caller to this endpoint. A port calls its implementation
// so. Services run each other's operations as commands and queries
// ([Command.Dispatch], [Query.Ask]); calling a handler function directly
// works, but the product cannot see it.
func (e *EndpointService[Req, Resp]) Call(ctx context.Context, req Req) (Resp, error) {
	a := e.app()
	if a == nil {
		var zero Resp
		return zero, unmounted(&e.nodeBase)
	}
	return inProcess(ctx, &callSpec{app: a, node: e, edge: model.EdgeCalls, op: model.OpCall, name: e.spanName(), auth: e.authMode()}, req, func(ctx context.Context) (Resp, error) {
		return e.pipe.invoke(ctx, a, e, req, nil)
	})
}

// perform is an endpoint's in-process run: Call.
func (e *EndpointService[Req, Resp]) perform(ctx context.Context, req Req) (Resp, error) {
	return e.Call(ctx, req)
}

// edgeKind is the kind of edge a call to the Endpoint draws.
func (e *EndpointService[Req, Resp]) edgeKind() model.EdgeKind { return model.EdgeCalls }

// route is the endpoint's route as app a serves it: its path under its
// module's prefix (ADR 0008), as declared for the product's own.
func (e *EndpointService[Req, Resp]) route(a *App) string {
	path, _ := e.servedPath(a)
	return e.method + " " + path
}

// servedPath is the endpoint's path as app a serves it; "" for the
// implementation of a port, which has none.
func (e *EndpointService[Req, Resp]) servedPath(a *App) (path string, served bool) {
	if e.path == "" {
		return "", false
	}
	prefix, _ := a.prefixOf(e.svc)
	return model.UnderPrefix(prefix, e.path), true
}

// serve is the HTTP handler of the endpoint.
func (e *EndpointService[Req, Resp]) serve(a *App) http.Handler {
	name := e.route(a)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, sp := e.requestSpan(a, w, r, name)
		ctx, rs := withResponseState(ctx, a, r)
		ctx, out := e.handle(ctx, a, w, r, sp)
		// Cookies go out before the status line, errors included.
		rs.write(ctx, w, a)
		sp.attr("http.status", strconv.Itoa(e.answer(ctx, a, w, out)))
		if sp.detailed() {
			sp.replied(out.resp, out.err, e.pipe.noBody)
		}
		sp.end(out.err)
	})
}

// outcome is what a request's run gave: the operation's response, or the
// error.
type outcome[Resp any] struct {
	resp Resp
	err  error
}

// requestSpan begins a request's span. A caller that sends a W3C
// traceparent continues its own trace; the response says which trace the
// request joined, so a client — the Studio's Try it — can find it. A
// malformed header starts a new trace.
func (e *EndpointService[Req, Resp]) requestSpan(a *App, w http.ResponseWriter, r *http.Request, name string) (context.Context, *span) {
	ctx := r.Context()
	if sc := trace.Extract(r.Header); sc.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, sc)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: e.id, from: a.clientOf(r), edge: model.EdgeCalls, op: model.OpRequest, name: name})
	if h, ok := trace.FormatTraceParent(trace.SpanContextFromContext(ctx)); ok {
		w.Header().Set("traceparent", h)
	}
	return ctx, sp
}

// handle authenticates the request, decodes it and runs it. Auth first, as
// the pipeline says: the credentials decide whether the body is read at
// all.
func (e *EndpointService[Req, Resp]) handle(ctx context.Context, a *App, w http.ResponseWriter, r *http.Request, sp *span) (context.Context, outcome[Resp]) {
	var req Req
	ctx, err := e.authenticate(ctx, a, r)
	if err != nil {
		return ctx, outcome[Resp]{err: err}
	}
	sp.user(ctx)
	if err := e.dec.decode(w, r.WithContext(ctx), e.maxBody(), &req); err != nil {
		return ctx, outcome[Resp]{err: err}
	}
	if sp.detailed() {
		sp.request(req)
	}
	resp, err := e.pipe.invoke(ctx, a, e, req, nil)
	return ctx, outcome[Resp]{resp: resp, err: err}
}

// answer writes what a request gets — 202 for a queued command's
// exposure, the result or the error — and returns the status sent.
func (e *EndpointService[Req, Resp]) answer(ctx context.Context, a *App, w http.ResponseWriter, out outcome[Resp]) int {
	if e.accepted && out.err == nil {
		return accepted(w)
	}
	return a.reply(ctx, w, out.resp, out.err, e.pipe.noBody)
}

// reply writes a result or an error, and returns the status it sent.
func (a *App) reply[T any](ctx context.Context, w http.ResponseWriter, resp T, err error, noBody bool) int {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if err != nil {
		status, body := describe(err)
		if status >= http.StatusInternalServerError {
			logger.Error(ctx, a.log, "request failed", logger.Int("status", status), logger.String("error", err.Error()))
		}
		return writeJSON(w, status, wireError{Error: body})
	}
	if noBody {
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	return writeJSON(w, http.StatusOK, resp)
}

// replyError writes err as an error answer, and returns the status it sent.
func (a *App) replyError(ctx context.Context, w http.ResponseWriter, err error) int {
	return a.reply(ctx, w, struct{}{}, err, false)
}

// accepted answers a queued command's dispatch: 202, and nothing more.
func accepted(w http.ResponseWriter) int {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusAccepted)
	return http.StatusAccepted
}

// writeJSON encodes v with encoding/json — the semantics every Go developer
// knows for omitempty — and returns the status sent.
func writeJSON(w http.ResponseWriter, status int, v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		raw = []byte(`{"error":{"code":"internal","message":"the response could not be encoded"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(raw, '\n')); err != nil {
		// The status is written: a client that went away cannot be told more.
		return status
	}
	return status
}

// mount registers the endpoint's route on r, or says why it cannot.
func (e *EndpointService[Req, Resp]) mount(a *App, r *routes) phrase {
	e.authn = a.authHandler()
	if e.routeless || e.dec == nil {
		return phrase{}
	}
	return r.handle(e.route(a), e.id, e.serve(a))
}

// describe fills the graph node out with what the Endpoint declares, and
// returns its edges.
func (e *EndpointService[Req, Resp]) describe(a *App, out *model.Node) []model.Edge {
	expose := model.ExposePublic
	if e.routeless {
		expose = model.ExposePrivate
	}
	path, _ := e.servedPath(a)
	info := &model.EndpointInfo{
		Method:   e.method,
		Path:     path,
		Expose:   expose,
		Auth:     e.authMode(),
		Access:   e.opts.access,
		Request:  requestSchemaOf(reflect.TypeFor[Req]()),
		Response: schemaOf(reflect.TypeFor[Resp]()),
	}
	if e.impl != nil {
		info.Implements = e.impl.base().id
	}
	info.Pipeline = e.drawn(a)
	out.Endpoint = info
	if e.exposes == nil {
		return nil
	}
	info.Exposes = e.exposes.base().id
	return []model.Edge{{From: e.id, To: info.Exposes, Kind: e.exposes.edgeKind(), Declared: true}}
}

// drawn is the endpoint's pipeline, in the order a request runs it: the
// user, the body, the validate tags, the policies.
func (e *EndpointService[Req, Resp]) drawn(a *App) []model.Mechanic {
	var out []model.Mechanic
	if e.authMode() != "" {
		out = append(out, authMechanic(a, e.authMode()))
	}
	if e.dec != nil && e.dec.hasBody {
		out = append(out, model.Mechanic{
			Kind: "decode", Label: "json ≤ " + humanBytes(e.maxBody()), Package: "encoding/json/v2",
			Config: map[string]string{"maxBytes": strconv.FormatInt(e.maxBody(), 10), "unknownMembers": "rejected"},
		})
	}
	if e.pipe.check != nil {
		out = append(out, validateMechanic())
	}
	for _, p := range e.opts.policies {
		out = append(out, p.mech)
	}
	return out
}

// humanBytes spells n bytes in the largest unit that divides it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

// NewEndpointService is an endpoint no service declares yet:
// [Service.Endpoint] makes one and declares it, which is how a product gets
// one.
func NewEndpointService[Req, Resp any]() *EndpointService[Req, Resp] {
	return &EndpointService[Req, Resp]{}
}
