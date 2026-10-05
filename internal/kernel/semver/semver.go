package semver

// IsValid reports whether v is a version: SemVer 2.0.0 with a leading "v",
// or one of the shorthands "vMAJOR" and "vMAJOR.MINOR".
func IsValid(v string) bool {
	_, ok := parse(v)
	//: a version is whatever parses; nothing else is checked.
	return ok
}

// Compare returns -1, 0 or +1 as v orders before, equal to or after w under
// SemVer precedence. Build metadata is ignored, so v1.0.0+a and v1.0.0+b
// compare equal.
//
// An invalid string orders below every valid version and equal to every
// other invalid string, so Compare is a total order over all strings and
// slices.SortFunc(list, Compare) puts the unreadable ones first.
func Compare(v, w string) int {
	left, leftOK := parse(v)
	right, rightOK := parse(w)
	//: validity first: every invalid string ranks below every version.
	if !leftOK || !rightOK {
		//: an invalid string below a version, and two invalid strings tied.
		return falseFirst(leftOK, rightOK)
	}
	//: MAJOR, then MINOR, then PATCH, numerically (SemVer §11.2).
	if order := compareNumbers(left.major, right.major); order != 0 {
		//: decided by the major version.
		return order
	}
	//: the minor version only matters between equal majors.
	if order := compareNumbers(left.minor, right.minor); order != 0 {
		//: decided by the minor version.
		return order
	}
	//: the patch version only matters between equal minors.
	if order := compareNumbers(left.patch, right.patch); order != 0 {
		//: decided by the patch version.
		return order
	}
	//: then the pre-release (SemVer §11.3, §11.4); build metadata never.
	return comparePrereleases(left.prerelease, right.prerelease)
}

// Prerelease returns the pre-release part of v with its leading hyphen —
// "-rc.1" for "v1.0.0-rc.1+build" — or "" when v has none: a release, a
// shorthand, or a string that is not a version, which [IsValid] tells apart.
func Prerelease(v string) string {
	parts, _ := parse(v)
	//: a string that does not parse yields empty components, so its
	//: pre-release is "" like a release's.
	return parts.prerelease
}
