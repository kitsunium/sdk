package gate

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
