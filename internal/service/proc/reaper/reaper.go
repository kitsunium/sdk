package reaper

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Option configures a reaper returned by New. Options are applied in order; an
// unknown or zero option is a no-op. The set is intentionally small — a reaper
// has little to tune beyond an optional sweep callback for observability.
type Option func(*config)

// config is the internal, mutable accumulator an Option mutates. It never
// escapes the package: New copies the resolved values into the concrete reaper.
type config struct {
	// onReap, when non-nil, is invoked after every drain sweep with the number
	// of children reaped in that sweep (including zero). It must not block; it
	// runs on the reaper's own goroutine or the ReapOnce caller's goroutine. On
	// illumos and Solaris the loop also sweeps once a second, so an idle reaper
	// there reports zero once a second.
	onReap func(int)
	// onExit, when non-nil, hears of every child a sweep collects.
	onExit func(ExitEvent)
}

// ExitEvent is one child a sweep collected: an orphan re-parented to this
// process, or a child a Process spawned and owns.
type ExitEvent struct {
	// PID is the collected child's process id.
	PID int
	// Exit is how the child ended, in the words Process.Wait uses: the exit
	// code, or -1 and the terminating signal, and its rusage.
	Exit coreproc.ExitValue
	// Owned reports that a Process spawned the child and has been handed the
	// same status; false is an orphan, whose status ends here.
	Owned bool
}

// WithOnReap registers fn as a post-sweep callback receiving the count of
// children reaped in each sweep. It is purely observational; fn must not block.
// On illumos and Solaris the loop also sweeps once a second (their exits post
// no SIGCHLD), so there fn hears zero once a second from an idle reaper.
func WithOnReap(fn func(int)) Option {
	//: capture fn into the accumulator so New can copy it onto the reaper.
	return func(c *config) {
		//: store the callback verbatim; nil disables the hook.
		c.onReap = fn
	}
}

// WithOnExit registers fn as an observer of every child a sweep collects:
// its pid, its exit in the words a Process's Wait uses, and whether a Process
// owned it. It is the one record of an orphan's end a subreaper or PID 1 can
// give — the status is told once, synchronously, and stored nowhere (ADR
// 0093). fn runs under the sweep lock, after the owner's status is handed
// over: it must not block. Off Unix, where nothing is collected, it is
// retained and never called.
func WithOnExit(fn func(ExitEvent)) Option {
	//: capture fn into the accumulator so New can copy it onto the reaper.
	return func(c *config) {
		//: store the observer verbatim; nil disables it.
		c.onExit = fn
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
