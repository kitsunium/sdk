package server

import (
	"context"
	stdnet "net"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
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
// plaintext, but for the http1 codec (http1.HandleLoop), which takes TLS:
// crypto/tls negotiates each connection on its own goroutine, bounded by
// HandshakeTimeout, offering HTTP/1.1 alone, and the cleartext reaches the
// codec on the portable backend's lanes (ADR 0198).
//
// The group keeps the server's lifecycle — the bind, the adoption of
// inherited sockets, MaxConns, the drain, State — and Loop(v) sizes and
// places the engine: io_uring after a probe of the running kernel, then epoll,
// on Linux, and the portable backend, the default engine's accept path
// serialised onto one handler goroutine, everywhere else. Start refuses a loop
// group given TLS it cannot take with LoopTLSUnsupported, one given Use or an option neither
// the engine (IdleTimeout, MaxConns, Adopt, Plaintext, Name, Engine) nor h,
// through LoopOptionHonourer, honours with LoopOptionUnsupported — an option
// given counts, its zero value included —, and an explicit backend the
// platform or the kernel cannot run with UnsupportedPlatform or
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
// every other option given — a zero value included — is refused unless the
// loop engine or the handler honours it (LoopOptionUnsupported, naming it).
// It reads the set of kinds the group was given, never a guess from non-zero
// fields (ADR 0187).
func (g *StreamGroup) loopRefusal() error {
	//: TLS only for the http1 codec, on the portable backend (ADR 0198); a
	//: raw loop handler stays plaintext (ADR 0186 §D2).
	if g.kinds.has(corenet.OptionTLS) {
		//: a raw loop handler, or a contradiction of the TLS path.
		if err := g.loopTLSRefusal(); err != nil {
			//: refused by name.
			return err
		}
	}
	chain, err := buildLoopChain(g.name, g.loopHandler, g.loopMiddlewares)
	//: a layer that does not forward every callback.
	if err != nil {
		//: refused by name.
		return err
	}
	g.loopChain = chain
	//: every option the group was given, in the constant block's order.
	for _, k := range g.kinds.list() {
		//: the engine's own, or the chain's; Use never, whatever it claims.
		if g.loopHonoursKind(chain, k) {
			continue
		}
		//: the first option nobody honours.
		return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("option", k.String()))
	}
	//: every option is honoured.
	return nil
}

// loopHonoursKind reports whether the loop group applies the option of kind
// k: the engine, the TLS codec's path, or the chain — Use never, whatever it
// claims.
func (g *StreamGroup) loopHonoursKind(chain corenet.LoopHandler, k corenet.OptionKind) bool {
	//: by who applies it.
	return loopHonours(k) || (g.tlsCodec() && tlsCodecHonours(k)) || (k != corenet.OptionMiddleware && chainHonours(chain, k))
}

// loopHonours reports the options the loop engine honours itself (ADR 0186
// §D4, ADR 0187): IdleTimeout, MaxConns and Adopt, and Plaintext, Name and
// Engine, which every engine honours. Listen and Loop are not options a loop
// judges; the drain is the server's.
func loopHonours(k corenet.OptionKind) bool {
	//: the six the engine applies.
	switch k {
	//: applied by the loop itself.
	case corenet.OptionIdleTimeout, corenet.OptionMaxConns, corenet.OptionAdopt,
		corenet.OptionPlaintext, corenet.OptionName, corenet.OptionEngine, corenet.OptionLoopMiddleware:
		//: honoured.
		return true
	//: every other kind needs the handler.
	default:
		//: not the engine's.
		return false
	}
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
		//: registered raw, so State reads the socket's own options; served
		//: through TLS when the codec takes it.
		s.registerLoopListeners(group, adopted, nil)
		lns = append(lns, group.loopLayers(adopted)...)
	}
	//: then every address, one listener each: the loop shards inside.
	for _, addr := range group.addrs {
		ln, rep, err := listen(ctx, addr, corenet.IdentityValue{}, false, false, s.rebindAttempts(addr))
		//: the address could not be bound.
		if err != nil {
			//: Start unwinds what is bound.
			return err
		}
		s.registerLoopListeners(group, []stdnet.Listener{ln}, &rep)
		lns = append(lns, group.loopLayers([]stdnet.Listener{ln})...)
	}
	//: an opt-in no backend may honour is refused before any engine starts.
	if err := loop.CheckCapabilities(group.name, group.loop); err != nil {
		//: Start unwinds the listeners.
		return err
	}
	plan := s.operator.plan(group.loopBackend(), nativeLoopBackends)
	//: the lane count is the same on every backend, a fallback included, so
	//: the chain learns it once, before any callback.
	if st, ok := group.loopChain.(corenet.LoopStarter); ok {
		st.OnStart(loopLanes(group.loop))
	}
	eng, err := startLoopEngine(group, lns, plan)
	//: the backend refused.
	if err != nil {
		//: Start unwinds the listeners.
		return err
	}
	//: started lower than the code asked, because the better backend failed.
	if st := eng.State(); st.Degraded {
		s.recordFallback(group.name, st.DegradedReason)
	}
	s.mu.Lock()
	s.loopEngines = append(s.loopEngines, eng)
	s.mu.Unlock()
	//: serving.
	return nil
}

// registerLoopListeners records a loop group's listeners, so Close and
// Shutdown close them and State reports them, as for every group: bound ones
// with the bind's report, adopted ones (rep nil) with SO_EXCLUSIVEADDRUSE
// read back.
func (s *Server) registerLoopListeners(group *StreamGroup, lns []stdnet.Listener, rep *bindReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: one row and one bound listener each.
	for _, ln := range lns {
		addr := corenet.AddressValue{Network: ln.Addr().Network(), Addr: ln.Addr().String()}
		s.listeners = append(s.listeners, &boundListener{group: group.name, addr: addr, ln: ln})
		row := corenet.ListenerStateValue{
			Group: group.name, Address: ln.Addr().String(), Network: addr.Network, Shards: 1, Adopted: rep == nil,
		}
		//: bound here: what the bind did; adopted: what the supervisor set.
		if rep != nil {
			row.Exclusive, row.BindWait = rep.exclusive, corenet.DurationValue(rep.wait)
		} else {
			row.Exclusive = exclusiveOf(listenerSocket(ln))
		}
		s.states = append(s.states, row)
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
