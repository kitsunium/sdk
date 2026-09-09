// Package lifecycle — hosts the engine: registration, the state Start and
// Stop share, and the observation hook.
package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// lifecycle is the concrete core/lifecycle.Lifecycle. It is unexported: there
// is one canonical ordering discipline, so a registry would be
// over-abstraction (the proc, resilience and scheduler precedents — ADR 0016 /
// ADR 0026 / ADR 0041).
type lifecycle struct {
	// clk is the injected time source; never package time.
	clk clock.Timed
	// budget is the per-component stop budget, already clamped.
	budget time.Duration
	// onTransition is the caller's observation hook, or nil.
	onTransition func(corelc.TransitionValue)
	// hookMu serialises onTransition so the hook need not be concurrency-safe.
	hookMu sync.Mutex
	// opMu serialises Start and Stop as WHOLE operations. Without it a Stop
	// arriving mid-Start would take a snapshot of the components that are up
	// so far, stop those, and leave Start still starting the rest — a torn
	// shutdown that no amount of per-field locking prevents.
	opMu sync.Mutex
	// mu guards the registration set and the run state below.
	mu sync.Mutex
	// running reports that Start has begun and Stop has not finished; it
	// freezes the component order.
	running bool
	// components holds the registrations in Add order, which IS the start
	// order and the reverse of the stop order.
	components []corelc.ComponentValue
	// names is the duplicate-name index.
	names map[string]bool
	// started holds the components whose Start returned nil, in the order
	// they returned. It is appended to BEFORE the next Start is attempted, so
	// there is no instant at which a component is up and unrecorded — which
	// is the whole guarantee behind the partial-start unwind.
	started []corelc.ComponentValue
}

// New returns a Lifecycle built from cfg. It cannot fail: a nil Clock falls
// back to clock.System, a non-positive StopTimeout to [DefaultStopTimeout],
// and a nil OnTransition to no observation — all working configurations
// rather than inert ones (ADR 0031). What CAN fail — an unrunnable component,
// a duplicate name — fails at Add, where the caller made the mistake.
func New(cfg Config) corelc.Lifecycle {
	//: names is built eagerly so Add never has to check for a nil map.
	return &lifecycle{
		clk:          cfg.resolvedClock(),
		budget:       cfg.stopBudget(),
		onTransition: cfg.OnTransition,
		names:        map[string]bool{},
	}
}

// Add registers a component at the end of the order, refusing anything that
// could not run.
func (l *lifecycle) Add(component corelc.ComponentValue) error {
	//: validate before taking the lock — a malformed component never touches
	//: state.
	if err := validateComponent(component); err != nil {
		//: the refusal already names the missing part.
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	//: the order is frozen from the moment Start begins.
	if l.running {
		//: name the component that was refused, not just the state.
		return kerrs.Wrap(corelc.LifecycleRunning, kerrs.WrapParams{},
			kerrs.String("component", component.Name))
	}
	//: a duplicate name would make every transition and every error ambiguous.
	if l.names[component.Name] {
		//: refuse rather than shadowing the first registration.
		return kerrs.Wrap(corelc.DuplicateComponent, kerrs.WrapParams{},
			kerrs.String("component", component.Name))
	}
	l.names[component.Name] = true
	l.components = append(l.components, component)
	//: registered at the end of the order.
	return nil
}

// validateComponent refuses a component that could never run, naming the
// missing part.
func validateComponent(component corelc.ComponentValue) error {
	//: a nameless component cannot be reported on, and its transitions would
	//: be indistinguishable from any other nameless component's.
	if component.Name == "" {
		//: the field says which part is missing.
		return kerrs.Wrap(corelc.InvalidComponent, kerrs.WrapParams{},
			kerrs.String("missing", "Name"))
	}
	//: a nil Start would panic on the first bring-up.
	if component.Start == nil {
		//: refuse at registration, where the caller can still fix it.
		return kerrs.Wrap(corelc.InvalidComponent, kerrs.WrapParams{},
			kerrs.String("missing", "Start"), kerrs.String("component", component.Name))
	}
	//: a nil Stop would panic during shutdown — the one moment at which a
	//: panic costs the most, and the one the caller is least able to rehearse.
	if component.Stop == nil {
		//: refuse at registration, at the same place.
		return kerrs.Wrap(corelc.InvalidComponent, kerrs.WrapParams{},
			kerrs.String("missing", "Stop"), kerrs.String("component", component.Name))
	}
	//: runnable.
	return nil
}

// beginStart claims the running flag and snapshots the component order.
func (l *lifecycle) beginStart() (order []corelc.ComponentValue, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	//: a second Start would bring every component up twice.
	if l.running {
		//: refuse rather than doubling every component.
		return nil, kerrs.Wrap(corelc.LifecycleRunning, kerrs.WrapParams{})
	}
	l.running = true
	//: a previous aborted run left nothing up; start the record clean.
	l.started = nil
	//: the sequence reads the snapshot without a lock; Add is refused
	//: meanwhile, so the slice header cannot change under it.
	return slices.Clone(l.components), nil
}

// markStarted records one component as up. It runs BEFORE the next Start is
// attempted, which is what makes the unwind total: every component that is up
// is in this list by the time anything can fail.
func (l *lifecycle) markStarted(component corelc.ComponentValue) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, component)
}

