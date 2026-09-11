// Package scheduler — hosts the engine: registration, and the state Run needs.
package scheduler

import (
	"slices"
	"sync"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// scheduler is the concrete core/scheduler.Scheduler. It is unexported: there
// is one canonical engine, so a registry would be over-abstraction (the proc
// and resilience precedents, ADR 0016 / ADR 0026).
type scheduler struct {
	// clk is the injected time source; never package time.
	clk clock.Timed
	// onResult is the caller's observation hook, or nil.
	onResult func(coresched.ResultValue)
	// hookMu serialises onResult so the hook need not be concurrency-safe.
	hookMu sync.Mutex
	// mu guards the registration set and the running flag.
	mu sync.Mutex
	// running reports that a Run is in progress; it freezes the entry set.
	running bool
	// entries holds the registrations, in Add order — which is also the order
	// same-instant fires are started in, so a caller can reason about it.
	entries []*entry
	// names is the duplicate-name index.
	names map[string]bool
}

// New returns a Scheduler built from cfg. It cannot fail: a nil Clock falls
// back to clock.System and a nil OnResult to no observation, both of which are
// working configurations rather than inert ones (ADR 0031). What CAN fail —
// an unrunnable entry, an unparseable expression — fails at Add and at Parse,
// where the caller made the mistake.
func New(cfg Config) coresched.Scheduler {
	clk := cfg.Clock
	//: the documented fallback; never mutate clock.System at package scope.
	if clk == nil {
		//: the wall clock is the only non-arbitrary default here.
		clk = clock.System
	}
	//: names is built eagerly so Add never has to check for a nil map.
	return &scheduler{clk: clk, onResult: cfg.OnResult, names: map[string]bool{}}
}

// Add registers an entry, refusing anything that could not run.
func (s *scheduler) Add(value coresched.EntryValue) error {
	//: validate before taking the lock — a malformed entry never touches state.
	if err := validateEntry(value); err != nil {
		//: the refusal already names the missing part.
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the entry set is frozen while the loop is reading it.
	if s.running {
		//: name the entry that was refused, not just the state.
		return kerrs.Wrap(coresched.SchedulerRunning, kerrs.WrapParams{},
			kerrs.String("job", value.Name))
	}
	//: a duplicate name would make every result and every error ambiguous.
	if s.names[value.Name] {
		//: refuse rather than shadowing the first registration.
		return kerrs.Wrap(coresched.DuplicateJob, kerrs.WrapParams{},
			kerrs.String("job", value.Name))
	}
	s.names[value.Name] = true
	s.entries = append(s.entries, newEntry(value))
	//: registered.
	return nil
}

// validateEntry refuses an entry that could never run, naming the missing part.
func validateEntry(value coresched.EntryValue) error {
	//: a nameless entry cannot be reported on, and its results would be
	//: indistinguishable from any other nameless entry's.
	if value.Name == "" {
		//: the field says which part is missing.
		return kerrs.Wrap(coresched.InvalidEntry, kerrs.WrapParams{},
			kerrs.String("missing", "Name"))
	}
	//: a nil Schedule would panic on the first arm.
	if value.Schedule == nil {
		//: refuse at registration, where the caller can still fix it.
		return kerrs.Wrap(coresched.InvalidEntry, kerrs.WrapParams{},
			kerrs.String("missing", "Schedule"), kerrs.String("job", value.Name))
	}
	//: a nil Job would panic on the first fire — hours later, in production.
	if value.Job == nil {
		//: same refusal, at the same place.
		return kerrs.Wrap(coresched.InvalidEntry, kerrs.WrapParams{},
			kerrs.String("missing", "Job"), kerrs.String("job", value.Name))
	}
	//: runnable.
	return nil
}

// begin claims the running flag and takes a snapshot of the entry set.
func (s *scheduler) begin() (snapshot []*entry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: two concurrent Runs would fire every entry twice.
	if s.running {
		//: refuse the second one rather than doubling every job.
		return nil, kerrs.Wrap(coresched.SchedulerRunning, kerrs.WrapParams{})
	}
	s.running = true
	//: the loop reads the snapshot without a lock; Add is refused meanwhile,
	//: so the slice header cannot change under it.
	return slices.Clone(s.entries), nil
}

// finish releases the running flag so the Scheduler can be reused.
func (s *scheduler) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a returned Run leaves the Scheduler usable: Add works again, and Run
	//: re-arms every entry from the clock's reading at that moment.
	s.running = false
}

// emit publishes one decision through the caller's hook.
func (s *scheduler) emit(result coresched.ResultValue) {
	//: no hook is a working configuration — see Config.OnResult.
	if s.onResult == nil {
		//: nothing to publish to.
		return
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	//: a panic in the hook is the CALLER's bug and is deliberately not
	//: recovered: only the Job is. Hiding an observer's panic would hide the
	//: defect in the code that was supposed to be watching.
	s.onResult(result)
}
