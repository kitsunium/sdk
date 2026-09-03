// Package server — the start, accept and drain lifecycle.
package server

import (
	"context"
	"errors"
	stdnet "net"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// drainPollInterval is how often the drain re-checks the in-flight count.
const drainPollInterval time.Duration = 10 * time.Millisecond

// Start binds every group's listeners and begins accepting.
//
// It returns once every listener is bound, so a caller that gets a nil error
// knows the ports are open — which is what makes a test able to dial
// immediately, and a supervisor able to report readiness honestly.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	//: a second Start would bind a second set of listeners on the same ports.
	if corenet.Phase(s.phase.Load()) != corenet.PhaseNew {
		s.mu.Unlock()
		//: report rather than silently double-bind.
		return errs.Wrap(corenet.AlreadyStarted, errs.WrapParams{})
	}
	//: a declaration error is reported here rather than at the call that made
	//: it, so the declaration chain stays free of error checks.
	if s.declErr != nil {
		s.mu.Unlock()
		//: surface the first recorded declaration mistake.
		return s.declErr
	}
	s.phase.Store(uint32(corenet.PhaseStarting))
	groups := slices.Clone(s.groups)
	packetGroups := slices.Clone(s.packetGroups)
	s.mu.Unlock()

	//: the run context outlives the caller's ctx so cancelling the caller does
	//: not tear down handlers before the drain has had its budget.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.runCtx = runCtx
	s.stop = cancel
	//: a failed bind must not leave earlier listeners open.
	if err := s.bindEverything(ctx, groups, packetGroups); err != nil {
		//: closing here is best-effort cleanup; the bind error is what matters.
		if cerr := s.Close(); cerr != nil {
			//: report both, so a cleanup failure never hides the bind failure.
			return errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
				errs.String("cause", err.Error()), errs.String("cleanup", cerr.Error()))
		}
		//: report the bind failure that aborted startup.
		return err
	}
	s.phase.Store(uint32(corenet.PhaseServing))
	//: every listener is bound and accepting.
	return nil
}

// bindEverything binds both natures, streams first so a mixed server's ports
// come up in declaration order.
func (s *Server) bindEverything(ctx context.Context, groups []*StreamGroup, packetGroups []*PacketGroup) error {
	//: streams first, then datagrams.
	if err := s.bindAll(ctx, groups); err != nil {
		//: abort before binding any datagram socket.
		return err
	}
	//: datagram groups bind the same way, minus the accept step.
	return s.bindPacketAll(ctx, packetGroups)
}

// bindPacketAll binds every datagram group, starting a read loop per socket.
func (s *Server) bindPacketAll(ctx context.Context, groups []*PacketGroup) error {
	//: bind in declaration order so a failure is reproducible.
	for _, group := range groups {
		//: a group with no handler would read datagrams and drop them.
		if group.handler == nil {
			//: refuse rather than discard traffic silently.
			return errs.Wrap(corenet.HandlerMissing, errs.WrapParams{},
				errs.String("group", group.name))
		}
		//: a group with no address is almost always a wiring mistake.
		if len(group.addrs) == 0 {
			//: refuse rather than start a group that listens nowhere.
			return errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
				errs.String("group", group.name), errs.String("why", "no listen address"))
		}
		//: propagate the first bind failure.
		if err := s.bindPacketGroup(ctx, group); err != nil {
			//: abort startup; Start unwinds whatever is already bound.
			return err
		}
	}
	//: every datagram group is bound.
	return nil
}

