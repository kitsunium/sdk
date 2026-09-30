// Package kit — authentication: the handler that says who calls, and the user
// it names.
package kit

import (
	"context"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// Authentication: one handler per app turns a request's credentials into a
// user, in front of every endpoint declared with [Auth] or [AuthOptional].
//
// The handler is a node of its own. It runs inside a span on that node —
// whose From names the endpoint it guards — but its span carries no edge:
// every guarded endpoint would otherwise grow an arrow to it, and the
// diagram says which endpoints ask for a user through their pipeline instead.
// What the handler does — a session looked up in a store — is drawn from the
// auth node like any other code.

// UID identifies an authenticated user. It is what the auth handler returns
// and what every endpoint reads with [UserID].
type UID string

// AuthenticatorHandler is the app's authentication handler: it turns the
// credentials of a request — a session cookie, a bearer token — into a user,
// in front of every endpoint declared with [Auth] or [AuthOptional]. An app
// has at most one.
//
// P is the credentials: a struct whose fields are tagged cookie:"…" or
// header:"…", decoded like an endpoint's request. D is what the handler says
// about the user; endpoints read it with [AuthData].
type AuthenticatorHandler[P, D any] struct {
	nodeBase
	handler func(context.Context, P) (UID, D, error)
	dec     *decoder
}

// AuthHandler declares the app's authentication handler. The handler returns
// the user's ID and data; an empty UID with a nil error means "no
// credentials", which [Auth] endpoints answer 401 and [AuthOptional]
// endpoints serve anonymously; an error — usually [Unauthenticated] — refuses
// the request.
//
// The handler runs only for a request that carries at least one of the
// credentials' cookies or headers; a request with none is anonymous without
// asking it.
//
//go:noinline
func (s *Service) AuthHandler[P, D any](name string, handler func(context.Context, P) (UID, D, error)) *AuthenticatorHandler[P, D] {
	a := &AuthenticatorHandler[P, D]{handler: handler}
	a.kind, a.name, a.decl = model.KindAuth, name, callerPos()
	if p, _ := funcInfo(handler); p.file != "" {
		a.body = &p
	}
	s.add(a, true)
	if handler == nil {
		s.problem(a.decl, a.id, "auth.nil", "name", name)
	}
	dec, problems := newDecoder(reflect.TypeFor[P](), nil)
	a.dec = dec
	for _, p := range append(problems, credentialProblems(reflect.TypeFor[P]())...) {
		s.problem(a.decl, a.id, "auth.problem", "name", name, "problem", p)
	}
	return a
}

// credentialProblems checks that the credentials type reads cookies and
// headers only. A body belongs to the endpoint, and a credential in the query
// string ends up in every access log between the client and the product.
func credentialProblems(t reflect.Type) []phrase {
	if t.Kind() != reflect.Struct {
		return []phrase{say("auth.credentials.not-struct", "type", t)}
	}
	var out []phrase
	params := 0
	for _, f := range reflect.VisibleFields(t) {
		if embedsStruct(&f) {
			continue
		}
		read, problem := credentialField(&f)
		if read {
			params++
		}
		if problem != nil {
			out = append(out, *problem)
		}
	}
	if params == 0 && len(out) == 0 {
		out = append(out, say("auth.credentials.none", "type", t))
	}
	return out
}

// embedsStruct reports whether f embeds a struct, or a pointer to one, whose
// fields the walk visits on their own.
func embedsStruct(f *reflect.StructField) bool {
	if !f.Anonymous {
		return false
	}
	ft := f.Type
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	return ft.Kind() == reflect.Struct
}

// credentialField says whether f is read from a cookie or a header, and what
// is wrong with it when it is read from elsewhere, or from nowhere.
func credentialField(f *reflect.StructField) (read bool, problem *phrase) {
	switch in, _ := paramTag(*f); in {
	case "cookie", "header":
		return true, nil
	case "":
		if f.IsExported() && f.Tag.Get("json") != "-" {
			return false, new(say("auth.credentials.untagged", "field", f.Name))
		}
		return false, nil
	default:
		return false, new(say("auth.credentials.source", "field", f.Name, "in", in))
	}
}

// describe fills the graph node out with what the Authenticator declares, and
// returns its edges.
func (a *AuthenticatorHandler[P, D]) describe(app *App, out *model.Node) []model.Edge {
	out.Auth = &model.AuthInfo{
		Credentials: requestSchemaOf(reflect.TypeFor[P]()),
		Data:        schemaOf(reflect.TypeFor[D]()),
		Endpoints:   app.guardedEndpoints(),
	}
	return nil
}

// authenticator is the app's auth handler, whatever its types.
type authenticator interface {
	node
	// authenticate runs the handler for a request to endpoint, and returns
	// the caller — the zero principal when the request carries no
	// credentials.
	authenticate(ctx context.Context, a *App, endpoint string, r *http.Request) (principal, error)
	// dataType is the type of what the handler says about the caller.
	dataType() reflect.Type
}

// dataType is the type of the auth data the handler returns.
func (h *AuthenticatorHandler[P, D]) dataType() reflect.Type { return reflect.TypeFor[D]() }

// authenticate runs the handler on the request r to endpoint, and returns who
// calls.
func (h *AuthenticatorHandler[P, D]) authenticate(ctx context.Context, a *App, endpoint string, r *http.Request) (principal, error) {
	if h.dec == nil || h.handler == nil {
		return principal{}, NewError(http.StatusInternalServerError, WireInternal, "internal error")
	}
	if !h.dec.present(r) {
		return principal{}, nil
	}
	var creds P
	if err := h.dec.setParams(r, reflect.ValueOf(&creds).Elem()); err != nil {
		return principal{}, Unauthenticated("the request's credentials are malformed")
	}
	ctx, sp := a.begin(ctx, &spanStart{node: h.id, from: endpoint, op: model.OpAuth, name: h.name})
	uid, data, err := h.call(ctx, a, creds)
	sp.end(err)
	if err != nil {
		return principal{}, err
	}
	return principal{uid: uid, data: data}, nil
}

// call runs the handler; a panic is logged with its stack and becomes an
// internal error that says nothing about it.
func (h *AuthenticatorHandler[P, D]) call(ctx context.Context, a *App, creds P) (uid UID, authData D, err error) {
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "auth handler panicked", logger.String("node", h.id), logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			err = NewError(http.StatusInternalServerError, WireInternal, "internal error")
		}
	}()
	return h.handler(ctx, creds)
}

