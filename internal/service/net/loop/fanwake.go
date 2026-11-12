package loop

import "sync/atomic"

// The verdicts of the secondary-wake probe (#703), as the group's log line
// names them.
const (
	// wakeUnprobed is a process whose probe never ran: no io_uring loop with
	// a secondary ring started yet, or no io_uring here.
	wakeUnprobed string = "unprobed"
	// wakePrompt is a kernel that wakes the primary's wait for a secondary
	// ring's completion.
	wakePrompt string = "prompt"
	// wakeSlow is a kernel that leaves the primary's wait to its timeout: a
	// loop with secondaries then waits fanWakeTick at most.
	wakeSlow string = "slow"
)

// secondaryWake is the process's verdict, read by the group's log line.
var secondaryWake atomic.Value

// secondaryWakeVerdict is the verdict, wakeUnprobed before the probe ran.
func secondaryWakeVerdict() string {
	v, ok := secondaryWake.Load().(string)
	//: never probed.
	if !ok {
		//: unprobed.
		return wakeUnprobed
	}
	//: as probed.
	return v
}
