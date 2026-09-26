// Package profiling — range 0.3.89.* (ADR 0121 service/profiling block).
package profiling

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.89.0 - 0.3.89.255

// CodeWindowInvalid identifies a CPU capture window that is not positive or
// exceeds MaxCPUWindow.
const CodeWindowInvalid errs.Code = 0x00_03_59_01 // 0.3.89.1

// CodeProfilerBusy identifies a CPU capture refused because the process's one
// CPU profiler is already running.
const CodeProfilerBusy errs.Code = 0x00_03_59_02 // 0.3.89.2

// CodeCaptureCanceled identifies a CPU capture whose context ended before its
// window.
const CodeCaptureCanceled errs.Code = 0x00_03_59_03 // 0.3.89.3

// CodeCaptureFailed identifies a profile the runtime could not write.
const CodeCaptureFailed errs.Code = 0x00_03_59_04 // 0.3.89.4

// CodeProfileMalformed identifies bytes that are not a well-formed pprof
// profile.
const CodeProfileMalformed errs.Code = 0x00_03_59_05 // 0.3.89.5

// CodeProfileTooLarge identifies a profile over MaxProfileBytes, compressed or
// decompressed.
const CodeProfileTooLarge errs.Code = 0x00_03_59_06 // 0.3.89.6

// CodeSampleTypeMissing identifies a fold asked for a sample type the profile
// does not measure.
const CodeSampleTypeMissing errs.Code = 0x00_03_59_07 // 0.3.89.7
