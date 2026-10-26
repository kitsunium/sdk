package http1

import (
	"bytes"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// statusUnauthorized answers a request the authenticating link refused.
const statusUnauthorized int = 401

// bearerScheme is the authentication scheme Bearer reads, lowercase.
const bearerScheme string = "bearer"

var (
	// wwwAuthenticate is the field a 401 from Bearer carries, with no spare
	// capacity, so a handler appending to it copies it.
	wwwAuthenticate = slices.Clip([]byte("WWW-Authenticate: Bearer\r\n"))
	// credentialFields are the request fields Log never records, lowercase.
	credentialFields = [...]string{"authorization", "proxy-authorization", "cookie"}
)

// RouteValue is one route of a router link: an identifier the application
// gives meaning to, a method, a canonical path and whether the path is a
// prefix. The identifier is what the chain names a route by — an
// authenticating link's exemptions, an access-log record, the handler's
// switch.
type RouteValue struct {
	// ID identifies the route, from 1; zero is "no route".
	ID int
	// Method is the method the route takes, empty for any.
	Method string
	// Path is the canonical path the route matches, from "/": the whole of
	// it, or its start when Prefix is set.
	Path string
	// Prefix makes Path match the start of a request's path.
	Prefix bool
}

// route is a RouteValue as the layer matches it: bytes, compared to the
// request's without a conversion.
type route struct {
	// id is the route's ID.
	id int
	// method is the route's method, empty for any.
	method []byte
	// path is the canonical path, or its start.
	path []byte
	// prefix makes path match the start of a request's path.
	prefix bool
}

// link is what every layer of the chain keeps of the handler it wraps.
type link struct {
	// next is the handler the layer wraps.
	next corenet.HTTP1Handler
	// batcher is next as a batcher, nil when it is not one.
	batcher corenet.HTTP1Batcher
}

// newLink finds next's batcher once, never per batch.
func newLink(next corenet.HTTP1Handler) link {
	b, _ := next.(corenet.HTTP1Batcher)
	//: both, from the one handler.
	return link{next: next, batcher: b}
}

// endBatch ends the next handler's batch when it is a batcher.
func (l *link) endBatch(lane int) {
	//: a handler that defers nothing has no batch to end.
	if l.batcher != nil {
		l.batcher.EndBatch(lane)
	}
}

// misconfigured wraps HTTP1Misconfigured with the link and what it refused.
func misconfigured(linkName, what string) error {
	//: once, at construction.
	return errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("link", linkName), errs.String("option", what))
}

// Routes returns the router link of an http1 chain: it matches each request's
// canonical Path against routes, in order, sets HTTP1RequestValue.Route to the
// first match's ID — zero when none matches — and calls the next handler,
// always: a request no route matches still reaches the next links, so an
// authenticating link judges it like any other and only the handler answers
// 404. A route matches when its Method is empty or the request's, and its
// Path is the request's, or a prefix of it when Prefix is set. An ID of zero
// or below, a repeated ID, a Path that does not start with "/" and a Method
// that is not a token are refused with corenet.HTTP1Misconfigured.
func Routes(routes ...RouteValue) (corenet.Middleware[corenet.HTTP1Handler], error) {
	table := make([]route, 0, len(routes))
	//: each route checked once, here, never per request.
	for i, r := range routes {
		//: the first rule broken names the route's field.
		if err := checkRoute(r, routes[:i]); err != nil {
			//: refused.
			return nil, err
		}
		table = append(table, route{id: r.ID, method: []byte(r.Method), path: []byte(r.Path), prefix: r.Prefix})
	}
	//: the middleware builds one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the table is shared, read only.
		return &RouteLayer{link: newLink(next), routes: table}
	}, nil
}

// checkRoute refuses a route whose ID, method or path breaks a rule, against
// the routes before it.
func checkRoute(r RouteValue, before []RouteValue) error {
	//: zero is "no route", so an ID starts at one.
	if r.ID <= 0 {
		//: refused.
		return misconfigured("Routes", "id")
	}
	//: an ID names one route.
	for _, b := range before {
		//: the same ID twice.
		if b.ID == r.ID {
			//: refused.
			return misconfigured("Routes", "id")
		}
	}
	//: a method is a token, or any.
	if r.Method != "" && !token(r.Method) {
		//: refused.
		return misconfigured("Routes", "method")
	}
	//: a path the codec would refuse can never match.
	if r.Path == "" || r.Path[0] != '/' || checkPath([]byte(r.Path)) != RefusalNone {
		//: refused.
		return misconfigured("Routes", "path")
	}
	//: a route the layer can match.
	return nil
}

