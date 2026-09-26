// Package profiling — declares the sentinel *errs.Error outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No refusal quotes the profile or the dump it refused: both describe the
// process's code, its files and what its goroutines were doing.
package profiling

import "github.com/kitsunium/sdk/internal/kernel/errs"

// httpBadRequest is 400: the window asked for is the caller's to fix.
const httpBadRequest int = 400

// httpConflict is 409: the profiler is taken; asking again later works.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up; nothing is wrong with the ask.
const httpUnavailable int = 503

var (
	// WindowInvalid refuses a CPU window that is not positive — a profile
	// of no time is empty by construction — or that exceeds MaxCPUWindow.
	WindowInvalid = errs.Define(CodeWindowInvalid, "WINDOW_INVALID",
		"A CPU profile needs a window longer than zero and at most five minutes",
		"service/profiling: CaptureCPU was given a window outside (0, MaxCPUWindow]; the window field says what",
		errs.WithHTTPStatus(httpBadRequest))

	// ProfilerBusy refuses a CPU capture while the process's one CPU profiler
	// is running — started by another capture, or by anyone's
	// pprof.StartCPUProfile. It is not queued: the caller decides whether to
	// wait.
	ProfilerBusy = errs.Define(CodeProfilerBusy, "PROFILER_BUSY",
		"A CPU profile is already being recorded",
		"service/profiling: pprof.StartCPUProfile refused: the runtime's CPU profiler is in use; the runtime's own error is joined",
		errs.WithHTTPStatus(httpConflict))

	// CaptureCanceled is a CPU capture whose context ended before the window
	// did. The profiler is stopped and nothing is returned: a partial window
	// would read as the whole one. The context's error is joined.
	CaptureCanceled = errs.Define(CodeCaptureCanceled, "CAPTURE_CANCELED",
		"The profile was abandoned before its window ended",
		"service/profiling: the context ended during CaptureCPU's window; the profiler was stopped and the samples dropped",
		errs.WithHTTPStatus(httpUnavailable))

	// CaptureFailed wraps an error runtime/pprof returned while writing a
	// profile: the heap, or the goroutines.
	CaptureFailed = errs.Define(CodeCaptureFailed, "CAPTURE_FAILED",
		"The profile could not be written",
		"service/profiling: runtime/pprof could not write the profile; the profile field names it")

	// ProfileMalformed refuses bytes that are not a well-formed pprof
	// profile: a truncated field, a wire type a field cannot have, an index
	// into the string, function or location table that points nowhere, a
	// sample whose values do not match the sample types, a gzip stream that
	// does not inflate. The field says which part; the input is never quoted.
	ProfileMalformed = errs.Define(CodeProfileMalformed, "PROFILE_MALFORMED",
		"That is not a well-formed pprof profile",
		"service/profiling: the profile did not decode; the reading field names the part")

	// ProfileTooLarge refuses a profile over MaxProfileBytes, before reading
	// it or once it inflates past the bound.
	ProfileTooLarge = errs.Define(CodeProfileTooLarge, "PROFILE_TOO_LARGE",
		"The profile is too large to read",
		"service/profiling: the profile, compressed or inflated, exceeds MaxProfileBytes")

	// SampleTypeMissing refuses a fold asked for a sample type the profile
	// does not measure — "cpu" of a heap profile.
	SampleTypeMissing = errs.Define(CodeSampleTypeMissing, "SAMPLE_TYPE_MISSING",
		"The profile does not measure that",
		"service/profiling: FoldConfig.SampleType names no sample type of the profile; the sample_type field says which")
)
