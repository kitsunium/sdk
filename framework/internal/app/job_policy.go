package app

import (
	"time"
)

// A job's overlap and starting deadline, as settings (issue #483, part 4):
// a job ([Service.Every], [Service.Cron]) declares, in its service, part 1's
// two settings (policy.go) — the code's value the default, config/<env>.*
// and <PRODUCT>_<NAME> overriding it, a key nobody declares refused:
//
//	<service>-job-<name>-overlap            skip, allow or replace
//	<service>-job-<name>-starting-deadline  how late a fire may still start; 0 is no bound
//
// The SDK's scheduler runs them (scheduler.Entry's Overlap and
// StartingDeadline, ADR 0184), through the jobs subsystem: the core names a
// policy by its word (plug.JobEntryValue) and imports none of the SDK's
// scheduler. A job at an interval is not jittered: the SDK's scheduler has
// no jitter, a jitter per host being deferred by ADR 0184.

// The knobs of a job, each a suffix of its setting's name.
const (
	knobOverlap          = "overlap"
	knobStartingDeadline = "starting-deadline"
)

// unknownOverlap is the number of a word that names no policy: the
// scheduler refuses it, the job's entry with it.
const unknownOverlap uint8 = 255

// OverlapPolicy is what a job's fire does while the job's previous run is
// still going: [OverlapSkip], [OverlapAllow] or [OverlapReplace]. It is
// the word of the job's setting <service>-job-<name>-overlap.
type OverlapPolicy string

// The overlap policies.
const (
	// OverlapSkip skips the fire, counted, the previous run going on: the
	// default, since the framework cannot know that two runs of a job may
	// run at once.
	OverlapSkip OverlapPolicy = "skip"
	// OverlapAllow runs the fire beside the previous run.
	OverlapAllow OverlapPolicy = "allow"
	// OverlapReplace cancels the previous run's context and runs the fire
	// once that run returned: the newest fire wins, two runs never overlap.
	OverlapReplace OverlapPolicy = "replace"
)

// jobOption is a JobConfigurer.
type jobOption func(k *jobKnobs)

// jobConfigure sets the option on what it configures.
func (f jobOption) jobConfigure(k *jobKnobs) { f(k) }

// Overlap says what a fire of the job does while its previous run is still
// going: [OverlapSkip], the default, skips it and counts it; [OverlapAllow]
// runs it beside; [OverlapReplace] cancels the previous run's context and
// runs it once that run returned. Any other word stops the start.
//
// p is the default of the job's setting <service>-job-<name>-overlap, which
// config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Overlap(p OverlapPolicy) JobConfigurer {
	return jobOption(func(k *jobKnobs) { k.overlap = string(p) })
}

// StartingDeadline bounds how late a fire of the job may still start: a
// scheduler that comes to a fire more than d after its instant — a machine
// that slept, a run that held the slot — does not run it, and counts it
// missed, as Kubernetes' startingDeadlineSeconds does. The default, zero, is
// no bound: the latest fire missed runs however late, never more than one.
// A negative d stops the start.
//
// d is the default of the job's setting
// <service>-job-<name>-starting-deadline, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func StartingDeadline(d time.Duration) JobConfigurer {
	return jobOption(func(k *jobKnobs) { k.deadline = d })
}

// jobKnobs are a job's overlap and starting deadline: the code's values, and
// the settings that override them once its declaration declared them.
type jobKnobs struct {
	overlap     string
	deadline    time.Duration
	overlapFor  *SettingService[string]
	deadlineFor *SettingService[time.Duration]
}

// defaultJobKnobs are a job's knobs until an option says otherwise: a fire
// that overlaps is skipped, a late one runs however late.
func defaultJobKnobs() jobKnobs {
	return jobKnobs{overlap: string(OverlapSkip)}
}

// declare declares the job n's settings in svc.
func (k *jobKnobs) declare(svc *Service, n *nodeBase) {
	k.overlapFor = declarePolicySetting(svc, n, knobOverlap, k.overlap, checkOverlap())
	k.deadlineFor = declarePolicySetting(svc, n, knobStartingDeadline, k.deadline, checkStartingDeadline())
}

// overlapIn is the job's overlap policy in the app that runs it.
func (k *jobKnobs) overlapIn() OverlapPolicy {
	return OverlapPolicy(knobValue(k.overlapFor, k.overlap))
}

// deadlineIn is the job's starting deadline in the app that runs it.
func (k *jobKnobs) deadlineIn() time.Duration { return knobValue(k.deadlineFor, k.deadline) }

// number is the policy as plug.JobEntryValue numbers it, the SDK
// scheduler's: 0 skip, 1 allow, 2 replace, and unknownOverlap for a word
// that names none — which a setting's check refuses first, when the job
// declared it.
func (p OverlapPolicy) number() uint8 {
	switch p {
	case OverlapSkip:
		return 0
	case OverlapAllow:
		return 1
	case OverlapReplace:
		return 2
	}
	return unknownOverlap
}

// checkOverlap refuses a policy the scheduler does not know.
func checkOverlap() func(v any) phrase {
	return judged(func(s string) bool {
		p := OverlapPolicy(s)
		return p == OverlapSkip || p == OverlapAllow || p == OverlapReplace
	}, func() phrase { return say("policy.overlap") })
}

// checkStartingDeadline refuses a negative deadline.
func checkStartingDeadline() func(v any) phrase {
	return judged(func(d time.Duration) bool { return d >= 0 }, func() phrase { return say("policy.starting-deadline") })
}