// RouteLayer is the router link Routes builds: the layer it wraps around the
// next handler.
type RouteLayer struct {
	link
	// routes are the routes, in the order given.
	routes []route
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *RouteLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	req.Route = 0
	//: the first route that matches.
	for i := range l.routes {
		r := &l.routes[i]
		//: the method, then the path or its start.
		if (len(r.method) == 0 || bytes.Equal(r.method, req.Method)) &&
			(bytes.Equal(r.path, req.Path) || (r.prefix && bytes.HasPrefix(req.Path, r.path))) {
			req.Route = r.id
			break
		}
	}
	l.next.ServeHTTP1(req, res)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *RouteLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// Bearer returns the authenticating link of an http1 chain: every request
// whose Route is not in exempt must carry "Authorization: Bearer <token>"
// that v verifies, or is answered 401 with WWW-Authenticate: Bearer and goes
// no further. The link resets Auth and Authenticated first, then lets v set
// the subject and the claims on the request's own Auth — a value, so nothing
// of them aliases the token's bytes — and sets Authenticated before the next
// handler runs. An exempt route reaches the next handler unauthenticated.
// The scheme matches case-insensitively, the token is what follows its
// spaces. A nil v, or an exempt ID of zero or below, is refused with
// corenet.HTTP1Misconfigured.
func Bearer(v corenet.HTTP1BearerVerifier, exempt ...int) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: no verifier would authenticate nobody, or everybody.
	if v == nil {
		//: refused.
		return nil, misconfigured("Bearer", "verifier")
	}
	//: an exemption names a route.
	for _, id := range exempt {
		//: zero is "no route": exempting it would exempt every unrouted request.
		if id <= 0 {
			//: refused.
			return nil, misconfigured("Bearer", "exempt")
		}
	}
	ids := slices.Clone(exempt)
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the exemptions are shared, read only.
		return &BearerLayer{link: newLink(next), v: v, exempt: ids}
	}, nil
}

// BearerLayer is the authenticating link Bearer builds: the layer it wraps
// around the next handler.
type BearerLayer struct {
	link
	// v verifies the token.
	v corenet.HTTP1BearerVerifier
	// exempt are the routes that need no token.
	exempt []int
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *BearerLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	req.Auth.Reset()
	req.Authenticated = false
	//: an exempt route goes on unauthenticated.
	if l.exempted(req.Route) {
		l.next.ServeHTTP1(req, res)
		//: not judged.
		return
	}
	tok, ok := bearerToken(req)
	//: no token, or one the verifier refuses: 401, and no further.
	if !ok || !l.v.VerifyBearer(tok, &req.Auth) {
		req.Auth.Reset()
		res.Status = statusUnauthorized
		addHeader(res, wwwAuthenticate)
		//: refused.
		return
	}
	req.Authenticated = true
	l.next.ServeHTTP1(req, res)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *BearerLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// exempted reports whether route is one of the exemptions.
func (l *BearerLayer) exempted(route int) bool {
	//: a few routes at most: a scan.
	return slices.Contains(l.exempt, route)
}

// bearerToken returns the token of an "Authorization: Bearer <token>" field,
// and false when there is none.
func bearerToken(req *corenet.HTTP1RequestValue) ([]byte, bool) {
	v := req.Authorization
	//: the scheme, case-insensitive, then at least one space.
	if !req.HasAuthorization || len(v) <= len(bearerScheme) || v[len(bearerScheme)] != ' ' ||
		!foldIs(v[:len(bearerScheme)], bearerScheme) {
		//: no Bearer credentials.
		return nil, false
	}
	tok := bytes.TrimLeft(v[len(bearerScheme):], " ")
	//: an empty token is none (the value's trailing spaces are already trimmed).
	return tok, len(tok) > 0
}

// addHeader gives res the field lines b: b itself when res has none, which
// costs nothing, or a copy after the ones it has.
func addHeader(res *corenet.HTTP1ResponseValue, b []byte) {
	//: the common case: no field yet, the link's bytes, which nothing appends into.
	if len(res.Header) == 0 {
		res.Header = b
		//: set.
		return
	}
	//: appended: a slice with no spare room is copied, never written into.
	res.Header = append(slices.Clip(res.Header), b...)
}

// Headers returns the link that gives every answer the field lines pairs
// names, written once by AppendField: name, value, name, value. It sets the
// response's Header before the next handler runs, as a slice with no spare
// capacity, so a handler that appends its own fields copies them rather than
// write into the link's bytes. An odd count, and every name AppendField
// refuses, are refused with corenet.HTTP1Misconfigured.
func Headers(pairs ...string) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: a name without its value.
	if len(pairs)%2 != 0 {
		//: refused.
		return nil, misconfigured("Headers", "pairs")
	}
	var b []byte
	//: each pair written once, here.
	for i := 0; i < len(pairs); i += 2 {
		var err error
		b, err = AppendField(b, pairs[i], pairs[i+1])
		//: a name the codec owns, or one that is not a token.
		if err != nil {
			//: refused, the name given.
			return nil, misconfigured("Headers", pairs[i])
		}
	}
	b = slices.Clip(b)
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the bytes are shared, read only.
		return &HeadersLayer{link: newLink(next), fields: b}
	}, nil
}

