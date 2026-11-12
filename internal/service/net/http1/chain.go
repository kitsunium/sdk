package http1

import (
	"bytes"
	"io/fs"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// statusUnauthorized answers a request the authenticating link refused.
const statusUnauthorized int = 401

// bearerScheme is the authentication scheme Bearer reads, lowercase.
const bearerScheme string = "bearer"

// traceFieldName is the field Trace reads, in the case every comparison folds to.
const traceFieldName string = "traceparent"

const (
	// statusTooManyRequests answers a request a limiter refused.
	statusTooManyRequests int = 429
	// routeKeyLen is the width of a number used as a rate key.
	routeKeyLen int = 8
	// byteBits is how many bits a byte holds.
	byteBits int = 8
	// corsOriginField is the field a browser names its page's origin in;
	// lowercase, as the codec's comparison folds it.
	corsOriginField string = "origin"
	// corsRequestMethodField asks a preflight's question.
	corsRequestMethodField string = "access-control-request-method"
	// requestIDFieldName is the field RequestID reads, in the case every
	// comparison folds to.
	requestIDFieldName string = "x-request-id"
	// mintedIDLen is the length of a minted identifier: a 64-bit number in hex.
	mintedIDLen int = 16
	// lowerHex are a minted identifier's digits.
	lowerHex string = "0123456789abcdef"
	// nibbleBits is how many bits one hexadecimal digit spells.
	nibbleBits int = 4
	// nibbleMask keeps one digit's bits.
	nibbleMask uint64 = 0xf
	// laneShift places the lane above a request's counter (HTTP1RequestValue.ID).
	laneShift int = 48
	// counterMask keeps a request's counter below its lane.
	counterMask uint64 = 1<<laneShift - 1
)

const (
	// rateKeyNone is the zero RateKey, refused.
	rateKeyNone rateKeyKind = iota
	// rateKeySubject keys on HTTP1RequestValue.Auth.
	rateKeySubject
	// rateKeyRoute keys on HTTP1RequestValue.Route.
	rateKeyRoute
	// rateKeyLane keys on HTTP1RequestValue.Lane.
	rateKeyLane
	// rateKeyField keys on a request field.
	rateKeyField
)

var (
	// wwwAuthenticate is the field a 401 from Bearer carries, with no spare
	// capacity, so a handler appending to it copies it.
	wwwAuthenticate = slices.Clip([]byte("WWW-Authenticate: Bearer\r\n"))
	// credentialFields are the request fields Log never records, lowercase.
	credentialFields = [...]string{"authorization", "proxy-authorization", "cookie"}
	// traceLineStart opens the field line Trace writes on an answer.
	traceLineStart = []byte("traceparent: ")
	// traceLineEnd closes it.
	traceLineEnd = []byte("\r\n")
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
	// Prefix makes Path match the start of a request's path, on a segment
	// boundary: "/health" matches "/health" and "/health/live", never
	// "/healthz"; a Path ending in "/" matches whatever follows it.
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

// wrapped returns the handler the link wraps, so the adapter can reach what a
// chain ends in.
func (l *link) wrapped() corenet.HTTP1Handler {
	//: the next handler of the chain.
	return l.next
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
// Path is the request's, or a prefix of it ending on a segment boundary when
// Prefix is set. An ID of zero or below, a repeated ID, a Path that does not
// start with "/" and a Method that is not a token are refused with
// corenet.HTTP1Misconfigured.
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
	req.Route, req.RoutePrefix = 0, 0
	//: the first route that matches.
	for i := range l.routes {
		r := &l.routes[i]
		//: the method, then the path or its start.
		if (len(r.method) == 0 || bytes.Equal(r.method, req.Method)) &&
			(bytes.Equal(r.path, req.Path) || (r.prefix && prefixOf(r.path, req.Path))) {
			req.Route = r.id
			//: a prefix route says how far it reached, for a file tree's name.
			if r.prefix {
				req.RoutePrefix = len(r.path)
			}
			break
		}
	}
	l.next.ServeHTTP1(req, res)
}

// prefixOf reports whether prefix starts path on a segment boundary: path is
// prefix, prefix ends in '/', or path's next byte is '/' — so a prefix route
// never takes a sibling whose name merely starts like it.
func prefixOf(prefix, path []byte) bool {
	//: the bytes first, then the boundary after them.
	return bytes.HasPrefix(path, prefix) &&
		(len(path) == len(prefix) || prefix[len(prefix)-1] == '/' || path[len(prefix)] == '/')
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
// has answered, it hands l one corenet.HTTP1AccessValue — lane, route, method,
// canonical path, status, body length, whether the request was authenticated,
// the request's ID and its RequestID and the Elapsed time from the batch's
// start — with the fields LogAllow names. A deferred answer has a second
// record when it completes, Completed set, carrying its status and the time
// since it was deferred; a Pending answer has only the first, its status zero,
// a worker's answer being the handler's own to log. LogSlowerThan keeps the
// slow records, LogSample one in n, and with both, every slow one and one in n
// of the rest. It never logs the query, the body or a credential: Authorization,
// Proxy-Authorization and Cookie in LogAllow are refused with
// corenet.HTTP1Misconfigured, as are a nil l, an empty or repeated name, a name
// that is not a token, more names than corenet.HTTP1MaxAccessFields, a threshold
// that is not positive, a sample of zero or below and a nil clock.
func Log(l corenet.HTTP1AccessLogger, opts ...LogOption) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: a link that logs nowhere.
	if l == nil {
		//: refused.
		return nil, misconfigured("Log", "logger")
	}
	cfg, err := newLogConfig(opts)
	//: an option the link cannot honour.
	if err != nil {
		//: refused, the option named.
		return nil, err
	}
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the names are shared, read only.
		return &LogLayer{link: newLink(next), l: l, cfg: cfg}
	}, nil
}

// LogOption tunes the Log link: the value LogAllow, LogSlowerThan, LogSample or
// LogClock returns, judged when the link is built.
type LogOption struct {
	// apply folds the option into the link's configuration, or refuses it.
	apply func(*logConfig) error
}

// LogAllow names request fields a record carries, at most
// corenet.HTTP1MaxAccessFields in all; a credential is refused.
func LogAllow(names ...string) LogOption {
	kept := slices.Clone(names)
	//: accumulated, so LogAllow may be given more than once.
	return LogOption{apply: func(c *logConfig) error {
		c.allow = append(c.allow, kept...)
		//: the names are judged together, once every one is known.
		return nil
	}}
}

// LogSlowerThan keeps only the records whose Elapsed reaches d, which must be
// positive.
func LogSlowerThan(d time.Duration) LogOption {
	//: judged when the link is built.
	return LogOption{apply: func(c *logConfig) error {
		//: zero would keep everything, and a negative one means nothing.
		if d <= 0 {
			//: refused.
			return misconfigured("Log", "slower")
		}
		c.slower = d
		//: kept.
		return nil
	}}
}

// LogSample keeps one record in n, by the request's counter, so the same
// request answers alike on both of a deferred answer's records. n must be
// positive.
func LogSample(n int) LogOption {
	//: judged when the link is built.
	return LogOption{apply: func(c *logConfig) error {
		//: zero would divide by nothing.
		if n <= 0 {
			//: refused.
			return misconfigured("Log", "sample")
		}
		c.sample = uint64(n)
		//: kept.
		return nil
	}}
}

// LogClock is the clock the link reads for Elapsed; clock.System by default.
func LogClock(c clock.Clock) LogOption {
	//: judged when the link is built.
	return LogOption{apply: func(cfg *logConfig) error {
		//: a clock is what every record reads.
		if c == nil {
			//: refused.
			return misconfigured("Log", "clock")
		}
		cfg.clk = c
		//: kept.
		return nil
	}}
}

// CORS returns the cross-origin link of an http1 chain: it answers a preflight
// itself — OPTIONS with Origin and Access-Control-Request-Method, 204, the policy's
// fields — before any link after it, so a Bearer link never sees a preflight, and
// gives every other answer the policy's fields. An origin compared byte for byte
// and not listed receives no CORS field and is still served, the browser being
// what enforces it. A value the policy refuses is corenet.CORSMisconfigured.
func CORS(cfg corenet.CORSValue) (corenet.Middleware[corenet.HTTP1Handler], error) {
	policy, err := corenet.NewCORSPolicy(cfg)
	//: an origin, method, field or age it cannot honour: refused here, not per request.
	if err != nil {
		//: as the policy refused it.
		return nil, err
	}
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the policy is shared, read only.
		return &CORSLayer{link: newLink(next), policy: policy}
	}, nil
}

