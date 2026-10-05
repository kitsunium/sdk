package health

import (
	"context"
	"errors"

	corehealth "github.com/kitsunium/sdk/internal/core/app/health"
	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// component is Component's body: decl_gen.go writes Component, from the
// design, as one call of it.
func component(registry corehealth.Health, name string) corelc.ComponentValue {
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
// relabelling them here would cost them their errors.Is. The one exception is
// a refusal with no error to report at all — see startupUnexplained.
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
		failed := failures(report)
		//: errors.Join of an empty slice is a genuine nil, and lifecycle reads a
		//: nil Start as SUCCESS — so a report that is not serving and names no
		//: failure would declare the application up. It never reaches Join.
		if len(failed) == 0 {
			//: still a refusal, just an unexplained one.
			return startupUnexplained(report.Status.String())
		}
		//: the caller's own errors, verbatim, so their errors.Is keeps working.
		return errors.Join(failed...)
	}
}

// startupUnexplained is the refusal for a startup report that is not serving
// and carries no error saying why.
//
// This package's own registry never produces one — every result it reports as
// failing carries its error — so the path is reached through a Health written
// elsewhere, or a Status outside the three. It is StartupPending's code,
// reason, public message and exit code, so errors.Is and errs.HasCode both
// match it; the Private is this path's own, because wrapping the sentinel would
// inherit one describing a readiness short-circuit instead.
func startupUnexplained(status string) error {
	//: read from the sentinel so the identity cannot drift from it.
	return kerrs.Wrap(nil, kerrs.WrapParams{
		Code:     corehealth.StartupPending.Code(),
		Reason:   corehealth.StartupPending.Reason(),
		Public:   corehealth.StartupPending.Public(),
		Private:  "service/app/health: the startup probe did not report serving and no result carried an error; the field carries the status it reported",
		ExitCode: corehealth.StartupPending.ExitCode(),
	}, kerrs.String("probe", corehealth.ProbeStartup.String()), kerrs.String("status", status))
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
