// Package kit — the daemon: what a running app knows of itself beyond its
// nodes.
package kit

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/signal"
)

// maxPhaseHistory caps the phase changes kept; the oldest go first.
const maxPhaseHistory int = 64

// Pprof labels ----------------------------------------------------------

// The pprof labels kit puts on goroutines, in dev: what a CPU sample, a
// goroutine, is doing, in the diagram's words.
const (
	// labelNode names the node whose work the goroutine is doing. begin sets
	// it and end restores what was there before.
	labelNode = "kit_node"
	// labelLoop names the loop the goroutine belongs to: the HTTP server,
	// the scheduler, a consumer.
	labelLoop = "kit_loop"
)

// stopSignals are the signals that end App.Run: an interrupt from a
// terminal, a termination from a supervisor.
var stopSignals = []signal.Signal{mustSignal("SIGINT"), mustSignal("SIGTERM")}

// runtimeState is what the daemon knows about itself beyond its nodes — its
// history, its HTTP server, its scheduler — and what the Studio may change in
// it, in dev. Fields are guarded by App.mu unless they say otherwise.
type runtimeState struct {
	// history lists the phases the app went through, across its runs.
	history []model.PhaseChange
	// http counts the current run's connections and requests.
	http *httpStats
	// sched runs the current run's scheduler.
	sched *schedRunner
	// plog is the logger kit.Log hands to product code: the app's logger,
	// which in dev also feeds the hub's log ring. Set by resolve.
	plog logger.Logger
	// config is what the current run's start read; boot, the steps it took.
	config []model.Setting
	boot   []model.BootStep
	// dbs are the current run's databases (database_run.go).
	dbs []*databaseRun
}

// signalCause is the cause of the run's context when a signal ended it.
type signalCause struct{ sig signal.Signal }

// newRun forgets what belonged to the previous run: its counters, its
// scheduler, its configuration and boot. The history stays. The caller holds
// a.mu.
func (rt *runtimeState) newRun() {
	rt.http, rt.sched = nil, nil
	rt.config, rt.boot, rt.dbs = nil, nil, nil
}

// rememberPhase records a phase change. The caller holds a.mu.
func (a *App) rememberPhase(p, reason string) {
	now := time.Now().UTC()
	if a.clock != nil {
		now = a.clock.Now().UTC()
	}
	a.rt.history = append(a.rt.history, model.PhaseChange{Phase: p, At: now, Reason: reason})
	if len(a.rt.history) > maxPhaseHistory {
		a.rt.history = slices.Delete(a.rt.history, 0, len(a.rt.history)-maxPhaseHistory)
	}
}

// failReason says why the app failed to start, in words fit for the Studio:
// a fixed sentence, never an error's own text.
func (a *App) failReason(err error) string {
	if isDiagnostics(err) {
		return "the declarations have errors"
	}
	for _, c := range a.componentsCopy() {
		if c.State == model.ComponentFailed {
			return "component " + c.Name + " failed"
		}
	}
	switch {
	case errs.HasCode(err, CodeAppConfig):
		return "the configuration cannot be applied"
	case errs.HasCode(err, CodeAppRunning):
		return "a service of the app is already running in another app"
	}
	return "the start failed"
}

// mustSignal names a signal the SDK knows on every platform.
func mustSignal(name string) signal.Signal {
	s, err := signal.Parse(name)
	if err != nil {
		panic("kit: " + name + " is not a signal: " + err.Error())
	}
	return s
}

// Error says which signal ended the run.
func (c signalCause) Error() string { return c.sig.String() + " received" }

// stopReason says why a run's context ended: the signal, or the caller.
func stopReason(ctx context.Context) string {
	if sc, ok := errors.AsType[signalCause](context.Cause(ctx)); ok {
		return sc.sig.String()
	}
	return "the context ended"
}

// describeRuntime adds to rt what the daemon knows about itself: its phase
// history, the process, the HTTP server and — in dev — the Studio's clock
// and mocks.
func (a *App) describeRuntime(rt *model.Runtime) {
	a.mu.Lock()
	rt.History = slices.Clone(a.rt.history)
	rt.Config = slices.Clone(a.rt.config)
	rt.Boot = slices.Clone(a.rt.boot)
	a.mu.Unlock()
	rt.Process = sampleProcess()
	rt.HTTP = a.describeHTTP()
	rt.Databases = a.describeDatabases()
	if m := a.mockList(); len(m) > 0 {
		rt.Mocks = m
	}
}
