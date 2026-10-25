package server

import (
	"context"

	corehealth "github.com/kitsunium/sdk/internal/core/app/health"
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// healthCheckName names the server's readiness and liveness checks.
const healthCheckName string = "net/server"

// RegisterHealth registers the server's readiness and liveness checks on h,
// both named "net/server" (ADR 0188). Readiness is built from State: ready
// while serving with no stalled loop group. Liveness fails once a loop group
// has stayed stalled for the stall grace, 30 seconds. A receive that never
// completes on a live ring is not watched.
func (s *Server) RegisterHealth(h corehealth.Health) error {
	//: no registry to register on.
	if h == nil {
		//: refused by the registry's own sentinel.
		return errs.Wrap(corehealth.InvalidCheck, errs.WrapParams{}, errs.String("check", healthCheckName))
	}
	//: readiness first: it is the one an orchestrator routes on.
	if err := h.AddReadiness(corehealth.ReadinessCheckValue{Name: healthCheckName, Check: s.readiness}); err != nil {
		//: a duplicate, typically.
		return err
	}
	//: then liveness, process-local evidence only.
	return h.AddLiveness(corehealth.LivenessCheckValue{Name: healthCheckName, Check: s.liveness})
}

// readiness is the readiness check, built from State.
func (s *Server) readiness(_ context.Context) error {
	//: a snapshot; nothing outside the process is asked.
	return readinessOf(s.State())
}

// readinessOf is ready for a server serving with no stalled loop group: not
// before Start has bound everything, not once the drain began, and not while a
// loop group accepts nothing. A degraded server is ready: it serves.
func readinessOf(st corenet.StateValue) error {
	//: by phase.
	switch st.Phase {
	//: bound and accepting.
	case corenet.PhaseServing:
	//: not bound yet.
	case corenet.PhaseNew, corenet.PhaseStarting:
		//: not ready.
		return errs.Wrap(corenet.NotStarted, errs.WrapParams{}, errs.String("phase", st.Phase.String()))
	//: draining or stopped.
	default:
		//: not ready.
		return errs.Wrap(corenet.ServerClosed, errs.WrapParams{}, errs.String("phase", st.Phase.String()))
	}
	//: a loop group every ring of which is drained accepts nothing.
	for _, lp := range st.Loops {
		//: this one.
		if groupStalled(lp) {
			//: not ready, by name.
			return stallError(lp.Group)
		}
	}
	//: ready.
	return nil
}

// liveness is the liveness check: it fails once a loop group has stayed
// stalled for loopStallGrace (R1). A ring whose receive never completes is
// not watched: only the accept watchdog drains a ring.
func (s *Server) liveness() error {
	group, at := s.stalledGroup()
	//: no stall seen, or still within the grace.
	if group == "" || s.clk.Since(at) < loopStallGrace {
		//: alive.
		return nil
	}
	//: stalled past the grace: restart, with the runbook's switch set.
	return stallError(group)
}
