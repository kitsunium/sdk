package server

import (
	"context"
	stdnet "net"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// loopEngine is a running loop group, whatever its backend: the leaf's native
// loops or the portable backend below.
type loopEngine interface {
	// Shutdown stops accepting and drains until ctx is done.
	Shutdown(ctx context.Context) error
	// Close closes everything now.
	Close() error
	// State reports the group.
	State() corenet.LoopStateValue
}

// HandleLoop serves the group on the loop engine (ADR 0186): h is called on
// the loop's own thread for every connection of the group, with the bytes
// each one receives, instead of a goroutine per connection. It is opt-in, and
// plaintext only.
//
// The group keeps the server's lifecycle — the bind, the adoption of
// inherited sockets, MaxConns, the drain, State — and Loop(v) sizes and
// places the engine: io_uring after a probe of the running kernel, then epoll,
// on Linux, and the portable backend, the default engine's accept path
// serialised onto one handler goroutine, everywhere else. Start refuses a loop
// group given TLS with LoopTLSUnsupported, one given Use or an option neither
// the engine (IdleTimeout, MaxConns, Adopt) nor h, through
// LoopOptionHonourer, honours with LoopOptionUnsupported, and an explicit
// backend the platform or the kernel cannot run with UnsupportedPlatform or
// LoopBackendUnavailable.
//
// A callback of h runs on the loop's thread and must not block it: a blocking
// call stalls every connection of its loop. h is declared once, like every
// handler: a second declaration of any kind makes Start return
// HandlerAlreadySet.
func (g *StreamGroup) HandleLoop(h corenet.LoopHandler) *StreamGroup {
	//: one handler per group, whatever kind.
	if !g.claimHandler() {
		//: returned for chaining; Start reports the second declaration.
		return g
	}
	g.loopHandler = h
	//: returned for chaining.
	return g
}

// loopRefusal judges a loop group's options at Start, once its handler is
// known: TLS is never served (LoopTLSUnsupported), Use never applies, and
// every other option set is refused unless the loop engine or the handler
// honours it (LoopOptionUnsupported, naming it).
func (g *StreamGroup) loopRefusal() error {
	//: plaintext only (ADR 0186 §D2).
	if !g.identity.IsZero() {
		//: refused by name.
		return errs.Wrap(corenet.LoopTLSUnsupported, errs.WrapParams{}, errs.String("group", g.name))
	}
	honours, _ := g.loopHandler.(corenet.LoopOptionHonourer)
	//: every option the group was given, in a fixed order.
	for _, k := range g.loopOptionsSet() {
		//: the engine's own, or the handler's.
		if loopHonours(k) || (honours != nil && k != corenet.OptionMiddleware && honours.Honours(k)) {
			continue
		}
		//: the first option nobody honours.
		return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("option", k.String()))
	}
	//: every option is honoured.
	return nil
}

// loopHonours reports the options the loop engine honours itself (ADR 0186
// §D4): IdleTimeout, MaxConns and Adopt. Listen and Loop are not options a
// loop judges; the drain is the server's.
func loopHonours(k corenet.OptionKind) bool {
	//: the three the engine applies.
	return k == corenet.OptionIdleTimeout || k == corenet.OptionMaxConns || k == corenet.OptionAdopt
}

// loopOptionsSet lists the kinds of every option set on the group.
func (g *StreamGroup) loopOptionsSet() []corenet.OptionKind {
	var set []corenet.OptionKind
	add := func(on bool, k corenet.OptionKind) {
		//: only what was given.
		if on {
			set = append(set, k)
		}
	}
	add(len(g.middlewares) > 0, corenet.OptionMiddleware)
	add(g.timeouts.Read > 0, corenet.OptionReadTimeout)
	add(g.timeouts.Write > 0, corenet.OptionWriteTimeout)
	add(g.timeouts.Idle > 0, corenet.OptionIdleTimeout)
	add(g.http.readHeaderTimeout > 0, corenet.OptionReadHeaderTimeout)
	add(g.http.maxHeaderBytes > 0, corenet.OptionMaxHeaderBytes)
	add(g.timeouts.Handshake > 0, corenet.OptionHandshakeTimeout)
	add(g.limits.ReadBufferSize > 0, corenet.OptionReadBufferSize)
	add(g.limits.MaxPacketSize > 0, corenet.OptionMaxPacketSize)
	add(g.limits.BatchSize > 0, corenet.OptionBatchSize)
	add(g.limits.Shards > 0, corenet.OptionShards)
	add(g.limits.MaxConns > 0, corenet.OptionMaxConns)
	add(len(g.adopt) > 0, corenet.OptionAdopt)
	add(g.admission != nil, corenet.OptionPreHandshake)
	//: in the constant block's order.
	return set
}

