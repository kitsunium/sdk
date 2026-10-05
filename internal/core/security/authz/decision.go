package authz

const (
	// Abstain means this policy has no opinion about this request. It is the
	// zero value — FIRST in the iota run for exactly that reason — and it
	// grants nothing: composition preserves it, and the closure refuses it.
	Abstain Decision = iota
	// Allow means this policy permits the request. It is the ONLY value that
	// grants, and under deny-overrides it can still be defeated by a [Deny]
	// from any other policy in the composition.
	Allow
	// Deny means this policy refuses the request. It is absorbing: no [Allow]
	// anywhere in a composition can outvote it (ADR 0057 §D2).
	Deny
)

// granted is Decision.Granted's body: decl_gen.go writes Decision.Granted, from the
// design, as one call of it.
func (d Decision) granted() bool {
	//: exactly one of the three states permits, and it is not the zero one.
	return d == Allow
}

// valid is Decision.Valid's body: decl_gen.go writes Decision.Valid, from the
// design, as one call of it.
func (d Decision) valid() bool {
	//: switch rather than a range test, so adding a state forces a visit here.
	switch d {
	//: the three states the contract declares, and nothing else.
	case Abstain, Allow, Deny:
		//: one of the three declared states.
		return true
	//: every other bit pattern reached this type by conversion or corruption.
	default:
		//: anything else is out of contract.
		return false
	}
}

// String renders the decision in lower case for logs and test failures:
// "abstain", "allow", "deny". An out-of-contract value renders as "invalid",
// which is deliberately not one of the three: a log line must not be able to
// show a corrupt decision as if it were a real verdict.
func (d Decision) String() string {
	//: mirror Valid's switch so the two can never disagree about the set.
	switch d {
	//: the third state, and the zero one.
	case Abstain:
		//: no opinion.
		return "abstain"
	//: the only state that grants.
	case Allow:
		//: permitted.
		return "allow"
	//: the absorbing state.
	case Deny:
		//: refused.
		return "deny"
	//: out of contract; it must not borrow one of the three names.
	default:
		//: out of contract — never rendered as one of the three.
		return "invalid"
	}
}
