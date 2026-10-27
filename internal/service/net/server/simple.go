package server

import (
	"net/http"
	"reflect"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

const (
	// entryHTTP, entryTCP and entryUDP name the simple API's entries in an
	// error's "entry" field.
	entryHTTP string = "HTTP"
	// entryTCP is Server.TCP.
	entryTCP string = "TCP"
	// entryUDP is Server.UDP.
	entryUDP string = "UDP"
)

// HTTP declares a group serving h over HTTP on addr, a TCP address — the
// simple API (ADR 0187 §D6, ADR 0197). h is an http.Handler, served by
// net/http on the goroutine engine, or an HTTP1Handler, served by the http1
// profile's codec on the loop engine; a handler that is both needs Engine to
// choose. The group is named "tcp+" + addr unless Name says otherwise, and
// opts apply as they do to Group. Without TLS, Plaintext is required (ADR
// 0187 §D7). Every refusal is reported by Start: a nil or typed nil handler
// (HandlerMissing), both interfaces without Engine (HandlerAmbiguous), an
// Engine the handler cannot run on (EngineUnsupported), a type no engine of
// HTTP serves (HandlerUnsupported), Plaintext with TLS (PlaintextWithTLS),
// neither (PlaintextRequired).
func (s *Server) HTTP(addr string, h any, opts ...GroupOption) *StreamGroup {
	g := s.simpleGroup("tcp", addr, opts)
	//: a refusal is the group's, reported by Start.
	if err := g.mountHTTPEntry(h); err != nil {
		g.recordErr(err)
	}
	//: returned for chaining.
	return g
}

// TCP declares a group serving h on addr, a TCP address — the simple API.
// h is a ConnHandler, one goroutine per connection, or a LoopHandler, on the
// loop engine; a handler that is both needs Engine to choose. An HTTP
// handler is refused with HandlerUnsupported: HTTP serves it. The group is
// named "tcp+" + addr unless Name says otherwise; the other refusals are
// HTTP's.
func (s *Server) TCP(addr string, h any, opts ...GroupOption) *StreamGroup {
	g := s.simpleGroup("tcp", addr, opts)
	//: a refusal is the group's, reported by Start.
	if err := g.mountTCPEntry(h); err != nil {
		g.recordErr(err)
	}
	//: returned for chaining.
	return g
}

// UDP declares a datagram group serving h on addr, a UDP address — the
// simple API. h is a PacketHandler; anything else, a LoopHandler included, is
// refused with HandlerUnsupported, and a nil or typed nil handler with
// HandlerMissing. The group is named "udp+" + addr unless Name says
// otherwise; every option the datagram engine does not honour is refused, as
// on any datagram group.
func (s *Server) UDP(addr string, h any, opts ...GroupOption) *PacketGroup {
	g := s.PacketGroup("", append([]GroupOption{Listen("udp", addr), defaultName("udp+" + addr)}, opts...)...)
	//: a refusal is the server's, reported by Start.
	if err := g.mountUDPEntry(h); err != nil {
		s.recordGroupErr(err)
	}
	//: returned for chaining.
	return g
}

// mountUDPEntry serves h as UDP's entry: a PacketHandler, the one type a
// datagram engine serves.
func (g *PacketGroup) mountUDPEntry(h any) error {
	//: a handler that is not there.
	if isNil(h) {
		//: refused.
		return errs.Wrap(corenet.HandlerMissing, errs.WrapParams{},
			errs.String("group", g.name), errs.String("entry", entryUDP))
	}
	ph, ok := h.(corenet.PacketHandler)
	//: any other type.
	if !ok {
		//: refused, the type named.
		return unsupported(g.name, entryUDP, h)
	}
	g.Handle(ph)
	//: mounted.
	return nil
}

// simpleGroup declares the stream group of a simple-API entry: listening on
// network addr, named network+"+"+addr unless Name names it.
func (s *Server) simpleGroup(network, addr string, opts []GroupOption) *StreamGroup {
	//: the address first, the default name before the caller's options.
	return s.Group("", append([]GroupOption{Listen(network, addr), defaultName(network + "+" + addr)}, opts...)...)
}

// defaultName names a group declared with no name, unless a later Name
// option names it: it is a declaration's own, recording no kind.
func defaultName(name string) GroupOption {
	//: before the caller's options, so Name still decides.
	return func(g *StreamGroup) {
		g.name, g.defaultName = name, name
	}
}

// recordGroupErr records err as the server's declaration error.
func (s *Server) recordGroupErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the first one is reported.
	s.recordDeclError(err)
}

// recordErr records err as the group's declaration error, the first kept.
func (g *StreamGroup) recordErr(err error) {
	//: the first explains the rest.
	if g.declErr == nil {
		g.declErr = err
	}
}

// mountHTTPEntry serves h as HTTP's entry: net/http for an http.Handler, the
// http1 codec on the loop for an HTTP1Handler, Engine choosing for a
// handler that is both.
func (g *StreamGroup) mountHTTPEntry(h any) error {
	//: a handler that is not there.
	if isNil(h) {
		//: refused.
		return g.missing(entryHTTP)
	}
	std, isStd := h.(http.Handler)
	h1, isH1 := h.(corenet.HTTP1Handler)
	//: by the engine the handler runs on.
	switch g.pickEngine(isStd, isH1) {
	case corenet.EngineGoroutine:
		g.HandleHTTP(std)
	case corenet.EngineLoop:
		g.HandleLoop(http1.HandleLoop(h1, http1.Parser{}))
	default:
		//: why none was picked.
		return g.pickRefusal(entryHTTP, h, isStd, isH1)
	}
	//: mounted.
	return nil
}

