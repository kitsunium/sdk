// Package profiling — range 0.3.89.*, allocated to the engine (ADR 0121
// service/observe/profiling block) and declared here since ADR 0160: the
// engine returns these and declares none.
//
// Package profiling — declares the sentinel *errs.Error outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No refusal quotes the profile or the dump it refused: both describe the
// process's code, its files and what its goroutines were doing.
package profiling

// httpBadRequest is 400: the window asked for is the caller's to fix.
const httpBadRequest int = 400

// httpConflict is 409: the profiler is taken; asking again later works.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up; nothing is wrong with the ask.
const httpUnavailable int = 503