// bindPacketGroup binds one datagram group and starts its read loops.
//
// Goroutine lifecycle: it starts exactly one readLoop per address. Each is
// owned by the Server, holds an inFlight token released on exit, and terminates
// when Close or Shutdown closes its socket.
func (s *Server) bindPacketGroup(ctx context.Context, group *PacketGroup) error {
	handler := group.resolved()
	//: inherited sockets are taken over before any address is bound.
	if err := s.adoptPacketSockets(group, handler); err != nil {
		//: an activation mismatch aborts startup.
		return err
	}
	//: one socket and one read goroutine per address.
	for _, addr := range group.addrs {
		pc, err := listenPacket(ctx, addr)
		//: surface the address that could not be bound.
		if err != nil {
			//: abort startup; Start unwinds whatever is already bound.
			return err
		}
		bound := &boundPacketConn{group: group.name, addr: addr, pc: pc}
		degraded, reason := batchDegradation(group.limits)
		s.mu.Lock()
		s.packetConns = append(s.packetConns, bound)
		s.states = append(s.states, corenet.ListenerStateValue{
			Group:          group.name,
			Address:        pc.LocalAddr().String(),
			Network:        addr.Network,
			Shards:         1,
			Degraded:       degraded,
			DegradedReason: reason,
		})
		s.mu.Unlock()
		s.inFlight.Add(1)
		go s.readLoop(bound, group, handler)
	}
	//: every address in the group is bound.
	return nil
}

// adoptPacketSockets takes over the group's inherited datagram sockets.
//
// Goroutine lifecycle: one readLoop per adopted socket, owned by the Server
// exactly as a bound one is.
func (s *Server) adoptPacketSockets(group *PacketGroup, handler corenet.PacketHandler) error {
	//: each name may publish several descriptors.
	for _, name := range group.adopt {
		conns, err := adoptPacket(name)
		//: an activation mismatch aborts startup rather than binding instead.
		if err != nil {
			//: the error already names the socket.
			return err
		}
		degraded, reason := batchDegradation(group.limits)
		//: one read goroutine per adopted descriptor.
		for _, pc := range conns {
			addr := corenet.AddressValue{Network: pc.LocalAddr().Network(), Addr: pc.LocalAddr().String()}
			bound := &boundPacketConn{group: group.name, addr: addr, pc: pc}
			s.mu.Lock()
			s.packetConns = append(s.packetConns, bound)
			s.states = append(s.states, corenet.ListenerStateValue{
				Group:          group.name,
				Address:        pc.LocalAddr().String(),
				Network:        addr.Network,
				Shards:         1,
				Adopted:        true,
				Degraded:       degraded,
				DegradedReason: reason,
			})
			s.mu.Unlock()
			s.inFlight.Add(1)
			go s.readLoop(bound, group, handler)
		}
	}
	//: every named socket was taken over.
	return nil
}

// batchDegradation reports whether batched reading was asked for and is
// unavailable, and why. A silent fallback is indistinguishable from a working
// one, so it is surfaced through State rather than logged once at startup.
func batchDegradation(limits corenet.LimitsValue) (degraded bool, reason string) {
	//: only a group that actually asked for batching is degraded by its absence.
	if batchAvailable() || batchSize(limits) <= 1 {
		//: got what it asked for, so there is nothing to report.
		return false, ""
	}
	//: name the fallback in plain words for whoever reads State.
	return true, "batched datagram read unavailable on this platform; reading one per syscall"
}

// bindAll binds every group, starting an accept loop per listener.
func (s *Server) bindAll(ctx context.Context, groups []*StreamGroup) error {
	//: bind in declaration order so a failure is reproducible.
	for _, group := range groups {
		//: a group with no handler would accept connections and drop them.
		if group.handler == nil {
			//: refuse rather than accept connections and drop them.
			return errs.Wrap(corenet.HandlerMissing, errs.WrapParams{},
				errs.String("group", group.name))
		}
		//: a group with neither an address nor an inherited socket is almost
		//: always a wiring mistake.
		if len(group.addrs) == 0 && len(group.adopt) == 0 {
			//: refuse rather than start a server that listens nowhere.
			return errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
				errs.String("group", group.name), errs.String("why", "no listen address"))
		}
		//: propagate the first bind failure.
		if err := s.bindGroup(ctx, group); err != nil {
			//: abort startup; Start unwinds whatever is already bound.
			return err
		}
	}
	//: every group is bound.
	return nil
}

