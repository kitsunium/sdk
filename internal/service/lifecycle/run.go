// Package lifecycle — hosts Run, the opt-in wiring between a Lifecycle and
// the process supervision the SDK already implements.
package lifecycle

import (
	"context"
	"errors"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/sdnotify"
	"github.com/kitsunium/sdk/internal/service/proc/signal"
)

// RunConfig parameterises [Run]. Its zero value wires NOTHING: no signal
// handler is installed and no datagram is sent, so [Run] reduces to
// start-wait-on-ctx-stop.
//
// That is the point. Signals and sd_notify are process-wide, observable side
// effects, and a library that installs them because it was imported is a
// library that fights the caller's own main. Both are opt-in fields, and both
// delegate to the SDK packages that already implement them — this file wires,
// it does not reimplement.
type RunConfig struct {
	// Signals is the set of OS signals that end the wait. An empty slice
	// installs no handler at all: the caller's context stays the only way to
	// end the wait, which is what a process whose main already owns signal
	// handling needs.
	//
	// The subscription is torn down before Run returns, so a caller that runs
	// two lifecycles in sequence does not accumulate handlers.
	Signals []coreproc.Signal
	// Notify asks for the systemd sd_notify(3) readiness protocol: READY=1
	// once every component is up, STOPPING=1 before the shutdown begins.
	//
	// It is safe to set unconditionally on a process that may not be
	// supervised: with $NOTIFY_SOCKET unset every notifier call is a
	// documented no-op that returns nil, so an unsupervised binary stays
	// silent rather than failing.
	Notify bool
}

// Run starts every component, waits, and stops them again — the whole of a
// service main, minus the caller's own wiring.
//
// It returns when ctx is cancelled or when one of cfg.Signals arrives,
// whichever happens first, having stopped every component in reverse order by
// then. A failed start is reported as-is: the components that came up before
// it were already unwound by [lifecycle.Start], so there is nothing left for
// Run to clean up.
func Run(ctx context.Context, lc corelc.Lifecycle, cfg RunConfig) error {
	//: nothing to announce and nothing to wait for until the components are up.
	if err := lc.Start(ctx); err != nil {
		//: Start already unwound whatever it had brought up.
		return err
	}
	//: a readiness that cannot be delivered is not cosmetic: the supervisor
	//: will kill a unit that never reports ready, so take the components back
	//: down rather than leaving a process nobody believes is running.
	if err := announce(cfg.Notify, sdnotify.Ready, "READY"); err != nil {
		//: the stop error joins the readiness error; neither hides the other.
		return errors.Join(err, lc.Stop(ctx))
	}
	waitForStop(ctx, cfg.Signals)
	//: announce BEFORE the shutdown, so the supervisor knows the process is
	//: going down while it is still draining rather than after.
	notifyErr := announce(cfg.Notify, sdnotify.Stopping, "STOPPING")
	//: ctx is very likely cancelled by now; Stop detaches from it on purpose.
	return errors.Join(notifyErr, lc.Stop(ctx))
}

// waitForStop blocks until the context is cancelled or a subscribed signal
// arrives.
func waitForStop(ctx context.Context, sigs []coreproc.Signal) {
	var received <-chan coreproc.Signal
	//: an empty set installs no handler — the caller's main keeps its signals.
	if len(sigs) > 0 {
		channel, unsubscribe := signal.Notify(sigs...)
		//: torn down before Run returns, so sequential lifecycles do not
		//: accumulate handlers on the same process.
		defer unsubscribe()
		received = channel
	}
	//: a nil channel blocks forever, so the signal arm simply never fires when
	//: nothing was subscribed. The same right-absence idiom as
	//: core/net.DrainSignal: no nil check, no second select, no duplicated arm.
	select {
	//: the caller asked to stop.
	case <-ctx.Done():
	//: the operating system asked to stop.
	case <-received:
	}
}

// announce delivers one opt-in sd_notify state.
func announce(enabled bool, send func() error, state string) error {
	//: not requested — the SDK sends nothing it was not asked to send.
	if !enabled {
		//: silent, and successful.
		return nil
	}
	err := send()
	//: the ordinary path, including the unsupervised no-op.
	if err == nil {
		//: delivered, or deliberately silent.
		return nil
	}
	//: side by side rather than wrapped: origin-wins would relabel
	//: READINESS_FAILED with whatever sdnotify reported.
	return errors.Join(kerrs.Wrap(ReadinessFailed, kerrs.WrapParams{},
		kerrs.String("state", state)), err)
}
