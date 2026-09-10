// Package authz — hosts Decision, the three-valued verdict a Policy returns.
package authz

// Decision is what a [Policy] answers: it permits, it refuses, or it has no
// opinion. Three states, not two.
//
// # Why the third state exists
//
// Most policies only know about part of the system. A policy that grants
// "orders:write" to a role has nothing to say about a request to read a
// profile — and it must be able to SAY that. Folding "no opinion" into either
// of the other two breaks something:
//
//   - Folded into [Allow], a policy authorizes every request it does not
//     recognise. That is the hole.
//   - Folded into [Deny], a policy vetoes every request the OTHER policies
//     were meant to grant, because refusal is absorbing (see ADR 0057 §D2).
//     That is the outage, and the usual repair for it is to make the combiner
//     permissive — which reintroduces the hole one layer up.
//
// [Abstain] costs one enum value and removes both.
//
// # The zero value
//
// The zero Decision is [Abstain], never [Allow]. A Policy that forgets to set
// its result therefore says "I have no opinion", and the request is refused by
// the closure in internal/service/authz.Check — not permitted (which would be
// a vulnerability) and not made to veto every other policy (which would be an
// outage from a forgotten assignment). This is ADR 0031 applied to a FUNC
// port: a func type has no constructor to refuse in, so the safe value has to
// be the zero one.
//
// A Decision outside the three named constants is a defect in whoever produced
// it. The SDK treats it as [Deny] and reports [PolicyMisconfigured]; it is
// never treated as permission. Use [Decision.Valid] to check, and
// [Decision.Granted] — never `!= Deny` — to read a verdict.
type Decision uint8

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

// Granted reports whether d permits the request. It is true for [Allow] and
// for nothing else.
//
// It exists so that no call site has to spell the test itself. `d != Deny` is
// the same expression with an extra character and a security hole in it: it is
// true for [Abstain], so it authorizes every request no policy recognised, and
// it is true for a corrupt Decision value as well.
func (d Decision) Granted() bool {
	//: exactly one of the three states permits, and it is not the zero one.
	return d == Allow
}

// Valid reports whether d is one of the three named states. A Decision that is
// not is a defect in the policy that produced it — a numeric conversion, a
// zeroed struct field read as a Decision, a value from a future version of a
// caller's own code — and the SDK refuses on it rather than guessing.
func (d Decision) Valid() bool {
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