// bindGroup binds one group's addresses and starts their accept loops.
func (s *Server) bindGroup(ctx context.Context, group *StreamGroup) error {
	handler := group.resolved()
	//: built once per group, so the ceiling is shared by every shard and
	//: every address the group listens on — one budget, not one per socket.
	group.limiter = newConnLimiter(group.limits)
	//: inherited sockets are taken over before any address is bound, so an
	//: activation mismatch fails startup rather than half-binding.
	if err := s.adoptStreamSockets(group, handler); err != nil {
		//: an activation mismatch aborts startup.
		return err
	}
	//: one shard set per address, each shard with its own accept goroutine.
	for _, addr := range group.addrs {
		//: propagate the first bind failure with the address that caused it.
		if err := s.bindShards(ctx, group, addr, handler); err != nil {
			//: abort startup; Start unwinds whatever is already bound.
			return err
		}
	}
	//: every address in the group is bound.
	return nil
}

// adoptStreamSockets takes over the group's inherited stream listeners.
//
// Goroutine lifecycle: one acceptLoop per adopted listener, owned by the Server
// exactly as a bound one is — adoption changes where the socket came from, not
// how it is served.
func (s *Server) adoptStreamSockets(group *StreamGroup, handler corenet.ConnHandler) error {
	//: each name may publish several descriptors.
	for _, name := range group.adopt {
		listeners, err := adoptStream(name)
		//: an activation mismatch aborts startup; binding instead would lose
		//: the very property socket activation exists to provide.
		if err != nil {
			//: the error already names the socket.
			return err
		}
		//: one accept goroutine per adopted descriptor.
		for _, ln := range listeners {
			addr := corenet.AddressValue{Network: ln.Addr().Network(), Addr: ln.Addr().String()}
			bound := &boundListener{group: group.name, addr: addr, ln: ln}
			s.mu.Lock()
			s.listeners = append(s.listeners, bound)
			s.states = append(s.states, corenet.ListenerStateValue{
				Group:   group.name,
				Address: ln.Addr().String(),
				Network: addr.Network,
				Shards:  1,
				Adopted: true,
			})
			s.mu.Unlock()
			s.inFlight.Add(1)
			go s.acceptLoop(bound, group, handler)
		}
	}
	//: every named socket was taken over.
	return nil
}

// bindShards opens one address's listeners and starts an accept loop per shard.
//
// Several listeners on one address is the whole point of SO_REUSEPORT: the
// kernel load-balances incoming connections across them, so N accept loops
// never contend on a single accept queue. Where the option is unavailable the
// count collapses to one and State reports why.
//
// Goroutine lifecycle: it starts exactly one acceptLoop per shard. Each is
// owned by the Server, holds an inFlight token released on exit, and terminates
// when Close or Shutdown closes its listener — the only signal that reliably
// interrupts a blocking Accept.
func (s *Server) bindShards(ctx context.Context, group *StreamGroup, addr corenet.AddressValue, handler corenet.ConnHandler) error {
	count, degraded, reason := resolveShards(group.limits.Shards, addr.Network)
	opened := 0
	//: every shard binds the same address; the kernel spreads accepts across them.
	for range count {
		ln, err := listen(ctx, addr, group.identity, count > 1)
		//: a shard that will not bind aborts startup rather than serving a
		//: quietly smaller set than the operator asked for.
		if err != nil {
			//: surface the address that could not be bound.
			return err
		}
		bound := &boundListener{group: group.name, addr: addr, ln: ln}
		s.mu.Lock()
		s.listeners = append(s.listeners, bound)
		//: only the first shard is reported, carrying the real shard count — N
		//: rows for one address would read as N separate addresses.
		if opened == 0 {
			s.states = append(s.states, corenet.ListenerStateValue{
				Group:          group.name,
				Address:        ln.Addr().String(),
				Network:        addr.Network,
				Shards:         count,
				Degraded:       degraded,
				DegradedReason: reason,
			})
		}
		s.mu.Unlock()
		opened++
		s.inFlight.Add(1)
		go s.acceptLoop(bound, group, handler)
	}
	//: every shard for this address is accepting.
	return nil
}