// mountTCPEntry serves h as TCP's entry: a goroutine per connection for a
// ConnHandler, the loop for a LoopHandler.
func (g *StreamGroup) mountTCPEntry(h any) error {
	//: a handler that is not there.
	if isNil(h) {
		//: refused.
		return g.missing(entryTCP)
	}
	//: an HTTP handler is HTTP's.
	if isHTTPHandler(h) {
		//: refused, the entry named.
		return errs.Wrap(corenet.HandlerUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("entry", entryTCP), errs.String("serve", entryHTTP))
	}
	conn, isConn := h.(corenet.ConnHandler)
	lh, isLoop := h.(corenet.LoopHandler)
	//: by the engine the handler runs on.
	switch g.pickEngine(isConn, isLoop) {
	case corenet.EngineGoroutine:
		g.Handle(conn)
	case corenet.EngineLoop:
		g.HandleLoop(lh)
	default:
		//: why none was picked.
		return g.pickRefusal(entryTCP, h, isConn, isLoop)
	}
	//: mounted.
	return nil
}

// pickEngine chooses the engine a handler that is a goroutine one, a loop
// one or both runs on: the one it is, or Engine's for both; EngineAuto when
// none fits.
func (g *StreamGroup) pickEngine(goroutine, loop bool) corenet.EngineValue {
	e := corenet.EngineAuto
	//: an Engine given decides between the two.
	if g.kinds.has(corenet.OptionEngine) {
		e = g.engine
	}
	//: the goroutine engine, named or the only one the handler runs on.
	if goroutine && fits(e, corenet.EngineGoroutine, loop) {
		//: chosen.
		return corenet.EngineGoroutine
	}
	//: the loop engine, likewise.
	if loop && fits(e, corenet.EngineLoop, goroutine) {
		//: chosen.
		return corenet.EngineLoop
	}
	//: nothing fits.
	return corenet.EngineAuto
}

// fits reports whether engine want may serve a handler under Engine e: e
// names it, or e is auto and the handler runs on no other.
func fits(e, want corenet.EngineValue, other bool) bool {
	//: named, or the only one.
	return e == want || (e == corenet.EngineAuto && !other)
}

// pickRefusal says why pickEngine found no engine: both kinds without Engine,
// an Engine neither kind runs on, or a type the entry does not serve.
func (g *StreamGroup) pickRefusal(entry string, h any, goroutine, loop bool) error {
	//: by what the handler is.
	switch {
	case goroutine && loop:
		//: Engine chooses.
		return errs.Wrap(corenet.HandlerAmbiguous, errs.WrapParams{},
			errs.String("group", g.name), errs.String("entry", entry), errs.String("type", typeName(h)))
	case goroutine || loop:
		//: the handler cannot run on the Engine given.
		return errs.Wrap(corenet.EngineUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("engine", g.engine.String()))
	default:
		//: no engine of the entry serves it.
		return unsupported(g.name, entry, h)
	}
}

// missing is HandlerMissing for a nil handler given to entry.
func (g *StreamGroup) missing(entry string) error {
	//: by name.
	return errs.Wrap(corenet.HandlerMissing, errs.WrapParams{},
		errs.String("group", g.name), errs.String("entry", entry))
}

// unsupported is HandlerUnsupported for a type entry serves on no engine.
func unsupported(group, entry string, h any) error {
	//: the type named.
	return errs.Wrap(corenet.HandlerUnsupported, errs.WrapParams{},
		errs.String("group", group), errs.String("entry", entry), errs.String("type", typeName(h)))
}

// isHTTPHandler reports whether h is a handler HTTP serves.
func isHTTPHandler(h any) bool {
	_, std := h.(http.Handler)
	_, h1 := h.(corenet.HTTP1Handler)
	//: either kind.
	return std || h1
}

// isNil reports whether h is nil, or a typed nil a method call would
// dereference.
func isNil(h any) bool {
	//: untyped nil.
	if h == nil {
		//: nil.
		return true
	}
	v := reflect.ValueOf(h)
	//: the kinds that can be nil.
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Interface, reflect.Slice, reflect.Chan:
		//: a typed nil.
		return v.IsNil()
	default:
		//: a value.
		return false
	}
}

// typeName is h's dynamic type, for an error field.
func typeName(h any) string {
	//: as reflect spells it.
	return reflect.TypeOf(h).String()
}

// servesHTTP reports whether the group speaks HTTP: net/http's adapter, or
// the http1 codec as its loop handler.
func (g *StreamGroup) servesHTTP() bool {
	_, h1 := g.loopHandler.(*http1.LoopHandler)
	//: either engine.
	return g.httpAdapter != nil || h1
}

// plaintextRefusal is ADR 0187 §D7: an HTTP group given no TLS must say
// Plaintext.
func (g *StreamGroup) plaintextRefusal() error {
	//: TLS, Plaintext, or no HTTP at all.
	if !g.servesHTTP() || g.kinds.has(corenet.OptionTLS) || g.kinds.has(corenet.OptionPlaintext) {
		//: nothing to require.
		return nil
	}
	//: refused by name.
	return errs.Wrap(corenet.PlaintextRequired, errs.WrapParams{}, errs.String("group", g.name))
}