// bindLoopGroup binds or adopts a loop group's listeners, registers them as
// the server's like any other, and starts the group's engine on them.
func (s *Server) bindLoopGroup(ctx context.Context, group *StreamGroup) error {
	var lns []stdnet.Listener
	//: inherited sockets first, as for every group.
	for _, name := range group.adopt {
		adopted, err := s.adoptStream(name)
		//: an activation mismatch aborts startup.
		if err != nil {
			//: the error names the socket.
			return err
		}
		lns = append(lns, adopted...)
		s.registerLoopListeners(group, adopted, true)
	}
	//: then every address, one listener each: the loop shards inside.
	for _, addr := range group.addrs {
		ln, err := listen(ctx, addr, corenet.IdentityValue{}, false, false)
		//: the address could not be bound.
		if err != nil {
			//: Start unwinds what is bound.
			return err
		}
		lns = append(lns, ln)
		s.registerLoopListeners(group, []stdnet.Listener{ln}, false)
	}
	eng, err := startLoopEngine(group, lns)
	//: the backend refused.
	if err != nil {
		//: Start unwinds the listeners.
		return err
	}
	s.mu.Lock()
	s.loopEngines = append(s.loopEngines, eng)
	s.mu.Unlock()
	//: serving.
	return nil
}

// registerLoopListeners records a loop group's listeners, so Close and
// Shutdown close them and State reports them, as for every group.
func (s *Server) registerLoopListeners(group *StreamGroup, lns []stdnet.Listener, adopted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: one row and one bound listener each.
	for _, ln := range lns {
		addr := corenet.AddressValue{Network: ln.Addr().Network(), Addr: ln.Addr().String()}
		s.listeners = append(s.listeners, &boundListener{group: group.name, addr: addr, ln: ln})
		s.states = append(s.states, corenet.ListenerStateValue{
			Group: group.name, Address: ln.Addr().String(), Network: addr.Network, Shards: 1, Adopted: adopted,
		})
	}
}

// loopStates reports every loop group.
func (s *Server) loopStates() []corenet.LoopStateValue {
	s.mu.RLock()
	engines := s.loopEngines
	s.mu.RUnlock()
	var out []corenet.LoopStateValue
	//: in start order.
	for _, e := range engines {
		out = append(out, e.State())
	}
	//: nil while no loop group runs.
	return out
}

// shutdownLoops drains every loop group within ctx.
func (s *Server) shutdownLoops(ctx context.Context) error {
	s.mu.RLock()
	engines := s.loopEngines
	s.mu.RUnlock()
	var first error
	//: each drains on its own threads; the budget is shared.
	for _, e := range engines {
		//: the first overrun is the one reported.
		if err := e.Shutdown(ctx); err != nil && first == nil {
			first = err
		}
	}
	//: drained, or the first overrun.
	return first
}

// closeLoops closes every loop group now.
func (s *Server) closeLoops() {
	s.mu.RLock()
	engines := s.loopEngines
	s.mu.RUnlock()
	//: best effort, like every Close.
	for _, e := range engines {
		swallowErr(e.Close())
	}
}

// loopIdle is the group's IdleTimeout as the loop takes it.
func loopIdle(group *StreamGroup) time.Duration {
	//: zero sets none.
	return time.Duration(group.timeouts.Idle)
}