// takeStarted hands back the components that are up, ALREADY REVERSED, and
// clears the record so nothing can be stopped twice.
//
// Reversing here rather than at each call site is deliberate: the reverse
// order is the domain's central promise, and there is exactly one place that
// can get it wrong.
func (l *lifecycle) takeStarted() []corelc.ComponentValue {
	l.mu.Lock()
	defer l.mu.Unlock()
	order := slices.Clone(l.started)
	l.started = nil
	slices.Reverse(order)
	//: last started, first stopped.
	return order
}

// release drops the running flag so the Lifecycle can be added to and started
// again.
func (l *lifecycle) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running = false
}

// guard calls one half of a component's contract with the panic recovered.
//
// A panic escaping a Start would skip the unwind entirely and leak every
// component already up — the exact failure this domain exists to prevent — and
// a panic escaping a Stop would abandon every component still to be stopped.
//
// phase is the rendered label rather than a corelc.Phase: this function only
// ever puts it in an error field, and taking the string keeps it from
// depending on a domain type it does not otherwise use.
func guard(ctx context.Context, name, phase string, fn func(context.Context) error) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: leave err as the component returned it.
			return
		}
		//: the recovered value travels as a FIELD, never as the wrap origin,
		//: so a panic carrying an *errs.Error cannot hijack COMPONENT_PANICKED.
		err = kerrs.Wrap(corelc.ComponentPanicked, kerrs.WrapParams{},
			kerrs.String("component", name), kerrs.String("phase", phase),
			kerrs.String("panic", fmt.Sprint(value)))
	}()
	//: the component gets the context the engine resolved for this phase.
	return fn(ctx)
}

// emit stamps Ended and publishes one transition through the caller's hook.
//
// The caller fills everything except Ended, which only this function can read
// honestly: it is the instant the engine STOPPED WAITING, which on an
// abandoned stop is when the budget expired and not when the component
// eventually returned.
func (l *lifecycle) emit(transition corelc.TransitionValue) {
	//: no hook is a working configuration — see Config.OnTransition.
	if l.onTransition == nil {
		//: nothing to publish to.
		return
	}
	transition.Ended = l.clk.Now()
	l.hookMu.Lock()
	defer l.hookMu.Unlock()
	//: a panic in the hook is the CALLER's bug and is deliberately not
	//: recovered: only the component is. Hiding an observer's panic would hide
	//: the defect in the code that was supposed to be watching.
	l.onTransition(transition)
}