// CORSLayer is the link CORS builds: the layer it wraps around the next
// handler.
type CORSLayer struct {
	link
	// policy is the built cross-origin policy.
	policy *corenet.CORSPolicy
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *CORSLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	origin, _ := req.Field(corsOriginField)
	//: a preflight is answered here: nothing behind this link has its credentials.
	if len(origin) > 0 && bytes.Equal(req.Method, methodOptions) {
		//: the question only a preflight asks.
		if method, ok := req.Field(corsRequestMethodField); ok {
			res.Status = statusNoContent
			res.CORS = l.policy.Preflight(origin, method).Lines
			//: answered.
			return
		}
	}
	res.CORS = l.policy.Actual(origin).Lines
	l.next.ServeHTTP1(req, res)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *CORSLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// logConfig is every option of a link, checked.
type logConfig struct {
	// allow are the field names a record carries, as given; names their bytes.
	allow []string
	names [][]byte
	// slower is the slow threshold, zero when unset.
	slower time.Duration
	// sample keeps one record in sample, zero when unset.
	sample uint64
	// clk is the clock read.
	clk clock.Clock
}

// newLogConfig folds and checks opts.
func newLogConfig(opts []LogOption) (*logConfig, error) {
	cfg := &logConfig{clk: clock.System}
	//: each option in turn, the last of a kind winning.
	for _, o := range opts {
		//: a refused value is named.
		if err := o.apply(cfg); err != nil {
			//: refused.
			return nil, err
		}
	}
	//: the record has a fixed number of slots.
	if len(cfg.allow) > corenet.HTTP1MaxAccessFields {
		//: refused.
		return nil, misconfigured("Log", "allow")
	}
	cfg.names = make([][]byte, 0, len(cfg.allow))
	//: each name checked once, here.
	for i, name := range cfg.allow {
		//: not a token, a credential, or a repeat.
		if !token(name) || credential(name) || repeated(name, cfg.allow[:i]) {
			//: refused, the name given.
			return nil, misconfigured("Log", name)
		}
		cfg.names = append(cfg.names, []byte(name))
	}
	//: checked.
	return cfg, nil
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
	// cfg holds the link's options, read only.
	cfg *logConfig
	// lanes holds each lane's batch stamp and deferred records.
	lanes laneTable[logLane]
}

// logLane is what the link keeps per lane, between the requests of a batch.
type logLane struct {
	// start is when the batch's first request reached the link.
	start time.Time
	// started says whether start is this batch's.
	started bool
	// deferred are the batch's deferred answers still to complete.
	deferred []deferredAccess
}

// deferredAccess is a deferred answer's first record, kept for its second.
type deferredAccess struct {
	// rec is the first record: its slices live until the batch's end.
	rec corenet.HTTP1AccessValue
	// at is when the answer was deferred.
	at time.Time
	// res is the deferred answer, valid until EndBatch returns.
	res *corenet.HTTP1ResponseValue
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *LogLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	lane := l.lanes.get(req.Lane)
	//: the first request of a batch stamps it: the others start from it.
	if !lane.started {
		lane.start, lane.started = l.cfg.clk.Now(), true
	}
	l.next.ServeHTTP1(req, res)
	now := l.cfg.clk.Now()
	rec := corenet.HTTP1AccessValue{
		Lane: req.Lane, Route: req.Route, Method: req.Method, Path: req.Path,
		BodyLen: len(res.Body), Authenticated: req.Authenticated, Stage: stageOf(res),
		TraceID: req.Trace.TraceID, RequestID: req.RequestID, ID: req.ID,
		Proto: req.Proto, Stream: req.Stream,
		Elapsed: corenet.DurationValue(now.Sub(lane.start)),
	}
	//: an answer the batch's end or a worker fills has no status yet; zero is
	//: 200 otherwise.
	if !res.Defer && !res.Pending {
		rec.Status = orDefault(res.Status, statusOK)
	}
	//: the allowed fields the request carried, in the link's order.
	for i, name := range l.cfg.allow {
		//: a field absent from the request takes no slot.
		if v, ok := req.Field(name); ok {
			rec.Fields[rec.NFields] = corenet.HTTP1FieldValue{Name: l.cfg.names[i], Value: v}
			rec.NFields++
		}
	}
	//: kept for the completion record, by value: the request moves on.
	if res.Defer {
		lane.deferred = append(lane.deferred, deferredAccess{rec: rec, at: now, res: res})
	}
	l.emit(&rec)
}

// stageOf names what a record says about the answer res holds.
func stageOf(res *corenet.HTTP1ResponseValue) corenet.HTTP1AccessStage {
	//: the two ways an answer leaves ServeHTTP1 unwritten.
	switch {
	case res.Defer:
		//: completed at the batch's end.
		return corenet.HTTP1AccessDeferred
	case res.Pending:
		//: the worker's to answer.
		return corenet.HTTP1AccessPending
	}
	//: written now.
	return corenet.HTTP1AccessAnswered
}

// emit hands rec to the logger when the link's filters keep it.
func (l *LogLayer) emit(rec *corenet.HTTP1AccessValue) {
	//: judged per record, so a slow completion is logged though its first was not.
	if l.keep(rec) {
		l.l.LogHTTP1(*rec)
	}
}

// keep reports whether a record passes the link's filters: every slow one, and
// one in n of the rest; with neither filter, every record.
func (l *LogLayer) keep(rec *corenet.HTTP1AccessValue) bool {
	slow := l.cfg.slower > 0 && rec.Elapsed.Duration() >= l.cfg.slower
	sampled := l.cfg.sample > 0 && (rec.ID&counterMask)%l.cfg.sample == 0
	//: no filter at all keeps everything; otherwise either filter admits it.
	return (l.cfg.slower == 0 && l.cfg.sample == 0) || slow || sampled
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does, then gives each answer deferred in the batch its
// completion record.
func (l *LogLayer) EndBatch(lane int) {
	l.finishBatch(lane)
}

// finishBatch ends the next handler's batch, then writes the batch's deferred
// answers their completion records and forgets its stamp.
func (l *LogLayer) finishBatch(lane int) {
	l.endBatch(lane)
	ln := l.lanes.get(lane)
	ln.started = false
	//: every deferred answer of the batch now holds what it will send.
	if len(ln.deferred) == 0 {
		//: nothing to complete.
		return
	}
	now := l.cfg.clk.Now()
	//: in the order they were deferred.
	for i := range ln.deferred {
		d := &ln.deferred[i]
		d.rec.Stage = corenet.HTTP1AccessCompleted
		d.rec.BodyLen = len(d.res.Body)
		d.rec.Status = completionStatus(d.res)
		d.rec.Elapsed = corenet.DurationValue(now.Sub(d.at))
		l.emit(&d.rec)
		d.res = nil
	}
	ln.deferred = ln.deferred[:0]
}

// completionStatus is the status a deferred answer leaves with: the 500 the
// adapter writes for one left deferred, or the handler's, zero meaning 200.
func completionStatus(res *corenet.HTTP1ResponseValue) int {
	//: left deferred past EndBatch: the adapter answers 500.
	if res.Defer {
		//: the status the wire will carry.
		return statusInternal
	}
	//: what the handler set.
	return orDefault(res.Status, statusOK)
}

// Trace returns the link of an http1 chain that reads a request's W3C
// traceparent, once, parsed with no allocation, into HTTP1RequestValue.Trace —
// a malformed one is left invalid and the request served (§4.3) — and writes
// a version-00 traceparent of the same trace on the answer when the request
// had one, as HTTP1ResponseValue.TraceLine: a Pending answer, written from the
// worker's own response value, does not carry it. It samples and exports
// nothing: what to do with the value is the handler's.
func Trace() corenet.Middleware[corenet.HTTP1Handler] {
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the batcher found once, never per batch.
		return &TraceLayer{link: newLink(next)}
	}
}

// TraceLayer is the link Trace builds: the layer it wraps around the next
// handler.
type TraceLayer struct {
	link
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *TraceLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	//: a request carrying the field: read once, an invalid value left invalid.
	if value, ok := req.Field(traceFieldName); ok {
		req.Trace, _ = coretrace.ParseTraceParentBytes(value)
	}
	//: the answer names the trace the request joined.
	if req.Trace.IsValid() {
		line := append(res.TraceLine[:0], traceLineStart...)
		line = coretrace.AppendTraceParent(line, req.Trace)
		addHeader(res, append(line, traceLineEnd...))
	}
	l.next.ServeHTTP1(req, res)
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *TraceLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// RequestIDOption tunes the RequestID link: RequestIDMint is the one option a
// link is built with.
type RequestIDOption struct {
	// mint makes the link mint every identifier.
	mint bool
}

// RequestIDMint makes the RequestID link mint every identifier and read nothing
// the client sent: for an edge that must not trust a caller's id.
func RequestIDMint() RequestIDOption {
	//: the one option.
	return RequestIDOption{mint: true}
}

// RequestID returns the link of an http1 chain that gives every request a
// correlation identifier: the X-Request-Id field the client sent, when it is 1
// to corenet.HTTP1MaxRequestID bytes of visible ASCII, and otherwise — absent,
// longer or holding any other byte — one minted from HTTP1RequestValue.ID as
// sixteen lowercase hexadecimal digits, the request being served either way.
// It copies the identifier into HTTP1RequestValue.RequestID, by value, and onto
// the answer, which the codec writes as X-Request-Id after the handler's Header,
// a deferred or a pending one included. The access record of a Log link in the
// same chain carries it. RequestIDMint makes it mint always.
func RequestID(opts ...RequestIDOption) corenet.Middleware[corenet.HTTP1Handler] {
	mint := false
	//: one option of consequence: any of them asking is enough.
	for _, o := range opts {
		mint = mint || o.mint
	}
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the batcher found once, never per batch.
		return &RequestIDLayer{link: newLink(next), mint: mint}
	}
}

// RequestIDLayer is the link RequestID builds: the layer it wraps around the
// next handler.
type RequestIDLayer struct {
	link
	// mint makes the layer ignore the client's identifier.
	mint bool
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *RequestIDLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	//: the client's, when one is asked for and valid; anything else is replaced.
	if !l.accepted(req) {
		mintRequestID(req)
	}
	res.RequestID = req.RequestID
	l.next.ServeHTTP1(req, res)
}

// accepted copies the client's identifier into req, and reports whether there
// was one the link may keep.
func (l *RequestIDLayer) accepted(req *corenet.HTTP1RequestValue) bool {
	//: an edge reads nothing of what the client sent.
	if l.mint {
		//: minted.
		return false
	}
	v, ok := req.Field(requestIDFieldName)
	//: Set is the gate: empty, too long or an invisible byte stays absent.
	return ok && req.RequestID.Set(v)
}

// mintRequestID writes req.ID as sixteen lowercase hexadecimal digits into
// req.RequestID, the digits of the value being the only input: no clock, no
// randomness, nothing shared between lanes.
func mintRequestID(req *corenet.HTTP1RequestValue) {
	var digits [mintedIDLen]byte
	v := req.ID
	//: from the last digit to the first.
	for i := mintedIDLen - 1; i >= 0; i-- {
		digits[i] = lowerHex[v&nibbleMask]
		v >>= nibbleBits
	}
	//: sixteen visible bytes: always accepted.
	req.RequestID.Set(digits[:])
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *RequestIDLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// rateKeyKind names what a RateKey keys on.
type rateKeyKind uint8

// rateLane is what RateLimit keeps per lane.
type rateLane struct {
	// key is the scratch a route's or a lane's key is written into, in the state
	// so its slice costs nothing on the interface call.
	key [routeKeyLen]byte
	// now is the clock's reading of this batch, in nanoseconds since the epoch.
	now int64
	// fresh says whether now is this batch's.
	fresh bool
}

// RateKey is what the RateLimit link keys a request on, built by RateKeySubject,
// RateKeyRoute, RateKeyLane or RateKeyField; the zero RateKey is refused.
type RateKey struct {
	// kind is the kind of key, zero for none.
	kind rateKeyKind
	// field is the request field RateKeyField names.
	field string
}

// RateKeySubject keys requests on the subject a Bearer link in the chain verified.
func RateKeySubject() RateKey {
	//: a key on the Auth value.
	return RateKey{kind: rateKeySubject}
}

// RateKeyRoute keys requests on the route a Routes link in the chain matched.
func RateKeyRoute() RateKey {
	//: a key on the route.
	return RateKey{kind: rateKeyRoute}
}

// RateKeyLane keys requests on the lane that serves them.
func RateKeyLane() RateKey {
	//: a key on the lane.
	return RateKey{kind: rateKeyLane}
}

// RateKeyField keys requests on the value of the request field name. A field
// is only as trustworthy as whatever sets it in front of this server.
func RateKeyField(name string) RateKey {
	//: a key on a field.
	return RateKey{kind: rateKeyField, field: name}
}

// RateOption tunes the RateLimit link: the value RateExempt or RateClock returns,
// judged when the link is built.
type RateOption struct {
	// apply folds the option into the link's configuration, or refuses it.
	apply func(*rateConfig) error
}

// rateConfig is every option of a RateLimit link, checked.
type rateConfig struct {
	// exempt are the routes never judged.
	exempt []int
	// clk is the clock read once per batch.
	clk clock.Clock
}

// RateExempt names routes the RateLimit link never judges.
func RateExempt(ids ...int) RateOption {
	kept := slices.Clone(ids)
	//: judged when the link is built.
	return RateOption{apply: func(c *rateConfig) error {
		//: each named route.
		for _, id := range kept {
			//: zero is "no route": exempting it would exempt every unrouted request.
			if id <= 0 {
				//: refused.
				return misconfigured("RateLimit", "exempt")
			}
		}
		c.exempt = append(c.exempt, kept...)
		//: kept.
		return nil
	}}
}

// RateClock is the clock the RateLimit link reads, once per batch; clock.System by default.
func RateClock(c clock.Clock) RateOption {
	//: judged when the link is built.
	return RateOption{apply: func(cfg *rateConfig) error {
		//: a clock is what every batch reads.
		if c == nil {
			//: refused.
			return misconfigured("RateLimit", "clock")
		}
		cfg.clk = c
		//: kept.
		return nil
	}}
}

// RateLimit returns the link of an http1 chain that refuses a request past its
// key's budget, by l: a 429 with Retry-After and Connection: close, going no
// further. It belongs after the links that give the key — Bearer for a subject,
// Routes for a route — and before the handler. RateExempt names routes it never
// judges. A nil limiter, a RateKey built by none of RateKeySubject, RateKeyRoute,
// RateKeyLane or RateKeyField, and an option it cannot honour are refused with
// corenet.HTTP1Misconfigured. A request carrying no key — no subject or no such
// field — takes the empty key, and every such request shares one bucket.
func RateLimit(l corenet.HTTP1Limiter, key RateKey, opts ...RateOption) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: a link that limits by nothing.
	if l == nil {
		//: refused.
		return nil, misconfigured("RateLimit", "limiter")
	}
	//: a key the link cannot read.
	if key.kind == rateKeyNone || (key.kind == rateKeyField && !token(key.field)) {
		//: refused.
		return nil, misconfigured("RateLimit", "key")
	}
	cfg := &rateConfig{clk: clock.System}
	//: each option in turn.
	for _, o := range opts {
		//: a refused value is named.
		if err := o.apply(cfg); err != nil {
			//: refused.
			return nil, err
		}
	}
	//: one layer per handler it wraps.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: the configuration is shared, read only.
		return &RateLimitLayer{link: newLink(next), l: l, key: key, cfg: cfg, epoch: cfg.clk.Now()}
	}, nil
}

