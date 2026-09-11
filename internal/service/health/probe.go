// Package health — hosts the probe answers: the phase the process is in, the
// three short-circuits that make the phases mean something, and the
// aggregation rule.
package health

import (
	"context"
	"sync"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// phase is the process's own position in its life, derived from state rather
// than stored as a mode: draining is a flag, starting is "a startup check has
// yet to pass", and serving is neither. Nothing has to remember to advance it,
// so nothing can forget.
type phase uint8

const (
	// phaseStarting means at least one registered startup check has not
	// passed. With no startup checks registered this is never reached, which
	// is what makes a registry that only has readiness checks work out of the
	// box instead of sitting not-ready forever.
	phaseStarting phase = iota
	// phaseServing means the process is up and has not been drained.
	phaseServing
	// phaseDraining means Drain was called. One-way.
	phaseDraining
)

// phase reads the current phase.
func (h *health) phase() phase {
	//: a read lock: the phase is derived from two fields and stores nothing.
	h.mu.RLock()
	defer h.mu.RUnlock()
	//: draining outranks everything: it is terminal.
	if h.draining {
		//: going away.
		return phaseDraining
	}
	//: still waiting on a startup check.
	if h.startupPending > 0 {
		//: coming up.
		return phaseStarting
	}
	//: up, and not going away.
	return phaseServing
}

// Probe answers one probe and publishes the report to the observation hook.
func (h *health) Probe(ctx context.Context, probe corehealth.Probe) corehealth.ReportValue {
	report := h.answer(ctx, probe)
	h.publish(report)
	//: the caller gets the same value the hook saw.
	return report
}

// answer dispatches to the probe that was asked for.
func (h *health) answer(ctx context.Context, probe corehealth.Probe) corehealth.ReportValue {
	current := h.phase()
	//: a closed set of three, plus a refusal for anything else.
	switch probe {
	//: "is this process still coming up?"
	case corehealth.ProbeStartup:
		//: a latched check replays; the rest are measured.
		return h.evaluateAll(ctx, corehealth.ProbeStartup)
	//: "can this replica take traffic right now?"
	case corehealth.ProbeReadiness:
		//: the two short-circuits live here.
		return h.readiness(ctx, current)
	//: "is this process irrecoverable?"
	case corehealth.ProbeLiveness:
		//: never fails for a reason outside the process.
		return h.liveness(ctx, current)
	//: a Probe value core/health never mints, including the zero.
	default:
		//: refuse rather than pick a probe on the caller's behalf.
		return h.unknown(probe)
	}
}

// readiness answers the routing question, short-circuiting in the two phases
// where the answer does not depend on any dependency.
func (h *health) readiness(ctx context.Context, current phase) corehealth.ReportValue {
	//: terminal, and checked first: a draining process is not ready however
	//: healthy its dependencies are.
	if current == phaseDraining {
		//: the supervisor is told what the probe answers: without it, a unit
		//: that announced READY kept showing its last healthy STATUS line for
		//: the whole drain, since the only announce sat below this return.
		//: One datagram, on the change; later polls owe nothing.
		h.announce(ctx, corehealth.StatusUnhealthy)
		//: no check is run — dialling a dependency to reconfirm a decision
		//: already taken would only add load to a shutdown.
		return h.shortCircuit(corehealth.ProbeReadiness, "draining",
			corehealth.StatusUnhealthy, Draining)
	}
	//: still coming up: not ready, and not for a reason a dependency knows.
	if current == phaseStarting {
		//: no check is run, for the same reason.
		return h.shortCircuit(corehealth.ProbeReadiness, "starting",
			corehealth.StatusUnhealthy, StartupPending)
	}
	report := h.evaluateAll(ctx, corehealth.ProbeReadiness)
	h.announce(ctx, report.Status)
	//: the aggregate of every readiness check, dependency calls included.
	return report
}

// liveness answers the restart question.
func (h *health) liveness(ctx context.Context, current phase) corehealth.ReportValue {
	//: while starting, liveness is DISABLED and reports healthy without
	//: running anything. "Is a process that is still coming up
	//: irrecoverable?" has one safe answer, and a liveness check that
	//: legitimately fails mid-startup — a worker pool not yet built — would
	//: otherwise restart the process on every attempt, forever.
	//:
	//: The price is stated rather than hidden: a process WEDGED during
	//: startup is never killed by liveness. The startup probe's own failure
	//: is the signal an orchestrator must act on, which is what a startup
	//: probe is for.
	if current == phaseStarting {
		//: alive, by construction.
		return h.shortCircuit(corehealth.ProbeLiveness, "starting",
			corehealth.StatusHealthy, nil)
	}
	//: draining is deliberately NOT a short-circuit here. A draining process
	//: keeps answering liveness normally, because a replica killed for
	//: failing liveness mid-drain loses exactly the in-flight work the drain
	//: existed to finish.
	return h.evaluateAll(ctx, corehealth.ProbeLiveness)
}

// shortCircuit builds a one-result report for a probe that deliberately ran
// nothing, naming the phase that decided it.
//
// It carries a result rather than an empty list because "unhealthy, no
// details" is the least actionable thing a probe can say: an operator reading
// the body should see the word `draining` and stop looking for a broken
// dependency.
func (h *health) shortCircuit(probe corehealth.Probe, name string,
	status corehealth.Status, sentinel error,
) corehealth.ReportValue {
	var err error
	//: the healthy short-circuit has nothing to explain.
	if sentinel != nil {
		err = kerrs.Wrap(sentinel, kerrs.WrapParams{}, kerrs.String("probe", probe.String()))
	}
	now := h.clk.Now()
	//: one synthetic result, named for the phase rather than for a check.
	return corehealth.ReportValue{
		Probe: probe, Status: status, At: now,
		Results: []corehealth.ResultValue{{Name: name, Status: status, At: now, Err: err}},
	}
}

// unknown refuses a Probe value the domain never mints.
func (h *health) unknown(probe corehealth.Probe) corehealth.ReportValue {
	now := h.clk.Now()
	err := kerrs.Wrap(corehealth.UnknownProbe, kerrs.WrapParams{},
		kerrs.String("probe", probe.String()))
	//: unhealthy, which is what the zero Status already is: a caller who did
	//: not say what they were asking gets the conservative answer, not a
	//: guess that could authorise routing or a restart.
	return corehealth.ReportValue{
		Probe: probe, Status: corehealth.StatusUnhealthy, At: now,
		Results: []corehealth.ResultValue{{Name: "unknown", At: now, Err: err}},
	}
}

// evaluateAll runs every check registered for one probe and aggregates them.
//
// The checks run in PARALLEL, so a probe's latency is the largest budget and
// not their sum. Serialising them would make the endpoint's worst case grow
// with the number of dependencies — and an endpoint an orchestrator polls on a
// fixed period must not get slower every time somebody registers a check.
func (h *health) evaluateAll(ctx context.Context, probe corehealth.Probe) corehealth.ReportValue {
	entries := h.entriesFor(probe)
	results := make([]corehealth.ResultValue, len(entries))
	var wg sync.WaitGroup
	//: index-addressed, so results stay in REGISTRATION order rather than
	//: completion order — two identical probes must render identically. Each
	//: goroutine owns one slot and writes it once, so the slice needs no lock.
	for i, e := range entries {
		wg.Go(func() {
			results[i] = h.evaluate(ctx, e)
		})
	}
	//: every check has either answered or been abandoned at its own budget;
	//: neither outcome can outlive this wait.
	wg.Wait()
	//: no checks at all is healthy: the process answering IS the evidence
	//: (ADR 0031). That is also the seed the fold starts from.
	status := corehealth.StatusHealthy
	//: fold every contribution in, worst-wins; see corehealth.Worst.
	for _, result := range results {
		status = corehealth.Worst(status, result.Status)
	}
	//: the worst of the parts, degraded never masking unhealthy.
	return corehealth.ReportValue{Probe: probe, Status: status, At: h.clk.Now(), Results: results}
}

// publish hands the report to the observation hook, serialised.
func (h *health) publish(report corehealth.ReportValue) {
	//: no hook is a working configuration — see Config.OnReport.
	if h.cfg.OnReport == nil {
		//: nothing to publish to, and the SDK writes nowhere itself.
		return
	}
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	//: a panic in the hook is the CALLER's bug and is deliberately not
	//: recovered: only a check is. Hiding an observer's panic would hide the
	//: defect in the code that was supposed to be watching.
	h.cfg.OnReport(report)
}
