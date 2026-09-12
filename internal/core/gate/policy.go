// Package gate — the policy value: which invocations are exempt, and what a
// mandated upgrade does.
package gate

import (
	"slices"
	"strings"
)

// pathSeparator joins a command path for comparison. A space cannot appear in a
// command name — the cli domain refuses one — so joining is lossless and a
// joined path can be compared by byte equality.
const pathSeparator string = " "

// PolicyValue is one product's gate policy: the commands that run unchecked,
// the commands that must never be gated, and what happens when the vendor
// mandates a newer build.
//
// It is a VALUE, copied by the caller and never mutated by this package. The
// zero value is not usable — see Validate — because none of its three fields
// has a safe default: an empty exemption list locks out recovery, and neither
// "refuse" nor "upgrade" is a guess this package may make on a vendor's behalf.
type PolicyValue struct {
	// ExemptExact lists command paths, relative to the root, that run without
	// the check — matched WHOLE, so a same-named command elsewhere in the tree
	// is not silently exempted with them.
	//
	// The empty string is the bare root invocation: a binary run with no
	// subcommand shows its help, and showing help must not require an
	// entitlement any more than `help` itself does.
	//
	// Paths are space-joined, e.g. "skill install". A command name cannot
	// contain a space, so the joining is lossless.
	ExemptExact []string
	// ExemptSubtree lists command paths whose whole subtree runs without the
	// check — the path itself and every command under it.
	//
	// Use it when the exemption is about a CAPABILITY rather than a command:
	// everything under `license` repairs the licence, so gating any of it would
	// be gating the repair. Use ExemptExact when a parent and its children
	// differ, which they usually do.
	ExemptSubtree []string
	// RecoveryPaths lists the commands an operator runs to repair a refused
	// entitlement. Every one of them MUST be exempt, and Validate refuses a
	// policy where one is not.
	//
	// It exists because the failure it prevents is silent and total: a policy
	// that gates its own repair command leaves a machine whose licence lapsed
	// with no path back short of reinstalling the binary, and nothing about the
	// policy LOOKS wrong — the exemption list is simply missing an entry.
	// Naming them separately turns that into a construction-time refusal.
	RecoveryPaths []string
	// OnUpdateRequired is what happens when the entitlement verifies but the
	// vendor mandates a newer build than this one.
	//
	// It has no default. See UpdateAction.
	OnUpdateRequired UpdateAction
}

// Exempt reports whether the command at path runs without the check.
//
// Parameters:
//   - path: the command path relative to the root, root NOT included. A nil or
//     empty path is the bare root invocation.
//
// Returns:
//   - exempt: true when the command runs unchecked.
func (p *PolicyValue) Exempt(path []string) bool {
	//: a nil policy exempts nothing, which is the refusing direction. Every
	//: accessor here tolerates one for the same reason: the code path that
	//: runs when a caller has configured nothing must not panic.
	if p == nil {
		//: nothing is exempt.
		return false
	}
	joined := strings.Join(path, pathSeparator)
	//: a whole-path match, which is the precise form.
	if slices.Contains(p.ExemptExact, joined) {
		//: exempt by exact match.
		return true
	}

	//: otherwise the command is exempt when it IS, or descends from, an
	//: exempt subtree. Descending is a prefix on a SEPARATOR boundary, never
	//: on bytes: "licensed" must not inherit "license"'s exemption.
	return slices.ContainsFunc(p.ExemptSubtree, func(root string) bool {
		return joined == root || strings.HasPrefix(joined, root+pathSeparator)
	})
}

// Validate reports every way this policy is unusable, rather than the first.
//
// A caller fixing a policy wants the whole list: the checks are independent,
// and a policy corrected one refusal at a time takes as many start-ups as it
// has faults.
//
// It returns ONE error naming them all rather than an errors.Join of several,
// and that is the difference between a promise and a delivered one: a joined
// error has no single Private detail, so errs.PrivateOf reports the first
// fault's and a structured-logging caller would still see them one at a time.
//
// Parameters: none.
//
// Returns:
//   - err: one typed refusal whose private detail names every fault, or nil.
func (p *PolicyValue) Validate() error {
	//: a nil policy is a gate nobody configured, and it fails the same way as
	//: an empty one rather than panicking.
	if p == nil {
		//: report it as the same misconfiguration.
		return misconfigured("no policy was supplied")
	}

	var faults []string
	//: an unset action is the one field with no safe guess; see UpdateAction.
	if !p.OnUpdateRequired.Valid() {
		faults = append(faults,
			"OnUpdateRequired is unset: neither refusing nor upgrading is a guess this "+
				"package may make on a vendor's behalf")
	}
	//: every recovery command must be reachable when the entitlement is what
	//: is broken, or a lapsed machine has no path back short of a reinstall.
	for _, recovery := range p.RecoveryPaths {
		if !p.Exempt(strings.Split(recovery, pathSeparator)) {
			faults = append(faults,
				"recovery path "+quote(recovery)+" is not exempt: an operator whose "+
					"entitlement was refused could not run the command that repairs it")
		}
	}
	//: a policy that exempts nothing at all cannot be repaired from inside the
	//: binary, whatever it names as recovery.
	if len(p.ExemptExact) == 0 && len(p.ExemptSubtree) == 0 {
		faults = append(faults,
			"no command is exempt: every invocation would require an entitlement, "+
				"including the ones that repair it")
	}
	//: a usable policy returns a genuine nil, not a wrapped empty list.
	if len(faults) == 0 {
		//: nothing to report.
		return nil
	}

	//: one refusal, every fault in it, so one start-up is enough to fix them.
	return misconfigured(strings.Join(faults, "; "))
}

// quote renders a path for a message, spelling the bare root invocation as
// something an operator can recognise rather than as an empty pair of quotes.
//
// Parameters:
//   - path: the space-joined command path.
//
// Returns:
//   - rendered: the path in quotes, or a phrase for the bare root.
func quote(path string) string {
	//: the empty path is the bare root, and "" would read as a missing value.
	if path == "" {
		//: name it.
		return "the bare root invocation"
	}

	//: an ordinary quoted path.
	return `"` + path + `"`
}