// acceptLoop accepts connections until its listener is closed.
//
// Goroutine lifecycle: one per listener, started by bindGroup and owned by the
// Server. It exits when Close or Shutdown closes the listener; its inFlight
// token is released on exit. It starts one serve goroutine per accepted
// connection, each of which takes its own token.
func (s *Server) acceptLoop(bound *boundListener, group *StreamGroup, handler corenet.ConnHandler) {
	defer s.inFlight.Done()
	//: accept until the listener is closed beneath us.
	for {
		raw, err := bound.ln.Accept()
		//: a closed listener ends the loop; anything else is transient and the
		//: loop continues, because one failed accept must not stop the server.
		if err != nil {
			//: the listener was closed by Close or Shutdown.
			if errors.Is(err, stdnet.ErrClosed) {
				//: Close or Shutdown ended this loop deliberately.
				return
			}
			continue
		}
		s.total.Add(1)
		s.active.Add(1)
		s.inFlight.Add(1)
		go s.serve(raw, group, handler)
	}
}

// serve runs the handler for one connection.
//
// Goroutine lifecycle: one per accepted connection, started by acceptLoop and
// owned by the Server. It always closes the connection, releases its pooled
// state and its inFlight token, so a handler that returns early or panics
// cannot leak either.
func (s *Server) serve(raw stdnet.Conn, group *StreamGroup, handler corenet.ConnHandler) {
	defer s.inFlight.Done()
	defer s.active.Add(-1)
	c := s.acquire(raw, group)
	defer s.release(c)
	//: a panicking handler closes its own connection and nothing more; one
	//: malformed peer must never be able to take the process down.
	defer recoverHandler()
	applyDeadlines(raw, group.timeouts)
	//: the handler's error ends this connection and nothing else. It is
	//: deliberately not propagated: there is nobody above a connection
	//: goroutine to return it to, and one peer's failure is not the server's.
	//: A ceiling rejection lands here too, and closes the connection the same
	//: way — which is the intended answer to "we are full".
	swallowErr(s.admit(s.runCtx, group.limiter, c, handler))
}

// recoverHandler swallows a handler panic so it cannot unwind past the
// connection that caused it.
func recoverHandler() {
	//: a nil recover means the handler returned normally; a non-nil one has
	//: already been contained by the time this returns.
	recover()
}

// applyDeadlines installs the group's per-phase deadlines on a connection.
//
// Deadline errors are ignored deliberately: SetDeadline fails only on a
// connection that is already closed, in which case the handler's first read
// reports the real problem far more usefully than a setup error would.
func applyDeadlines(raw stdnet.Conn, timeouts corenet.TimeoutsValue) {
	//: an idle timeout bounds the whole connection's silence, so it is the
	//: outermost deadline and the others refine it.
	if idle := timeouts.Idle.Duration(); idle > 0 {
		swallowErr(raw.SetDeadline(time.Now().Add(idle)))
	}
	//: a read deadline bounds one read from the peer.
	if read := timeouts.Read.Duration(); read > 0 {
		swallowErr(raw.SetReadDeadline(time.Now().Add(read)))
	}
	//: a write deadline bounds one write to the peer.
	if write := timeouts.Write.Duration(); write > 0 {
		swallowErr(raw.SetWriteDeadline(time.Now().Add(write)))
	}
}

// Serve starts the server and blocks until ctx is cancelled, then drains.
//
// It is the one-call entry point: a caller that has nothing else to do on the
// main goroutine writes Serve and gets startup, blocking and graceful shutdown
// without assembling them.
func (s *Server) Serve(ctx context.Context) error {
	//: a failed start has already released whatever it bound.
	if err := s.Start(ctx); err != nil {
		//: a failed start already released whatever it bound.
		return err
	}
	<-ctx.Done()
	//: the drain budget is independent of the cancelled ctx, or shutdown would
	//: have no time left to run at the very moment it is needed.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.drainTimeout)
	defer cancel()
	//: drain within the budget and report whether it completed.
	return s.Shutdown(drainCtx)
}