// Auth makes an endpoint require an authenticated caller: the app's auth
// handler runs first, and a request without valid credentials is answered
// 401 before the handler runs. A command or a query that asks for one is
// refused 401 when its caller carries no user: an in-process dispatch
// carries its caller's, and is not authenticated again.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Auth() OperationOption {
	return callOption(func(o *callOptions) { o.auth = model.AuthRequired })
}

// AuthOptional runs the app's auth handler when the request carries
// credentials, and serves anonymous callers too: [UserID] tells which.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func AuthOptional() OperationOption {
	return callOption(func(o *callOptions) { o.auth = model.AuthOptional })
}

// authKey carries the authenticated caller.
type authKey struct{}

type principal struct {
	uid  UID
	data any
}

// UserID returns the authenticated caller of the request ctx serves, and
// whether there is one. An in-process [EndpointService.Call] carries its caller's
// user along.
func UserID(ctx context.Context) (UID, bool) {
	p, ok := ctx.Value(authKey{}).(principal)
	if !ok || p.uid == "" {
		return "", false
	}
	return p.uid, true
}

// AuthData returns what the auth handler said about the caller, and whether
// there is a caller whose data has type D.
func AuthData[D any](ctx context.Context) (D, bool) {
	var zero D
	p, ok := ctx.Value(authKey{}).(principal)
	if !ok || p.uid == "" {
		return zero, false
	}
	d, ok := p.data.(D)
	return d, ok
}

// WithUser returns ctx acting as the given user, with data: what a job, a
// loop or a test uses to call an [Auth] endpoint in-process on someone's
// behalf.
func WithUser[D any](ctx context.Context, uid UID, data D) context.Context {
	return context.WithValue(ctx, authKey{}, principal{uid: uid, data: data})
}

// authenticate runs the app's auth handler in front of an endpoint that asks
// for it, and returns ctx carrying the caller.
func (e *EndpointService[Req, Resp]) authenticate(ctx context.Context, a *App, r *http.Request) (context.Context, error) {
	mode := e.authMode()
	if mode == "" {
		return ctx, nil
	}
	h := e.authn
	if h == nil {
		// Start refuses an app like this; a request can only get here if the
		// endpoint was mounted without it.
		return ctx, NewError(http.StatusInternalServerError, WireInternal, "internal error")
	}
	p, err := h.authenticate(ctx, a, e.id, r)
	if err != nil {
		return ctx, err
	}
	if p.uid == "" {
		if mode == model.AuthRequired {
			return ctx, Unauthenticated("this endpoint requires an authenticated caller")
		}
		return ctx, nil
	}
	return context.WithValue(ctx, authKey{}, p), nil
}

