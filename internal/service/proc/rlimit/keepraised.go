package rlimit

import (
	"sync"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// keepRaised runs keepRaisedNoFile once per process and memoises its outcome:
// once the runtime's restore is off, nothing remains to switch off.
var keepRaised = sync.OnceValues(keepRaisedNoFile)

// KeepRaisedNoFile makes every child this process spawns from now on inherit
// its raised open-file soft limit. The Go runtime raises its own soft
// RLIMIT_NOFILE to the hard limit at start and, by design, puts the original soft
// limit back in every child it forks — 1024 on a stock Linux, 256 on macOS —
// until the program sets RLIMIT_NOFILE itself. KeepRaisedNoFile sets it to the
// value in force, which ends that restore without a trampoline, and returns that
// value. It acts once per process; a later call returns the first call's
// outcome. A failing getrlimit or setrlimit is RlimitFailed; off Unix it is
// UnsupportedPlatform.
func KeepRaisedNoFile() (kept coreproc.LimitValue, err error) {
	//: the first call's outcome, for this call and every later one.
	return keepRaised()
}
