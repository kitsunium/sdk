package scheduler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcsched "github.com/kitsunium/sdk/internal/service/scheduler"
)

// resultBuffer sizes the harness's result channel. It is generous so the
// observation hook — which the engine SERIALISES — never blocks the scheduler
// inside a test and turns a behaviour assertion into a deadlock diagnosis.
const resultBuffer int = 64

var (
	// engineOrigin is the instant every engine test starts its ManualClock at.
	// It is a real calendar instant rather than the zero time, so a stray
	// wall-clock read anywhere in the engine would produce a wildly different
	// Started stamp and fail TestTimestampsComeFromTheInjectedClock at once.
	engineOrigin = time.Date(2031, time.March, 7, 0, 0, 0, 0, time.UTC)

	// errBoom is a caller-owned failure, used to prove the engine reports a
	// job's error verbatim instead of relabelling it.
	errBoom = errors.New("boom")
)

// harness wires a ManualClock, a Scheduler and a result channel together, and
// owns the goroutine Run executes on.
//
// Every test in this file drives time through this ManualClock and
// synchronises on results and on BlockUntil. There is no time.Sleep anywhere,
// and TestSuiteNeverSleeps enforces that mechanically rather than by
// convention.
type harness struct {
	clk     *clock.ManualClock
	sched   coresched.Scheduler
	results chan coresched.ResultValue
	done    chan error
	cancel  context.CancelFunc
}

// newHarness builds a scheduler on a fresh ManualClock at engineOrigin.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		clk:     clock.NewManualClock(engineOrigin),
		results: make(chan coresched.ResultValue, resultBuffer),
		done:    make(chan error, 1),
	}
	h.sched = svcsched.New(svcsched.Config{
		Clock: h.clk,
		//: a buffered send keeps the hook non-blocking; the engine serialises
		//: calls, so the channel order is the emission order.
		OnResult: func(result coresched.ResultValue) { h.results <- result },
	})
	return h
}

// add registers an entry, failing the test if it is refused.
func (h *harness) add(t *testing.T, entry coresched.EntryValue) {
	t.Helper()
	if err := h.sched.Add(entry); err != nil {
		t.Fatalf("Add(%q) failed: %v", entry.Name, err)
	}
}

// start launches Run on its own goroutine and arranges for it to be stopped.
//
// Lifecycle: exactly one goroutine, which publishes Run's error on h.done and
// exits. h.stop — registered as a Cleanup here — cancels the context and reads
// that channel, so the goroutine never outlives the test.
func (h *harness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.sched.Run(ctx) }()
	//: every test stops the scheduler, including the ones that fail early.
	t.Cleanup(func() { h.stop(t) })
}

// stop cancels the run and waits for it to drain.
func (h *harness) stop(t *testing.T) {
	t.Helper()
	//: idempotent: a test that already stopped explicitly hits the closed path.
	if h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	if err := <-h.done; err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

// tick releases exactly one scheduling decision: it waits until the run loop
// has armed its timer, then moves the clock by d.
//
// The BlockUntil is not decoration. Without it the test races the loop's
// re-arm and would sometimes advance past two deadlines at once, which the
// engine correctly coalesces — turning a cadence assertion into a flake that
// looks like a scheduler bug.
func (h *harness) tick(d time.Duration) {
	h.clk.BlockUntil(1)
	h.clk.Advance(d)
}

// next returns the next reported decision.
func (h *harness) next(t *testing.T) coresched.ResultValue {
	t.Helper()
	//: a blocking receive. If the engine never reports, the test binary's own
	//: timeout dumps every goroutine — which is a better diagnosis than an
	//: arbitrary in-test deadline, and needs no sleep to implement.
	return <-h.results
}

// everySchedule builds a fixed-interval Schedule, failing the test if refused.
func everySchedule(t *testing.T, period time.Duration) coresched.Schedule {
	t.Helper()
	schedule, err := svcsched.Every(period)
	if err != nil {
		t.Fatalf("Every(%s) failed: %v", period, err)
	}
	return schedule
}

// TestCadenceIsExact drives three consecutive fires and pins the instant each
// one was scheduled for. No wall-clock time passes: the whole three-hour
// cadence is asserted in microseconds.
func TestCadenceIsExact(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "hourly", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	for hour := 1; hour <= 3; hour++ {
		h.tick(time.Hour)
		result := h.next(t)
		want := engineOrigin.Add(time.Duration(hour) * time.Hour)
		if !result.Scheduled.Equal(want) {
			t.Fatalf("fire %d scheduled for %s, want %s", hour, result.Scheduled, want)
		}
		if result.Missed != 0 || result.Skipped {
			t.Fatalf("fire %d: missed=%d skipped=%v, want a clean fire", hour, result.Missed, result.Skipped)
		}
	}
}

// TestTimestampsComeFromTheInjectedClock is the proof that the engine reads
// clock.Timed and never package time. On a ManualClock nothing advances unless
// a test advances it, so a single stray time.Now() anywhere on this path would
// stamp the real wall clock — a different century — and fail here.
func TestTimestampsComeFromTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "stamped", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	h.tick(time.Hour)
	result := h.next(t)
	want := engineOrigin.Add(time.Hour)
	if !result.Started.Equal(want) || !result.Finished.Equal(want) {
		t.Errorf("started=%s finished=%s, want both %s — a stray time.Now() would land in the real present",
			result.Started, result.Finished, want)
	}
}