// authMode is how the endpoint asks for authentication: model.AuthRequired,
// model.AuthOptional or "" — an exposure as the operation it exposes does.
func (e *EndpointService[Req, Resp]) authMode() string {
	if e.exposes != nil {
		return e.exposes.authMode()
	}
	return e.opts.auth
}

// authHandler returns the app's auth handler, or nil. With several — a
// declaration error — it returns the first.
//
// IFACE-PLUGIN: the auth handler the app declares is one of several generic
// types, each behind this port.
func (a *App) authHandler() authenticator {
	if a == nil {
		return nil
	}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if h, ok := n.(authenticator); ok {
				return h
			}
		}
	}
	return nil
}

// guardedEndpoints counts the endpoints of the app that ask for
// authentication — an exposure of an operation that asks for a user
// included.
func (a *App) guardedEndpoints() int {
	if a == nil {
		return 0
	}
	count := 0
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if g, ok := n.(interface{ authMode() string }); ok && g.authMode() != "" && n.base().kind == model.KindEndpoint {
				count++
			}
		}
	}
	return count
}

// authProblems judges authentication across the app: at most one handler,
// and one whenever an endpoint, a command or a query asks for a user.
func (a *App) authProblems() []model.Diagnostic {
	var handlers, guarded []node
	for _, n := range a.mountedNodes() {
		if _, ok := n.(authenticator); ok {
			handlers = append(handlers, n)
		}
		if g, ok := n.(interface{ authMode() string }); ok && g.authMode() != "" {
			guarded = append(guarded, n)
		}
	}
	if len(handlers) == 0 {
		return a.unauthenticated(guarded)
	}
	var out []model.Diagnostic
	first := handlers[0].base()
	for _, extra := range handlers[1:] {
		said := say("auth.two", "first", first.id, "second", extra.base().id)
		if src := a.source(&first.decl); src != nil {
			said = say("auth.two-at", "first", first.id, "file", src.File, "line", src.Line, "second", extra.base().id)
		}
		out = append(out, diagnosticOf("error", extra.base().id, a.source(&extra.base().decl), said))
	}
	return out
}

// unauthenticated refuses every node that asks for a user in an app with no
// auth handler.
func (a *App) unauthenticated(guarded []node) []model.Diagnostic {
	var out []model.Diagnostic
	for _, n := range guarded {
		said := say("auth.missing-for", "node", n.base().id)
		if n.base().kind == model.KindEndpoint {
			said = say("auth.missing", "endpoint", n.base().id)
		}
		out = append(out, diagnosticOf("error", n.base().id, a.source(&n.base().decl), said))
	}
	return out
}

// principalProblems warns that every Allow refuses when the app's auth data
// gives the SDK's authz no attributes: its type does not implement
// kit.Principal.
func (a *App) principalProblems() []model.Diagnostic {
	h := a.authHandler()
	if h == nil || h.dataType().Implements(principalType) {
		return nil
	}
	var allowing []phrase
	for _, n := range a.mountedNodes() {
		if g, ok := n.(interface{ allows() bool }); ok && g.allows() {
			allowing = append(allowing, plain(n.base().id))
		}
	}
	if len(allowing) == 0 {
		return nil
	}
	return []model.Diagnostic{diagnosticOf("warning", h.base().id, a.source(&h.base().decl),
		say("principal.missing", "handler", h.base().id, "type", h.dataType(), "nodes", listOf(allowing)))}
}

// mountedNodes are the nodes of every service the app mounts.
func (a *App) mountedNodes() []node {
	var out []node
	for _, svc := range a.services {
		if svc != nil {
			nodes, _ := svc.snapshot()
			out = append(out, nodes...)
		}
	}
	return out
}

// appProblems is what can only be judged across the nodes an app mounts and
// the environment it runs in.
func (a *App) appProblems() []model.Diagnostic {
	a.mu.Lock()
	settings := slices.Clone(a.settingProblems)
	a.mu.Unlock()
	return slices.Concat(a.authProblems(), a.principalProblems(), a.portProblems(), a.replaceProblems(), a.mailerProblems(), a.secretProblems(), a.sealingProblems(), a.databaseProblems(), settings, a.privacyProblems(), a.profileProblems())
}

// authMechanic is the first step of a guarded endpoint's pipeline: the app's
// auth handler, and whether a user is required.
func authMechanic(a *App, mode string) model.Mechanic {
	m := model.Mechanic{Kind: "auth", Label: "auth", Config: map[string]string{"mode": mode}}
	if a != nil {
		if h := a.authHandler(); h != nil {
			m.Label = h.base().name
			m.Config["handler"] = h.base().id
		}
	}
	return m
}