// HeadersLayer is the link Headers builds: the layer it wraps around the next
// handler.
type HeadersLayer struct {
	link
	// fields are the field lines every answer carries.
	fields []byte
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *HeadersLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	addHeader(res, l.fields)
	l.next.ServeHTTP1(req, res)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *HeadersLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// Log returns the access-log link of an http1 chain: once the next handler
// has answered, it hands l one corenet.HTTP1AccessValue — lane, route,
// method, canonical path, status, body length, whether the request was
// authenticated and whether its answer was deferred — with the request's
// fields named in allow, at most corenet.HTTP1MaxAccessFields of them. It
// never logs the query, the body or a credential: Authorization,
// Proxy-Authorization and Cookie in allow are refused with
// corenet.HTTP1Misconfigured, as are a nil l, an empty or repeated name and a
// name that is not a token.
func Log(l corenet.HTTP1AccessLogger, allow ...string) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: a link that logs nowhere.
	if l == nil {
		//: refused.
		return nil, misconfigured("Log", "logger")
	}
	//: the record has a fixed number of slots.
	if len(allow) > corenet.HTTP1MaxAccessFields {
		//: refused.
		return nil, misconfigured("Log", "allow")
	}
	names := make([][]byte, 0, len(allow))
	//: each name checked once, here.
	for i, name := range allow {
		//: not a token, a credential, or a repeat.
		if !token(name) || credential(name) || repeated(name, allow[:i]) {
			//: refused, the name given.
			return nil, misconfigured("Log", name)
		}
		names = append(names, []byte(name))
	}
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the names are shared, read only.
		return &LogLayer{link: newLink(next), l: l, allow: allow, names: names}
	}, nil
}

// credential reports whether name is a field Log never records.
func credential(name string) bool {
	//: three names, case-insensitive.
	for _, c := range credentialFields {
		//: a credential.
		if foldIs(name, c) {
			//: never logged.
			return true
		}
	}
	//: not one.
	return false
}

// repeated reports whether name is among before, case-insensitive.
func repeated(name string, before []string) bool {
	//: a few names: a scan.
	for _, b := range before {
		//: the same field twice.
		if foldEqualBytes([]byte(name), []byte(b)) {
			//: repeated.
			return true
		}
	}
	//: new.
	return false
}

// LogLayer is the access-log link Log builds: the layer it wraps around the
// next handler.
type LogLayer struct {
	link
	// l receives the records.
	l corenet.HTTP1AccessLogger
	// allow are the field names a record carries, as given.
	allow []string
	// names are allow's bytes, a record's field names.
	names [][]byte
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *LogLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	l.next.ServeHTTP1(req, res)
	rec := corenet.HTTP1AccessValue{
		Lane: req.Lane, Route: req.Route, Method: req.Method, Path: req.Path,
		BodyLen: len(res.Body), Authenticated: req.Authenticated, Deferred: res.Defer,
	}
	//: a deferred answer has no status yet; zero is 200 otherwise.
	if !res.Defer {
		rec.Status = orDefault(res.Status, statusOK)
	}
	//: the allowed fields the request carried, in the link's order.
	for i, name := range l.allow {
		//: a field absent from the request takes no slot.
		if v, ok := req.Field(name); ok {
			rec.Fields[rec.NFields] = corenet.HTTP1FieldValue{Name: l.names[i], Value: v}
			rec.NFields++
		}
	}
	l.l.LogHTTP1(rec)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *LogLayer) EndBatch(lane int) {
	l.endBatch(lane)
}