// TestCronCadenceOnAManualClock runs the real parser against the real engine,
// so the two halves are proven to agree on what "next" means.
func TestCronCadenceOnAManualClock(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "nightly", Schedule: schedule,
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	//: engineOrigin is midnight, so the first fire is two hours away…
	h.tick(2 * time.Hour)
	first := h.next(t)
	wantFirst := time.Date(2031, time.March, 7, 2, 0, 0, 0, time.UTC)
	if !first.Scheduled.Equal(wantFirst) {
		t.Fatalf("first fire scheduled for %s, want %s", first.Scheduled, wantFirst)
	}
	//: …and the next one exactly a day later.
	h.tick(24 * time.Hour)
	second := h.next(t)
	wantSecond := time.Date(2031, time.March, 8, 2, 0, 0, 0, time.UTC)
	if !second.Scheduled.Equal(wantSecond) || second.Missed != 0 {
		t.Errorf("second fire scheduled for %s (missed %d), want %s (missed 0)",
			second.Scheduled, second.Missed, wantSecond)
	}
}

// TestMissedDeadlinesAreSkippedAndCounted pins the second arbitrage. Five
// hourly deadlines pass in one jump — a machine that slept, or a VM that was
// paused. The job runs ONCE, for the most recent deadline, and the four older
// ones are reported as missed rather than replayed.
//
// The follow-up fire is what proves nothing was queued: if the engine had
// caught up, the next decision off the channel would be one of the stale
// deadlines instead of origin+6h.
func TestMissedDeadlinesAreSkippedAndCounted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "hourly", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	h.tick(5 * time.Hour)
	result := h.next(t)
	if want := engineOrigin.Add(5 * time.Hour); !result.Scheduled.Equal(want) {
		t.Fatalf("scheduled for %s, want the LATEST missed deadline %s", result.Scheduled, want)
	}
	if result.Missed != 4 {
		t.Fatalf("missed = %d, want 4 (the deadlines at +1h..+4h)", result.Missed)
	}
	h.tick(time.Hour)
	followUp := h.next(t)
	if want := engineOrigin.Add(6 * time.Hour); !followUp.Scheduled.Equal(want) {
		t.Errorf("follow-up scheduled for %s, want %s — a stale deadline here means the engine caught up",
			followUp.Scheduled, want)
	}
	if followUp.Missed != 0 {
		t.Errorf("follow-up missed = %d, want 0", followUp.Missed)
	}
}

// TestOverlapIsSkippedByDefault pins the third arbitrage in its safe form. The
// first run is still executing when the second deadline arrives, so the second
// fire is dropped and REPORTED — the report is what stops a permanently
// skipping entry from looking like a healthy one.
func TestOverlapIsSkippedByDefault(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, resultBuffer)
	release := make(chan struct{})
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "slow", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})
	h.start(t)
	h.tick(time.Hour)
	<-started
	h.tick(time.Hour)
	skip := h.next(t)
	if !skip.Skipped {
		t.Fatalf("second fire ran; want it skipped for overlap (result %+v)", skip)
	}
	if want := engineOrigin.Add(2 * time.Hour); !skip.Scheduled.Equal(want) {
		t.Errorf("skip scheduled for %s, want %s", skip.Scheduled, want)
	}
	if !skip.Started.IsZero() || !skip.Finished.IsZero() {
		t.Errorf("skip carries run stamps %s/%s; a fire that did not run has none",
			skip.Started, skip.Finished)
	}
	close(release)
	run := h.next(t)
	if run.Skipped || !run.Scheduled.Equal(engineOrigin.Add(time.Hour)) {
		t.Errorf("first run reported as %+v, want a clean run scheduled for %s",
			run, engineOrigin.Add(time.Hour))
	}
	//: exactly one goroutine ever entered the job.
	select {
	case <-started:
		t.Error("a second copy of the job ran; overlap must be skipped by default")
	default:
	}
}

