// Package health — hosts the lifecycle wiring: the one registration that makes
// "not ready" precede a drain instead of following it.
package health

import (
	"context"
	"errors"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
)

// Component adapts a registry to core/lifecycle: its Start runs the startup
// checks, and its Stop marks the process draining.
//
// # Add it LAST
//
// One rule, and it happens to be right in both directions, because a
// Lifecycle's stop order is the exact reverse of its Add order (ADR 0050):
//
//   - Added last, it STARTS last — so its startup checks run after every
//     component they might depend on is already up, which is the only order in
//     which "the migration ran" is a question worth asking.
//   - Added last, it STOPS first — so readiness flips to not-ready before a
//     single other component begins closing. That is the whole point: an
//     orchestrator observes the 503, withdraws the replica from routing, and
//     the drain then runs against a socket nothing new arrives on.
//
// Get that order wrong and the drain begins while the load balancer is still
// sending traffic — the connections the drain is waiting for keep being
// created, and the shutdown budget expires against work that never stops
// arriving.
//
// Liveness deliberately keeps answering through the whole shutdown, so the
// orchestrator withdraws the replica rather than killing it.
func Component(registry corehealth.Health, name string) corelc.ComponentValue {
	//: both halves are required by lifecycle.Add; neither is a placeholder.
	return corelc.ComponentValue{
		Name:  name,
		Start: startupGate(registry),
		Stop:  drainGate(registry),
	}
}

// startupGate returns a lifecycle Start that refuses to declare the
// application up while a startup check is failing.
//
// It reports the checks' OWN errors, joined, rather than a sentinel of its
// own: the caller registered those checks and wrote those errors, and
// relabelling them here would cost them their errors.Is.
func startupGate(registry corehealth.Health) corelc.Start {
	//: a closure over the registry, because lifecycle.Start is a FUNC port —
	//: there is no interface here to hang a method on (ADR 0050).
	return func(ctx context.Context) error {
		report := registry.Probe(ctx, corehealth.ProbeStartup)
		//: the ordinary path, including a registry with no startup checks at
		//: all — which is healthy, because the process running is the
		//: evidence (ADR 0031).
		if report.Status.Serving() {
			//: startup complete; readiness takes over from here.
			return nil
		}
		//: errors.Join yields a genuine nil for an empty slice, so a report
		//: that is unhealthy for no recorded reason cannot fake a success.
		return errors.Join(failures(report)...)
	}
}

// drainGate returns a lifecycle Stop that marks the registry draining and
// returns at once.
//
// It closes nothing and waits for nothing. The withdrawal it triggers is
// observed by an orchestrator polling from outside the process, on ITS
// schedule, which the SDK neither knows nor should guess at: sleeping here for
// a "propagation delay" would spend the shutdown budget of a component that
// has nothing to shut down. A caller who needs that delay expresses it as its
// own component, added after this one.
func drainGate(registry corehealth.Health) corelc.Stop {
	//: the same closure shape as startupGate, over the same registry; the
	//: context is ignored because Drain neither blocks nor can be cancelled.
	return func(_ context.Context) error {
		registry.Drain()
		//: instant, and idempotent — lifecycle may call a Stop exactly once,
		//: but a caller may also call Drain themselves.
		return nil
	}
}

// failures collects the errors of every result that did not pass.
func failures(report corehealth.ReportValue) []error {
	collected := make([]error, 0, len(report.Results))
	//: registration order, so the joined message reads like the report does.
	for _, result := range report.Results {
		//: a passing check contributes nothing, and neither does a failing
		//: one that somehow carries no error — a nil in a Join would be
		//: dropped anyway, and appending it would only obscure the count.
		if result.Status == corehealth.StatusHealthy || result.Err == nil {
			continue
		}
		collected = append(collected, result.Err)
	}
	//: verbatim, so the caller's errors.Is keeps working.
	return collected
}
