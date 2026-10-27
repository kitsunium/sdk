package server

import (
	"net/http"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// StreamGroup is a set of listeners sharing one handler, one middleware chain
// and one policy. Grouping exists so a server can expose the same handler on a
// TCP port and a Unix socket — or two ports with different TLS identities —
// without the caller assembling the wiring twice.
type StreamGroup struct {
	// name identifies the group in logs, metrics and State.
	name string
	// addrs are the addresses to bind.
	addrs []corenet.AddressValue
	// handler serves each accepted connection.
	handler corenet.ConnHandler
	// middlewares decorate the handler, outermost first.
	middlewares []corenet.Middleware[corenet.ConnHandler]
	// identity turns the group's listeners into TLS or mTLS listeners.
	identity corenet.IdentityValue
	// limits bounds what the group may consume.
	limits corenet.LimitsValue
	// timeouts bounds each phase of a connection.
	timeouts corenet.TimeoutsValue
	// httpAdapter is set when the group serves an http.Handler, so the engine
	// can shut the embedded http.Server down with the rest of the group.
	httpAdapter *httpAdapter
	// http holds the bounds only an HTTP group has: the header phase's own
	// deadline and the header size cap. They live here rather than in
	// core/net's Timeouts and Limits, which describe every protocol a group
	// can serve, and "a request header" is not a word in most of them.
	http httpBounds
	// protos is the Protocols option's list, as given.
	protos []string
	// httpProtos is what the group serves, computed at Start from protos and
	// the identity (protocolsRefusal).
	httpProtos httpProtocols
	// limiter caps concurrent connections; nil when the group has no ceiling.
	limiter *connLimiter
	// adopt names inherited sockets to take over instead of binding.
	adopt []string
	// admission counts connections against their address before their
	// handshake; nil when the group has none (ADR 0174).
	admission *Admission
	// declErr is the group's first declaration error — a second handler,
	// HandlerAlreadySet — surfaced by Start like the server's own (ADR 0186
	// §D1). The group records it because a handler is declared on the group,
	// after Group has returned and released the server's lock.
	declErr error
	// declared records that a handler was declared, even a nil one, so a
	// second declaration is caught whatever the first one passed.
	declared bool
	// loop is the Loop option's value; loopSet records that the option was
	// given, a zero value included, so Start can refuse it on a group whose
	// handler is not a loop handler (ADR 0186 §D1).
	loop    corenet.LoopValue
	loopSet bool
	// loopHandler is the group's loop handler, set by HandleLoop; a group
	// with one is served by the loop engine (ADR 0186 §D1).
	loopHandler corenet.LoopHandler
	// loopMiddlewares decorate the loop handler, outermost first (UseLoop).
	loopMiddlewares []corenet.Middleware[corenet.LoopHandler]
	// loopBusy is the chain's offloader's Pending, nil when nothing offloads:
	// what a draining loop asks before it closes a settled connection (R3).
	loopBusy func(lane int, ref corenet.LoopConnRef) bool
	// loopChain is loopHandler wrapped in loopMiddlewares, built and checked
	// once at Start; the loop engine serves it.
	loopChain corenet.LoopHandler
	// kinds is the set of option kinds the group was given, a zero value
	// included, so a refusal reads what was posed rather than guessing from a
	// non-zero field (ADR 0187).
	kinds optionSet
	// engine is the Engine option's value; it is read only when kinds holds
	// OptionEngine.
	engine corenet.EngineValue
	// optName is the Name option's value; it is read only when kinds holds
	// OptionName.
	optName string
}

// Name returns the group's name.
func (g *StreamGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// Handle sets the group's handler. A group declares one handler, once: a
// second declaration on the same group — Handle, HandleFunc, HandleHTTP or
// HandleLoop, in any order — does not replace the first, and Start returns
// HandlerAlreadySet naming the group (ADR 0186 §D1).
func (g *StreamGroup) Handle(h corenet.ConnHandler) *StreamGroup {
	//: a second declaration keeps the first handler and is reported by Start.
	if !g.claimHandler() {
		//: returned for chaining; the recorded error is what Start reports.
		return g
	}
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// claimHandler reports whether the group may take its handler now, and
// records HandlerAlreadySet when it already has one. It is the one check every
// declaration takes, so Handle, HandleFunc and HandleHTTP cannot disagree on
// what a second declaration means.
func (g *StreamGroup) claimHandler() bool {
	//: the first declaration claims the group.
	if !g.declared {
		g.declared = true
		//: nothing declared yet.
		return true
	}
	//: the first error is the one that explains the others.
	if g.declErr == nil {
		g.declErr = errs.Wrap(corenet.HandlerAlreadySet, errs.WrapParams{},
			errs.String("group", g.name))
	}
	//: the group keeps the handler it was given first.
	return false
}

// HandleFunc sets the group's handler from a plain function. It exists because
// the overwhelmingly common case is one function, and making that case require
// a named type would be the papercut that decides whether the API feels light.
func (g *StreamGroup) HandleFunc(f corenet.ConnHandlerFunc) *StreamGroup {
	//: the func adapter already satisfies the port.
	return g.Handle(f)
}

// HandleHTTP serves an http.Handler over this group's listeners.
//
// The group keeps its own limits, TLS identity and drain; net/http only does
// the protocol. That split is ADR 0029 D3: reimplementing HTTP would mean
// owning request smuggling defences, HTTP/2 flow control and HPACK to lose,
// not gain, throughput.
func (g *StreamGroup) HandleHTTP(h http.Handler) *StreamGroup {
	//: the body lives in mountHTTP so this method stays within the inliner's
	//: budget at every call site.
	return g.mountHTTP(h)
}

// mountHTTP installs the net/http adapter as the group's handler, unless the
// group already has one.
func (g *StreamGroup) mountHTTP(h http.Handler) *StreamGroup {
	//: checked before the adapter is installed, so a second declaration never
	//: leaves an HTTP adapter beside the first handler.
	if !g.claimHandler() {
		//: returned for chaining; the recorded error is what Start reports.
		return g
	}
	adapter := newHTTPAdapter(h)
	g.httpAdapter = adapter
	g.handler = adapter
	//: returned for chaining; the adapter IS a ConnHandler, so the rest of the
	//: engine is unchanged.
	return g
}

// tracksCloses reports whether the group's sockets must report their own
// Close: a handler can take one over — the group serves HTTP — while a ceiling
// still has to count it. It is read at bind time, after the limiter is built.
func (g *StreamGroup) tracksCloses() bool {
	//: every other group either cannot hijack or has no slot to hold.
	return g.httpAdapter != nil && g.limiter != nil
}

// Use appends middlewares, outermost first.
func (g *StreamGroup) Use(middlewares ...corenet.Middleware[corenet.ConnHandler]) *StreamGroup {
	g.middlewares = append(g.middlewares, middlewares...)
	//: posed, even with no middleware: a loop group refuses the call itself.
	g.kinds.add(corenet.OptionMiddleware)
	//: returned for chaining.
	return g
}

// UseLoop appends loop middlewares, outermost first, and records the
// OptionLoopMiddleware kind on every call, even with none. A loop group
// wraps its handler in them at Start, after checking that each layer is a
// corenet.LoopLayered whose Next is what it was given and that every one of
// the eight callbacks, sent through a probe chain, reaches the probe: a layer
// that fails either check fails Start with LoopMiddlewareInvalid. The chain
// honours an option when one of its layers or the handler does. A group no
// loop handler serves refuses UseLoop with LoopOptionUnsupported.
func (g *StreamGroup) UseLoop(middlewares ...corenet.Middleware[corenet.LoopHandler]) *StreamGroup {
	g.loopMiddlewares = append(g.loopMiddlewares, middlewares...)
	//: posed, even with no middleware: a goroutine group refuses the call itself.
	g.kinds.add(corenet.OptionLoopMiddleware)
	//: returned for chaining.
	return g
}

// resolved returns the handler with the group's middlewares applied.
func (g *StreamGroup) resolved() corenet.ConnHandler {
	//: the adapter learns the group's deadlines here, at Start, which is the
	//: first moment every option is certainly applied and still before any
	//: accept loop exists to read them.
	if g.httpAdapter != nil {
		g.httpAdapter.timeouts = g.timeouts
		g.httpAdapter.bounds = g.http
		g.httpAdapter.protocols = &g.httpProtos.accepted
	}
	//: composed once at Start rather than per connection, so the middleware
	//: chain costs nothing on the hot path.
	chained := corenet.Chain(g.handler, g.middlewares...)
	//: a group with no TLS identity has no negotiation to bound, so it is not
	//: wrapped at all and pays nothing.
	if g.identity.IsZero() {
		//: plaintext: straight to the handler chain.
		return chained
	}
	//: wrapped once per group, so bounding the handshake costs no allocation
	//: per connection.
	return handshakeHandler{next: chained, timeouts: g.timeouts}
}