// Shutdown stops accepting and waits for in-flight connections to finish,
// severing what remains once ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	//: shutting down a server that never started is a no-op, not an error.
	if corenet.Phase(s.phase.Load()) == corenet.PhaseNew {
		//: never started, so there is nothing to drain.
		return nil
	}
	s.phase.Store(uint32(corenet.PhaseDraining))
	//: closing the listeners is what ends the accept loops.
	s.closeListeners()
	//: stop any embedded http.Server first, so its keep-alive connections are
	//: released instead of holding the drain open to its full budget.
	s.shutdownHTTP(ctx)
	err := s.waitIdle(ctx)
	//: cancelling the run context asks every handler still running to stop.
	if s.stop != nil {
		s.stop()
	}
	//: the budget expired. Sever the remaining sockets and return rather than
	//: waiting: a handler blocked on something other than its socket cannot be
	//: killed, and blocking here would make the budget meaningless — which is
	//: exactly the bug this branch exists to prevent.
	if err != nil {
		s.closeLive()
		s.phase.Store(uint32(corenet.PhaseStopped))
		//: report the overrun rather than block forever.
		return err
	}
	//: every handler has returned, so this wait is short by construction.
	s.inFlight.Wait()
	s.phase.Store(uint32(corenet.PhaseStopped))
	//: the drain completed within its budget.
	return nil
}

// shutdownHTTP stops every group's embedded http.Server, if it has one.
func (s *Server) shutdownHTTP(ctx context.Context) {
	s.mu.RLock()
	groups := slices.Clone(s.groups)
	s.mu.RUnlock()
	//: only groups that were given an http.Handler carry an adapter.
	for _, group := range groups {
		//: a plain ConnHandler group has no embedded http.Server to stop.
		if group.httpAdapter != nil {
			group.httpAdapter.shutdown(ctx)
		}
	}
}

// waitIdle blocks until no connection is in flight or ctx expires.
func (s *Server) waitIdle(ctx context.Context) error {
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	//: poll until idle or out of budget.
	for {
		//: every connection has finished on its own — the clean outcome.
		if s.active.Load() == 0 {
			//: nothing in flight — the clean outcome.
			return nil
		}
		select {
		//: the budget expired with work still in flight.
		case <-ctx.Done():
			//: report the overrun with what was still running.
			return errs.Wrap(corenet.DrainTimeout, errs.WrapParams{},
				errs.Int64("active", s.active.Load()))
		//: re-check after the poll interval.
		case <-ticker.C:
		}
	}
}

// Close releases every listener and severs every live connection immediately,
// without draining.
func (s *Server) Close() error {
	s.closeListeners()
	//: cancelling asks in-flight handlers to stop; it does not wait for them.
	if s.stop != nil {
		s.stop()
	}
	//: severing the live sockets is what makes Close immediate rather than a
	//: drain with no budget.
	s.closeLive()
	s.phase.Store(uint32(corenet.PhaseStopped))
	//: closing is best-effort by contract; there is nothing actionable to report.
	return nil
}

// closeListeners closes and forgets every bound listener.
//
// A close error is not surfaced: during shutdown the only realistic cause is a
// listener already closed by a concurrent Close, and reporting that would turn
// a benign race into a spurious failure.
func (s *Server) closeListeners() {
	s.mu.Lock()
	listeners := s.listeners
	s.listeners = nil
	packetConns := s.packetConns
	s.packetConns = nil
	s.mu.Unlock()
	//: closing a datagram socket is what ends its read loop.
	for _, bound := range packetConns {
		swallowErr(bound.Close())
	}
	//: close every listener that was still open.
	//: close every listener that was still open.
	for _, bound := range listeners {
		swallowErr(bound.Close())
	}
}