// TestOverlapCanBeAllowedExplicitly pins the opt-in. AllowOverlap is the
// caller's in-code assertion that concurrent copies of this job are safe, so
// setting it must actually produce two concurrent runs — otherwise the field
// would be a comment.
func TestOverlapCanBeAllowedExplicitly(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, resultBuffer)
	release := make(chan struct{})
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "concurrent", Schedule: everySchedule(t, time.Hour), AllowOverlap: true,
		Job: func(context.Context) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})
	h.start(t)
	h.tick(time.Hour)
	<-started
	h.tick(time.Hour)
	//: the second copy starts while the first is still blocked, which is only
	//: possible if the overlap check was skipped.
	<-started
	close(release)
	for fire := range 2 {
		result := h.next(t)
		if result.Skipped {
			t.Errorf("fire %d was skipped despite AllowOverlap", fire)
		}
	}
}

// TestJobPanicIsRecoveredAndReported pins the survivability property: one
// job's bug must not take down the process, or the other entries with it.
func TestJobPanicIsRecoveredAndReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "panicky", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { panic("job exploded") },
	})
	h.start(t)
	h.tick(time.Hour)
	first := h.next(t)
	if !kerrs.HasCode(first.Err, coresched.CodeJobPanicked) {
		t.Fatalf("err = %v, want JOB_PANICKED", first.Err)
	}
	//: the scheduler is still scheduling — the panic did not end the run.
	h.tick(time.Hour)
	second := h.next(t)
	if want := engineOrigin.Add(2 * time.Hour); !second.Scheduled.Equal(want) {
		t.Errorf("scheduler stopped after a panic: next fire %s, want %s", second.Scheduled, want)
	}
}

// TestAJobPanicCarriesTheJobsOwnStack pins that the recovered report still
// names the code that panicked. A panic recovered without its stack is a
// crash report pointing at the recover site — the engine, which did nothing
// wrong — while the job's own frame is lost; service/events, service/queue and
// service/cli already capture it on the same path. The recovered value stays a
// field and JOB_PANICKED stays the origin, so the stack changes what the
// report says, not what it is.
//
// The frame is asserted by FILE, as service/events does, because `go test`
// and Bazel spell the external test package differently while the file name
// is the same under both.
//
// Mutation: dropping the stack field from invoke failed with `stack field =
// "", want the panicking job's frame (run_external_test.go)`.
func TestAJobPanicCarriesTheJobsOwnStack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "panicky", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { panic("job exploded") },
	})
	h.start(t)
	h.tick(time.Hour)
	result := h.next(t)
	origin, ok := errors.AsType[*kerrs.Error](result.Err)
	if !ok || origin.Code() != coresched.CodeJobPanicked {
		t.Fatalf("err = %v, want JOB_PANICKED as the origin", result.Err)
	}
	fields := map[string]string{}
	for _, field := range kerrs.FieldsOf(result.Err) {
		fields[field.Key()] = field.StringValue()
	}
	if !strings.Contains(fields["stack"], "run_external_test.go") {
		t.Fatalf("stack field = %q, want the panicking job's frame (run_external_test.go)", fields["stack"])
	}
	//: and the engine frames that ran it, so it is a whole stack rather than
	//: the recover site alone.
	if !strings.Contains(fields["stack"], "scheduler/run.go") {
		t.Errorf("stack field lost the engine's frames: %q", fields["stack"])
	}
	if fields["panic"] != "job exploded" || fields["job"] != "panicky" {
		t.Errorf("panic/job fields = %q/%q, want %q/%q", fields["panic"], fields["job"], "job exploded", "panicky")
	}
}

