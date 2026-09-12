// Package entitlement - the mandatory-update floor. The roster says which
// version is the lowest allowed to run; this decides whether the binary
// asking is above it.
package entitlement

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// RequiresUpdate reports whether `current` is below the roster's floor.
//
// The comparison is SemVer, not string ordering: "v1.5.9" sorts after
// "v1.5.14" lexically, which would let exactly the builds a floor is meant
// to retire keep running.
//
// Two cases deliberately do NOT require an update:
//
//   - No floor recorded. A roster published before this field existed must
//     not lock out every client that predates it.
//   - A binary NEWER than the floor. A release candidate under test, or a
//     mirror that was rolled back, must keep working — the updater only ever
//     moves forward, so refusing a newer build would leave it with no way
//     out at all.
//
// "Cannot be ordered" is NOT one case but two, and they fail in OPPOSITE
// directions. Treating them alike is what made the floor optional:
//
//   - An unparseable FLOOR fails OPEN. A typo in the roster is the vendor's
//     mistake, and bricking every client in the field over it would be far
//     worse than a missed upgrade prompt. The licence check itself is
//     unaffected.
//   - An unparseable, absent or unstamped CURRENT fails CLOSED. "dev" used to
//     pass here on the grounds that a source build "carries no anchor either"
//     — true of the ordinary developer loop, and irrelevant to the case that
//     matters: a build that DOES carry an anchor and reports "dev" satisfied
//     the licence check while ignoring the floor entirely. The same held for
//     a caller that simply never called WithVersion, which is one forgotten
//     method call away and was already true of `license status`.
//
// The asymmetry is the point. A binary cannot demonstrate it is at or above
// a bar it cannot name, and the action that resolves it — install a published
// build — is precisely what coreent.ErrUpdateRequired already asks for. The cost is
// stated where it is paid: an anchored build whose version stamp failed is
// refused, and the ordinary unanchored developer loop never reaches this
// function at all because enforceLicense returns before Verify is called.
func RequiresUpdate(current, floor string) bool {
	floorTag := normaliseTag(floor)
	//: Nothing to enforce, and nothing that CAN be enforced, reach the same
	//: answer through the same predicate: an absent floor is not valid semver
	//: either. Absence is how every roster published before the field existed
	//: behaves; a mistyped one is the vendor's error. Both fail open.
	if !semver.IsValid(floorTag) {
		//: Nothing to compare against.
		return false
	}

	currentTag := normaliseTag(current)
	//: A binary that cannot name its own version cannot show it clears the
	//: bar. "dev" needs no case of its own — it is simply not a version, so
	//: it lands here with the empty string and every other unorderable stamp,
	//: and all of them are below a floor that IS well formed.
	if !semver.IsValid(currentTag) {
		//: Unidentifiable is below the floor.
		return true
	}

	//: Strictly below the floor is the only remaining case that requires an
	//: update: equal passes, and newer passes so a candidate build or a
	//: rolled-back mirror is not left stranded.
	return semver.Compare(currentTag, floorTag) < 0
}

// normaliseTag puts a version into the "vX.Y.Z" form golang.org/x/mod/semver
// requires, tolerating the bare "1.5.14" that a build stamp produces.
func normaliseTag(version string) string {
	trimmed := strings.TrimSpace(version)
	//: semver.IsValid rejects a tag without its leading "v"; the build
	//: stamp writes one without, so accept both spellings.
	if trimmed != "" && !strings.HasPrefix(trimmed, "v") {
		//: Add the prefix the package expects.
		return "v" + trimmed
	}
	//: Already in the expected form.
	return trimmed
}

// UpdateRequiredError is the refusal raised when a binary sits below the
// roster's floor. It carries both versions as FIELDS, not just in its
// message, because the caller has to act on the floor and not merely print
// it: after upgrading it must check that the newly installed build actually
// clears the bar.
//
// Without that, a floor the published release cannot satisfy — a roster
// asking for v9.9.9 while the mirror still serves v1.5.10 — makes the gate
// upgrade to no effect, re-execute, refuse again, and loop forever. That is
// not hypothetical: it is what the end-to-end test did before this type
// existed.
type UpdateRequiredError struct {
	// Current is the version of the binary that was refused.
	Current string
	// Required is the lowest version the roster accepts.
	Required string
}

// Error renders both versions: "from what, to what" is the only question
// anyone asks at that point.
func (e *UpdateRequiredError) Error() string {
	//: Name both ends so the message is actionable on its own.
	return fmt.Sprintf("this build is %s, the published minimum is %s", e.Current, e.Required)
}

// Unwrap ties the typed error to the sentinel so errors.Is keeps working
// through the exit-code dispatch.
func (e *UpdateRequiredError) Unwrap() error {
	//: The sentinel is what every errors.Is call in the gate matches on.
	return coreent.ErrUpdateRequired
}

// UpdateRefusal builds the error a caller reports when the floor is not met.
func UpdateRefusal(current, floor string) error {
	//: A typed error so the caller can read the floor back, not just show it.
	return &UpdateRequiredError{Current: current, Required: floor}
}
