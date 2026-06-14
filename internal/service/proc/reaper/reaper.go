// Package reaper — the PID1 / subreaper zombie collector backing pkg/v1/reaper.
//
// New returns a coreproc.Reaper whose concrete behaviour is selected at build
// time: a real SIGCHLD-driven waitpid loop on Unix (reaper_unix.go) and a
// degrade-to-no-op stub elsewhere (reaper_other.go). SetChildSubreaper and
// IsPID1 are likewise platform-split. This file holds only the cross-platform
// Option surface so the option type is declared once.
package reaper

// Option configures a reaper returned by New. Options are applied in order; an
// unknown or zero option is a no-op. The set is intentionally small — a reaper
// has little to tune beyond an optional sweep callback for observability.
type Option func(*config)

// config is the internal, mutable accumulator an Option mutates. It never
// escapes the package: New copies the resolved values into the concrete reaper.
type config struct {
	// onReap, when non-nil, is invoked after every drain sweep with the number
	// of children reaped in that sweep (including zero). It must not block; it
	// runs on the reaper's own goroutine or the ReapOnce caller's goroutine.
	onReap func(int)
}

// WithOnReap registers fn as a post-sweep callback receiving the count of
// children reaped in each sweep. It is purely observational; fn must not block.
func WithOnReap(fn func(int)) Option {
	//: capture fn into the accumulator so New can copy it onto the reaper.
	return func(c *config) {
		//: store the callback verbatim; nil disables the hook.
		c.onReap = fn
	}
}

// resolve folds opts onto a zero config, returning the accumulated settings.
func resolve(opts []Option) config {
	//: zero accumulator the options mutate in caller order.
	var c config
	//: apply each option in caller order onto the accumulator.
	for _, opt := range opts {
		//: skip a nil option so a sparse variadic list never panics.
		if opt == nil {
			//: nothing to apply for a nil entry.
			continue
		}
		//: let the option mutate the shared accumulator.
		opt(&c)
	}
	//: hand back the fully-folded configuration.
	return c
}