// TestJobErrorIsReportedVerbatim pins that the engine does NOT relabel a job's
// failure. The caller's errors.Is against their own sentinel has to keep
// working, which a wrap-as-scheduler-error would break.
func TestJobErrorIsReportedVerbatim(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "failing", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return errBoom },
	})
	h.start(t)
	h.tick(time.Hour)
	first := h.next(t)
	if !errors.Is(first.Err, errBoom) {
		t.Fatalf("err = %v, want the job's own error", first.Err)
	}
	//: a failing job never stops the scheduler.
	h.tick(time.Hour)
	if second := h.next(t); !errors.Is(second.Err, errBoom) {
		t.Errorf("second fire err = %v, want the job's own error again", second.Err)
	}
}

// TestSchedulerWithNoJobsIsLegitimate pins the distinction ADR 0031 warns
// about. An empty scheduler is a service whose jobs are all behind a feature
// flag; it must run, wait, and stop cleanly. Only an unrunnable ENTRY is
// refused, and that is a different question with a different answer.
func TestSchedulerWithNoJobsIsLegitimate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.start(t)
	h.stop(t)
	//: nothing was ever armed, so no decision was ever reported.
	select {
	case result := <-h.results:
		t.Errorf("an empty scheduler reported %+v", result)
	default:
	}
}

// TestAddRefusesUnrunnableEntries is the other half of that distinction: an
// entry that could never run is refused at registration, where the caller can
// still fix it, rather than at 02:00 in production.
func TestAddRefusesUnrunnableEntries(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Every(time.Hour)
	if err != nil {
		t.Fatalf("Every failed: %v", err)
	}
	job := coresched.Job(func(context.Context) error { return nil })
	tests := []struct {
		name  string
		entry coresched.EntryValue
	}{
		{"no name", coresched.EntryValue{Schedule: schedule, Job: job}},
		{"no schedule", coresched.EntryValue{Name: "x", Job: job}},
		{"no job", coresched.EntryValue{Name: "x", Schedule: schedule}},
		{"zero value", coresched.EntryValue{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sched := svcsched.New(svcsched.Config{Clock: clock.NewManualClock(engineOrigin)})
			addErr := sched.Add(tc.entry)
			if !kerrs.HasCode(addErr, coresched.CodeInvalidEntry) {
				t.Errorf("Add = %v, want INVALID_ENTRY", addErr)
			}
		})
	}
}

// TestAddRefusesADuplicateName pins that a name identifies exactly one entry;
// otherwise every result and every error field would be ambiguous.
func TestAddRefusesADuplicateName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	entry := coresched.EntryValue{
		Name: "twice", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	}
	h.add(t, entry)
	if err := h.sched.Add(entry); !kerrs.HasCode(err, coresched.CodeDuplicateJob) {
		t.Errorf("second Add = %v, want DUPLICATE_JOB", err)
	}
}

// TestTheEntrySetIsFrozenWhileRunning pins both refusals that share the
// SCHEDULER_RUNNING sentinel: registering during a run, and starting a second
// one. The second would fire every entry twice.
func TestTheEntrySetIsFrozenWhileRunning(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "first", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	//: waiting for the armed timer proves Run has claimed the flag, without
	//: sleeping to "give it a moment".
	h.clk.BlockUntil(1)
	addErr := h.sched.Add(coresched.EntryValue{
		Name: "late", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	if !kerrs.HasCode(addErr, coresched.CodeSchedulerRunning) {
		t.Errorf("Add during Run = %v, want SCHEDULER_RUNNING", addErr)
	}
	runErr := h.sched.Run(context.Background())
	if !kerrs.HasCode(runErr, coresched.CodeSchedulerRunning) {
		t.Errorf("second Run = %v, want SCHEDULER_RUNNING", runErr)
	}
}

// TestRunDrainsInFlightJobs pins the shutdown contract: Run does not return
// while a job it started is still executing. A job that outlives its scheduler
// is work the caller believes has stopped.
func TestRunDrainsInFlightJobs(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "lingering", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})
	h.start(t)
	h.tick(time.Hour)
	<-started
	h.cancel()
	close(release)
	if err := <-h.done; err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	h.cancel = nil
	//: the job's result is already published by the time Run returns, because
	//: emit happens before the WaitGroup releases. An engine that returned on
	//: cancellation without draining would leave this channel empty.
	select {
	case result := <-h.results:
		if result.Skipped {
			t.Errorf("drained result is a skip: %+v", result)
		}
	default:
		t.Error("Run returned before the in-flight job published its result")
	}
}

