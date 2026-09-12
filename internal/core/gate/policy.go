// Package gate — the policy value: which invocations are exempt, and what a
// mandated upgrade does.
package gate

import (
	"slices"
	"strings"
	"unicode"
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
// It takes the command path relative to the root, with the root itself NOT
// included. A nil path, an empty one, and a single empty element — which is what
// splitting an empty string yields — are all the bare root invocation.
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
// It returns one typed refusal whose private detail names every fault, or nil.
func (p *PolicyValue) Validate() error {
	//: a nil policy is a gate nobody configured, and it fails the same way as
	//: an empty one rather than panicking.
	if p == nil {
		//: report it as the same misconfiguration.
		return misconfigured("no policy was supplied")
	}

	var faults []string
	//: entries no invocation can produce, first: they make a list LOOK
	//: complete while the command it names stays gated.
	faults = append(faults, p.unreachableEntries()...)
	//: then the two lockouts, and the one field with no safe guess.
	faults = append(faults, p.lockouts()...)
	//: a usable policy returns a genuine nil, not a wrapped empty list.
	if len(faults) == 0 {
		//: nothing to report.
		return nil
	}

	//: one refusal, every fault in it, so one start-up is enough to fix them.
	return misconfigured(strings.Join(faults, "; "))
}

// unreachableEntries reports every list entry no command path can equal, so a
// policy carrying one is refused rather than quietly not applying it.
func (p *PolicyValue) unreachableEntries() []string {
	faults := unmatchable("ExemptExact", p.ExemptExact)
	faults = append(faults, unmatchable("ExemptSubtree", p.ExemptSubtree)...)
	faults = append(faults, unmatchable("RecoveryPaths", p.RecoveryPaths)...)
	//: an empty SUBTREE root is not the bare root, and it is not "everything"
	//: either: it exempts only the bare invocation, which ExemptExact already
	//: says. Refusing beats the two alternatives — silently covering one
	//: command reads as a subtree that does not work, and covering the whole
	//: tree would turn one stray entry into a gate that gates nothing.
	if slices.Contains(p.ExemptSubtree, "") {
		faults = append(faults,
			"ExemptSubtree contains the empty path: a subtree root must name a command. "+
				"The bare root invocation belongs in ExemptExact, and an empty subtree "+
				"root would either cover nothing useful or cover everything")
	}

	//: whatever was found, possibly nothing.
	return faults
}

// lockouts reports the ways this policy would leave a machine with no path back
// — plus the one field that has no safe default.
func (p *PolicyValue) lockouts() []string {
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
		//: a recovery command that is GATED is the lockout; the entry being
		//: unreachable is a different fault, reported by unreachableEntries.
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

	//: whatever was found, possibly nothing.
	return faults
}

// unmatchable reports every entry in a path list that no real command path can
// equal, so a policy carrying one is refused rather than quietly not applying.
//
// A path is built by joining command names with a single space, and a command
// name contains no whitespace at all — the cli domain refuses a name with a
// space in it, and a tab or a newline could not be typed as one either. So an
// entry carrying stray whitespace of ANY kind cannot be produced by any
// invocation, and the exemption it was written for never fires. Nothing about
// the policy looks wrong; the command it names is simply still gated.
//
// It takes field for the message and paths to check, and returns one message
// per unmatchable entry.
func unmatchable(field string, paths []string) []string {
	var faults []string
	//: every entry, because each is independently wrong or right.
	for _, path := range paths {
		//: the bare root is the one legitimate empty entry; ExemptSubtree's
		//: own refusal above is what rejects it there.
		if path == "" {
			continue
		}
		//: any whitespace that is not the single separator makes this a path
		//: no invocation can produce — a tab, a newline, a leading or trailing
		//: space, or two separators in a row.
		if strayWhitespace(path) {
			faults = append(faults,
				field+" entry "+quote(path)+" carries stray whitespace, so no command path "+
					"can equal it: the exemption it was written for never applies and the "+
					"command stays gated")
		}
	}

	//: whatever was found, possibly nothing.
	return faults
}

// strayWhitespace reports whether path carries whitespace no command path can.
//
// It takes the space-joined path and returns true when the path is unreachable.
func strayWhitespace(path string) bool {
	//: a trimmed entry that differs, or a doubled separator, is unreachable.
	if strings.TrimSpace(path) != path || strings.Contains(path, pathSeparator+pathSeparator) {
		//: unreachable.
		return true
	}

	//: and so is any whitespace that is not the separator itself — a tab or a
	//: newline survives TrimSpace in the middle of a string.
	return strings.ContainsFunc(path, func(r rune) bool {
		return unicode.IsSpace(r) && string(r) != pathSeparator
	})
}

// quote renders a path for a message, spelling the bare root invocation as
// something an operator can recognise rather than as an empty pair of quotes.
//
// It takes the space-joined command path and returns it in quotes, or a phrase
// for the bare root.
func quote(path string) string {
	//: the empty path is the bare root, and "" would read as a missing value.
	if path == "" {
		//: name it.
		return "the bare root invocation"
	}

	//: an ordinary quoted path.
	return `"` + path + `"`
}
