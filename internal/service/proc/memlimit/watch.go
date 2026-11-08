package memlimit

import (
	"context"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// minWatchPeriod is the shortest period Watch re-reads at: the files are read
// on every period, and a cap that changes is an operator's act, not a
// sub-second event.
const minWatchPeriod time.Duration = time.Second

// Watch applies the limit as ApplyWith does, reports it to onChange, then
// re-derives it every period on clk until ctx ends — and installs and reports
// again only when the derivation changed: an orchestrator's resize, a
// systemctl set-property MemoryMax=, a vertical autoscaler. A re-read on a
// clock is the only honest mechanism: the kernel reports no write to a
// cgroupfs limit file through inotify, and memory.max raises no event.
//
// A period under one second is clamped to one second (ADR 0031). When the cap
// is lifted, or falls below the floor, the runtime's default — no limit, and
// the GC percent that was in force — is put back rather than a limit no cap
// stands behind kept. An operator's GOMEMLIMIT is never touched. onChange may
// be nil; it runs on Watch's goroutine and must not block.
//
// It blocks until ctx ends and then returns nil; it returns
// MemlimitMisconfigured at once, and installs nothing, for a configuration
// ApplyWith refuses.
func Watch(
	ctx context.Context,
	clk clock.Timed,
	every time.Duration,
	cfg ConfigValue,
	onChange func(coreproc.MemoryLimitValue),
) error {
	//: the running process.
	return watchFrom(ctx, clk, every, cfg, systemEnvironment(), onChange)
}

// watchFrom is Watch in env.
func watchFrom(
	ctx context.Context,
	clk clock.Timed,
	every time.Duration,
	cfg ConfigValue,
	env environment,
	onChange func(coreproc.MemoryLimitValue),
) error {
	//: a configuration no derivation can honour installs nothing.
	if err := cfg.validate(); err != nil {
		//: refused at once.
		return err
	}
	//: an ended watch does nothing at all.
	if ctx.Err() != nil {
		//: not a fault: the caller stopped it.
		return nil
	}
	installer := governor{cfg: cfg.resolved(), env: env}
	last := installer.derive()
	installer.settle(last)
	report(onChange, last)
	period := max(every, minWatchPeriod)
	timer := clk.NewTimer(period)
	defer timer.Stop()
	//: one derivation per period, until the caller stops.
	for {
		select {
		//: stopped by the caller.
		case <-ctx.Done():
			//: not a fault.
			return nil
		//: due: read again, act on a change only.
		case _, open := <-timer.C():
			//: a timer's channel never closes; were it to, stop rather than spin.
			if !open {
				return nil
			}
			next := installer.derive()
			//: the same files derive the same value: nothing to install.
			if next != last {
				installer.settle(next)
				report(onChange, next)
				last = next
			}
			timer.Reset(period)
		}
	}
}

// report hands a derivation to onChange, when there is one.
func report(onChange func(coreproc.MemoryLimitValue), derived coreproc.MemoryLimitValue) {
	//: a nil observer is allowed.
	if onChange != nil {
		onChange(derived)
	}
}