// TestSchedulerIsReusableAfterRunReturns pins the lifecycle: the frozen entry
// set thaws when Run returns, so a service that stops and restarts its
// scheduler does not need a new one.
func TestSchedulerIsReusableAfterRunReturns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "first", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	h.stop(t)
	h.add(t, coresched.EntryValue{
		Name: "second", Schedule: everySchedule(t, time.Hour),
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	//: both entries are armed from the same reading, so one tick fires both.
	h.clk.BlockUntil(1)
	h.clk.Advance(time.Hour)
	names := map[string]bool{}
	for range 2 {
		names[h.next(t).Name] = true
	}
	if !names["first"] || !names["second"] {
		t.Errorf("second run fired %v, want both entries", names)
	}
}

// TestExhaustedScheduleDisarmsTheEntry pins the finite-schedule path: a
// Schedule that reports it will never fire again disarms its own entry, and
// the scheduler keeps running for everyone else.
func TestExhaustedScheduleDisarmsTheEntry(t *testing.T) {
	t.Parallel()
	only := engineOrigin.Add(time.Hour)
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name: "once",
		Schedule: func(after time.Time) (time.Time, bool) {
			//: one fire, then exhausted.
			if after.Before(only) {
				return only, true
			}
			return time.Time{}, false
		},
		Job: func(context.Context) error { return nil },
	})
	h.start(t)
	h.tick(time.Hour)
	if result := h.next(t); !result.Scheduled.Equal(only) {
		t.Fatalf("fired for %s, want %s", result.Scheduled, only)
	}
	//: the scheduler is now idle but still alive; stopping it must be clean,
	//: which h.stop asserts through the Cleanup.
	h.stop(t)
	select {
	case result := <-h.results:
		t.Errorf("an exhausted schedule fired again: %+v", result)
	default:
	}
}

// TestNonAdvancingScheduleDisarmsInsteadOfSpinning pins the liveness guard. A
// downstream Schedule that returns the SAME instant forever breaks the port's
// strictly-increasing contract; the engine treats it as exhausted so one
// caller's bug costs one entry, not the whole scheduler.
//
// Without the guard this test does not fail — it HANGS, inside the
// missed-deadline walk, until the test binary's timeout dumps the goroutines.
// That is the failure mode it exists to prevent.
func TestNonAdvancingScheduleDisarmsInsteadOfSpinning(t *testing.T) {
	t.Parallel()
	stuck := engineOrigin.Add(time.Hour)
	h := newHarness(t)
	h.add(t, coresched.EntryValue{
		Name:     "stuck",
		Schedule: func(time.Time) (time.Time, bool) { return stuck, true },
		Job:      func(context.Context) error { return nil },
	})
	h.start(t)
	h.tick(time.Hour)
	if result := h.next(t); !result.Scheduled.Equal(stuck) {
		t.Fatalf("fired for %s, want %s", result.Scheduled, stuck)
	}
	h.stop(t)
}

// TestEveryRefusesANonPositivePeriod pins the ADR 0031 refusal on the
// interval schedule: a period of zero is not a fast schedule, it is a busy
// loop, and no substitute value would be anything but a guess.
func TestEveryRefusesANonPositivePeriod(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		period time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedule, err := svcsched.Every(tc.period)
			if !kerrs.HasCode(err, svcsched.CodeInvalidInterval) {
				t.Errorf("Every(%s) = %v, want INVALID_INTERVAL", tc.period, err)
			}
			if schedule != nil {
				t.Errorf("Every(%s) returned a non-nil Schedule alongside its error", tc.period)
			}
		})
	}
}

// TestEveryMeasuresFromTheDueInstant pins that the cadence does not drift with
// a slow job: the next fire is one period after the previous DUE instant, not
// after the previous completion.
func TestEveryMeasuresFromTheDueInstant(t *testing.T) {
	t.Parallel()
	schedule := everySchedule(t, 30*time.Minute)
	cursor := engineOrigin
	for step := 1; step <= 4; step++ {
		next, ok := schedule(cursor)
		if !ok {
			t.Fatalf("step %d: schedule exhausted", step)
		}
		want := engineOrigin.Add(time.Duration(step) * 30 * time.Minute)
		if !next.Equal(want) {
			t.Fatalf("step %d: next = %s, want %s", step, next, want)
		}
		cursor = next
	}
}
