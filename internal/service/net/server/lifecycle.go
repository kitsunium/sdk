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
	s.mu.Unlock()

	//: the run context outlives the caller's ctx so cancelling the caller does
	//: not tear down handlers before the drain has had its budget.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.runCtx = runCtx
	s.stop = cancel
	//: a failed bind must not leave earlier listeners open.
	if err := s.bindAll(ctx, groups); err != nil {
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
		//: a group with no address is almost always a wiring mistake.
		if len(group.addrs) == 0 {
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
//
// Goroutine lifecycle: it starts exactly one acceptLoop per address. Each is
// owned by the Server, holds an inFlight token released on exit, and terminates
// when Close or Shutdown closes its listener — the only signal that reliably
// interrupts a blocking Accept.
func (s *Server) bindGroup(ctx context.Context, group *StreamGroup) error {
	handler := group.resolved()
	//: each address gets its own listener and its own accept goroutine.
	//: one listener and one accept goroutine per address.
	for _, addr := range group.addrs {
		ln, err := listen(ctx, addr, group.identity)
		//: surface the bind failure with the address that caused it.
		if err != nil {
			//: surface the address that could not be bound.
			return err
		}
		bound := &boundListener{group: group.name, addr: addr, ln: ln}
		s.mu.Lock()
		s.listeners = append(s.listeners, bound)
		s.states = append(s.states, corenet.ListenerStateValue{
			Group:   group.name,
			Address: ln.Addr().String(),
			Network: addr.Network,
			Shards:  1,
		})
		s.mu.Unlock()
		s.inFlight.Add(1)
		go s.acceptLoop(bound, group, handler)
	}
	//: every address in the group is bound.
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
	swallowErr(handler.ServeConn(s.runCtx, c))
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
	s.mu.Unlock()
	//: close every listener that was still open.
	//: close every listener that was still open.
	for _, bound := range listeners {
		swallowErr(bound.Close())
	}
}
