// Package rlimit — the portable half of the measurement: PrepareSysProcAttr,
// which the package documents as the fail-fast call to make at Spec construction
// time. It issues no syscall, so it is the one thing here a caller could
// plausibly run in a loop, and the only question it raises is whether it is
// cheap enough not to think about.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// errSink defeats dead-code elimination on the error-returning entry points.
var errSink error

// benchLimits builds a limit set of the given resources with plausible ceilings.
func benchLimits(res ...coreproc.Resource) map[coreproc.Resource]coreproc.LimitValue {
	out := make(map[coreproc.Resource]coreproc.LimitValue, len(res))
	for _, r := range res {
		out[r] = coreproc.LimitValue{Soft: 1024, Hard: 4096}
	}
	return out
}

// requireSupported skips when the platform has no setrlimit(2) at all, so the
// numbers below are never the cost of a stub returning UnsupportedPlatform.
func requireSupported(b *testing.B) {
	b.Helper()
	if err := rlimit.PrepareSysProcAttr(benchLimits(coreproc.ResourceNoFile)); err != nil {
		b.Skipf("rlimit is not supported on this platform: %v", err)
	}
}

// BenchmarkPrepareSysProcAttr_Nil is the call a caller makes when the Spec asks
// for no limits at all — the common case, and the one that must not cost
// anything.
func BenchmarkPrepareSysProcAttr_Nil(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.PrepareSysProcAttr(nil)
	}
}

func BenchmarkPrepareSysProcAttr_One(b *testing.B) {
	requireSupported(b)
	limits := benchLimits(coreproc.ResourceNoFile)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.PrepareSysProcAttr(limits)
	}
}

func BenchmarkPrepareSysProcAttr_Four(b *testing.B) {
	requireSupported(b)
	limits := benchLimits(
		coreproc.ResourceNoFile, coreproc.ResourceCore,
		coreproc.ResourceCPU, coreproc.ResourceFSize,
	)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.PrepareSysProcAttr(limits)
	}
}

// BenchmarkPrepareSysProcAttr_Unmapped is the refusal path: the first resource
// with no platform RLIMIT_* mapping returns the bare sentinel. It is measured
// because a validator that is fast only when it succeeds is not a validator a
// caller can put on a hot path.
func BenchmarkPrepareSysProcAttr_Unmapped(b *testing.B) {
	limits := map[coreproc.Resource]coreproc.LimitValue{
		coreproc.Resource(0): {Soft: 1, Hard: 1},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.PrepareSysProcAttr(limits)
	}
}