// RateLimitLayer is the link RateLimit builds: the layer it wraps around the next
// handler.
type RateLimitLayer struct {
	link
	// l decides.
	l corenet.HTTP1Limiter
	// key says what a request is keyed on.
	key RateKey
	// cfg holds the link's options, read only.
	cfg *rateConfig
	// epoch is the link's zero instant: now is a monotonic count from it.
	epoch time.Time
	// lanes holds each lane's batch stamp and scratch.
	lanes laneTable[rateLane]
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *RateLimitLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	//: an exempt route is never judged.
	if slices.Contains(l.cfg.exempt, req.Route) {
		l.next.ServeHTTP1(req, res)
		//: not judged.
		return
	}
	lane := l.lanes.get(req.Lane)
	//: the first request of a batch reads the clock: the others share it.
	if !lane.fresh {
		lane.now, lane.fresh = l.cfg.clk.Now().Sub(l.epoch).Nanoseconds(), true
	}
	ok, after := l.l.Allow(l.keyOf(req, lane), lane.now)
	//: over budget: refused here, and no handler runs.
	if !ok {
		res.Status, res.RetryAfter, res.Close = statusTooManyRequests, after, true
		//: refused.
		return
	}
	l.next.ServeHTTP1(req, res)
}

// keyOf returns the bytes req is keyed on: a subject's or a field's own bytes, or
// the lane's scratch for a number.
func (l *RateLimitLayer) keyOf(req *corenet.HTTP1RequestValue, lane *rateLane) []byte {
	//: by what the link was built to key on.
	switch l.key.kind {
	case rateKeySubject:
		//: empty for an unauthenticated request.
		return req.Auth.Subject()
	case rateKeyField:
		v, _ := req.Field(l.key.field)
		//: empty when absent.
		return v
	case rateKeyRoute:
		//: the route number, in the lane's scratch.
		return appendBE(&lane.key, uint64(req.Route))
	default:
		//: the lane, the only other kind construction lets through.
		return appendBE(&lane.key, uint64(req.Lane))
	}
}

