package consensus

import "github.com/kitsunium/sdk/internal/kernel/errs"

// Role is a node's part in its group.
type Role uint8

// The roles.
const (
	// RoleFollower follows a leader, or waits for one.
	RoleFollower Role = 1
	// RolePreCandidate asks whether it COULD win, without raising its term.
	RolePreCandidate Role = 2
	// RoleCandidate campaigns at a raised term.
	RoleCandidate Role = 3
	// RoleLeader leads its term.
	RoleLeader Role = 4
	// RoleLearner receives the log and votes when asked, but never campaigns.
	RoleLearner Role = 5
)

// String names the role.
func (r Role) String() string {
	switch r {
	case RoleFollower:
		return "follower"
	case RolePreCandidate:
		return "pre-candidate"
	case RoleCandidate:
		return "candidate"
	case RoleLeader:
		return "leader"
	case RoleLearner:
		return "learner"
	}
	return "unknown"
}

// Status is a node's view of its group after one turn of its event loop.
type Status struct {
	// ID is this node.
	ID NodeID
	// Role is its role.
	Role Role
	// Term is its current term.
	Term uint64
	// Leader is the leader it knows of in Term, 0 for none.
	Leader NodeID
	// Commit is the highest index it knows committed.
	Commit uint64
	// Applied is the highest index its state machine applied.
	Applied uint64
	// Log is the identity of its log.
	Log LogIdentity
	// Versions are its capabilities and the committed cluster version.
	Versions Versions
	// Timing is the committed timing contract.
	Timing TimingContract
	// NoSpace is the raised no-space alarm, 0 when none.
	NoSpace AlarmID
}

// Precondition returns the precondition an operation clearing the current
// alarm, or replacing the current contract, would carry.
func (s Status) Precondition() Precondition {
	return Precondition{Log: s.Log, Alarm: s.NoSpace, Generation: s.Timing.Generation}
}

// StopReason says why a node stopped itself. Err carries the sentinel of the
// stop; ExitCode is the process exit status the product should use.
type StopReason struct {
	// Err is the stop's error, built on its sentinel.
	Err error
	// Unmarked is set when a STORAGE_FAILED stop could write its marker
	// nowhere: the exit code is then 73.
	Unmarked bool
}

// ExitCode returns errs.ExitCodeOf of the stop's error, or 73 for a storage
// failure whose marker could not be written (ADR 0152 D4).
func (s StopReason) ExitCode() int {
	if s.Unmarked {
		return exitCantCreate
	}
	if s.Err == nil {
		return 0
	}
	return errs.ExitCodeOf(s.Err)
}

// FrameLane is the connection a frame belongs on: control, data or snapshot.
// It is carried in the two high bits of the frame's type byte, so a transport
// routes a frame of a type it does not parse.
type FrameLane uint8

// The lanes.
const (
	// LaneControl carries votes, heartbeats, their replies and TimeoutNow.
	LaneControl FrameLane = 0
	// LaneData carries entries, their acknowledgements and forwarded proposals.
	LaneData FrameLane = 1
	// LaneSnapshot carries snapshot streams.
	LaneSnapshot FrameLane = 2
)
