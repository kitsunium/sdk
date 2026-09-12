// Package gate — what a mandated upgrade does.
package gate

// UpdateAction is what the gate does when the entitlement verifies but the
// vendor mandates a newer build than the running one.
//
// The zero value is deliberately unclaimed. Every other field of PolicyValue
// has a defensible empty meaning — no exemptions, no recovery paths — but this
// one does not: refusing and upgrading are opposite answers, and a product that
// silently picked either on a vendor's behalf would be wrong for half of them.
// A binary distributed to machines an operator does not own must not replace
// itself uninvited; one distributed to a fleet that expects to track the floor
// must not stop working instead. Validate refuses the unset value rather than
// guessing (ADR 0031).
type UpdateAction uint8

const (
	// UpdateRefuse stops the invocation and reports the floor. The caller
	// tells the operator which version is required; nothing is downloaded and
	// nothing is replaced.
	//
	// This is the conservative answer and the right one when the binary may be
	// running somewhere the operator did not put it.
	UpdateRefuse UpdateAction = iota + 1
	// UpdateApply asks the caller to upgrade and re-run the invocation.
	//
	// The gate does NOT perform the upgrade — see the package doc. It reports
	// that the caller should, which keeps the network access, the signature
	// verification and the process replacement in the caller's own control
	// flow where they can be refused, logged or tested.
	//
	// A caller acting on it must upgrade AT MOST ONCE per invocation: a floor
	// no published release satisfies — a roster asking for v9.9.9 while the
	// mirror serves v1.5.10 — otherwise upgrades to no effect, re-runs, is
	// refused again and loops forever.
	UpdateApply
	// UpdateWarn runs the invocation anyway and reports the floor alongside it.
	//
	// It trades enforcement for availability on purpose, and it is the only
	// action here that lets an out-of-date build keep working. Choose it when
	// the floor is advice; choose UpdateRefuse when it is a requirement.
	UpdateWarn
)

// Valid reports whether this action was set to one of the three answers.
//
// Parameters: none.
//
// Returns:
//   - valid: false for the unclaimed zero value.
func (a UpdateAction) Valid() bool {
	//: the zero value is unclaimed, and everything past the last action is a
	//: value from a newer build of this package that this one cannot honour.
	return a >= UpdateRefuse && a <= UpdateWarn
}

// String names the action for a diagnostic.
//
// Parameters: none.
//
// Returns:
//   - name: the action's name, or "unset" for the zero value.
func (a UpdateAction) String() string {
	//: a small closed set, so a switch is the whole implementation.
	switch a {
	case UpdateRefuse:
		//: refuse and report the floor.
		return "refuse"
	case UpdateApply:
		//: ask the caller to upgrade and re-run.
		return "apply"
	case UpdateWarn:
		//: run anyway and report the floor.
		return "warn"
	default:
		//: the unclaimed zero, or a value this build does not know.
		return "unset"
	}
}
