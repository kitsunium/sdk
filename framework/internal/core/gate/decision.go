// Package gate — what the gate decided, and why.
package gate

// Outcome is what the caller should do with an invocation.
//
// The zero value is unclaimed for the same reason UpdateAction's is: a decision
// nobody made must not read as "allow". A caller switching on this and falling
// through its cases gets OutcomeUnset, which is the refusing direction.
type Outcome uint8

const (
	// OutcomeAllow runs the invocation. Either it was exempt, or the
	// entitlement verified and no floor stood in the way.
	OutcomeAllow Outcome = iota + 1
	// OutcomeRefuse stops the invocation. DecisionValue.Cause carries the
	// refusal the verifier produced, so the caller maps it to an exit status
	// and an operator-facing sentence with errs.CodeOf or errors.Is.
	OutcomeRefuse
	// OutcomeUpgrade asks the caller to upgrade and re-run this invocation.
	//
	// Reached only when the entitlement verified, the vendor mandates a newer
	// build, and the policy said UpdateApply. A caller that cannot upgrade
	// treats it as OutcomeRefuse with the same Cause.
	OutcomeUpgrade
)

// String names the outcome for a diagnostic, spelling the unclaimed zero value
// "unset" rather than inventing a name for it.
func (o Outcome) String() string {
	//: a small closed set, so a switch is the whole implementation.
	switch o {
	case OutcomeAllow:
		//: run it.
		return "allow"
	case OutcomeRefuse:
		//: stop, and report Cause.
		return "refuse"
	case OutcomeUpgrade:
		//: upgrade, then re-run.
		return "upgrade"
	default:
		//: the unclaimed zero, or a value this build does not know.
		return "unset"
	}
}

// DecisionValue is what the gate concluded about one invocation.
//
// It is a value and carries no method that acts: the caller performs whatever
// Outcome names. Nothing here ends a process, opens a socket or writes a file.
type DecisionValue struct {
	// Outcome is what to do.
	Outcome Outcome
	// Cause is the refusal the verifier produced, or nil.
	//
	// It is carried rather than re-labelled so the caller keeps everything the
	// domain said: errors.Is answers on entitlement's sentinels and
	// errs.CodeOf on its codes, through this field, exactly as if the caller
	// had held the verifier's error itself. A gate that summarised the cause
	// would force every consumer to match on text.
	//
	// Non-nil with OutcomeUpgrade too: the floor refusal is what SAYS which
	// version is required, and a caller that upgrades needs to read it.
	Cause error
	// Exempt records that this invocation was never checked, as opposed to
	// checked and allowed.
	//
	// The two are the same Outcome and a very different fact. A daemon that
	// seeds a watchdog from a verification must not seed it from an exemption:
	// no verification happened, so there is no grant, no deadline and nothing
	// to age against.
	Exempt bool
	// FloorUnmet records that the vendor mandates a newer build, whatever the
	// policy decided to do about it.
	//
	// It is separate from Outcome because UpdateWarn allows the invocation and
	// the caller still has to say so. Reading Outcome alone would lose that.
	FloorUnmet bool
}
