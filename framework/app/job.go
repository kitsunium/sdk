package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Job is scheduled work: a piece of the daemon's internal loop. It runs on
// the SDK scheduler, whose decisions about time are documented rather than
// emergent: a fire that is due while the previous run of the same job is
// still going is skipped and counted, and a missed deadline is skipped and
// counted, never caught up.
type Job = iapp.Job

// JobOption configures a job ([Service].Every, [Service].Cron):
// [Overlap], [StartingDeadline].
type JobOption = iapp.JobConfigurer

// OverlapPolicy is what a job's fire does while the job's previous run is
// still going: [OverlapSkip], [OverlapAllow] or [OverlapReplace]. It is
// the word of the job's setting <service>-job-<name>-overlap.
type OverlapPolicy = iapp.OverlapPolicy

const (
	// OverlapSkip skips the fire, counted, the previous run going on: the
	// default, since the framework cannot know that two runs of a job may
	// run at once.
	OverlapSkip OverlapPolicy = iapp.OverlapSkip
	// OverlapAllow runs the fire beside the previous run.
	OverlapAllow OverlapPolicy = iapp.OverlapAllow
	// OverlapReplace cancels the previous run's context and runs the fire
	// once that run returned: the newest fire wins, two runs never overlap.
	OverlapReplace OverlapPolicy = iapp.OverlapReplace
)

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
func Overlap(p OverlapPolicy) JobOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Overlap(p)
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
func StartingDeadline(d time.Duration) JobOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.StartingDeadline(d)
}