// appendBE writes v big-endian into buf and returns it as a slice.
func appendBE(buf *[routeKeyLen]byte, v uint64) []byte {
	//: from the low byte up.
	for i := routeKeyLen - 1; i >= 0; i-- {
		buf[i] = byte(v)
		v >>= byteBits
	}
	//: the whole array.
	return buf[:]
}

// EndBatch implements corenet.HTTP1Batcher: it ends the next handler's
// batch when the next handler is a batcher, so a chain defers answers as
// its handler does.
func (l *RateLimitLayer) EndBatch(lane int) {
	l.finishRateBatch(lane)
}

// finishRateBatch ends the next handler's batch and forgets the batch's stamp.
func (l *RateLimitLayer) finishRateBatch(lane int) {
	l.endBatch(lane)
	l.lanes.get(lane).fresh = false
}

// FilesLayer is the link Files builds: the layer it wraps around the next
// handler.
type FilesLayer struct {
	link
	// route is the route it serves; tag its jobs' top bits.
	route int
	tag   uint64
	// fsys is the tree.
	fsys fs.FS
	// immutable and spa are the static tree's policies.
	immutable func(name string) bool
	spa       bool
	// base holds every answer's fields; allow base and a 405's.
	base  []byte
	allow []byte
	// nextWorker and nextCanceller are what the handler behind it answers
	// jobs with, nil when it does not.
	nextWorker    corenet.HTTP1Worker
	nextCanceller jobCanceller
	// lanes holds each lane's job table, built on its first request, replaced
	// and never written in place under mu; a lane reads it without the lock.
	lanes atomic.Pointer[[]*filesLane]
	mu    sync.Mutex
}
