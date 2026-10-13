package consensus

import "time"

// TimingContract is the set of timings every member must agree on for leases
// and vote refusals to be sound. The committed contract entry is its ONLY
// authority; a node's own configuration is merely the proposal used at
// Bootstrap.
type TimingContract struct {
	// Generation counts the committed contracts, 1 at founding.
	Generation uint64
	// HeartbeatInterval is how often the leader confirms itself.
	HeartbeatInterval time.Duration
	// ElectionTimeoutMin is the lower bound of the randomized election
	// timeout, and the vote-refusal window.
	ElectionTimeoutMin time.Duration
	// ElectionTimeoutMax is its upper bound, exclusive.
	ElectionTimeoutMax time.Duration
	// MaxClockDrift is the largest rate difference between two clocks, as a
	// fraction in (0, 1). Zero refuses lease reads.
	MaxClockDrift float64
}
