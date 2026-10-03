// Package process — the running process itself: what it was built from and
// what it is doing.
package process

import (
	"runtime/debug"

	svcself "github.com/kitsunium/sdk/internal/service/proc/self"
)

// Stats is the running process at one instant: identity, scheduler settings,
// goroutines, heap, collections with their pause distribution, scheduling
// latency, and CPU time. Every count is cumulative since the process started.
// It is an alias of the service value.
type Stats = svcself.StatsValue

// Distribution is one of the Go runtime's cumulative histograms, read as
// durations: Count, and Quantile(q) as the upper bound of the bucket that
// reaches q. It is an alias of the service value.
type Distribution = svcself.DistributionValue

// BuildInfo is what the running binary was built from: the toolchain, the
// main package, the main module with its version-control stamp, and every
// dependency followed through its replacement. It is an alias of the service
// value.
type BuildInfo = svcself.BuildValue

// Module is one module of the running binary, with its release, its commit
// and its local directory told apart. It is an alias of the service value.
type Module = svcself.ModuleValue

// Self takes a snapshot of the running process. It never fails: a figure the
// platform cannot give is zero, and Stats.CPUEstimated says when CPUTime is
// the runtime's estimate rather than the kernel's count.
func Self() Stats {
	//: delegate verbatim to the service implementation.
	return svcself.ReadStats()
}

// Build describes what the running binary was built from, and reports false
// when the binary carries no build information.
func Build() (info BuildInfo, ok bool) {
	//: delegate verbatim to the service implementation.
	return svcself.ReadBuild()
}

// ParseBuild describes a BuildInfo from elsewhere — one a test builds by
// hand, or debug.ParseBuildInfo's reading of another binary — exactly as
// Build describes the running one.
func ParseBuild(info *debug.BuildInfo) BuildInfo {
	//: delegate verbatim to the service implementation.
	return svcself.ParseBuild(info)
}
