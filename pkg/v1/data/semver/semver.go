//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/data/semver .

// Package semver orders and reads version strings under Semantic Versioning
// 2.0.0, written the way Go writes them — with a leading "v" — and recognises
// Go pseudo-versions. It is the SDK's replacement for golang.org/x/mod/semver
// and the pseudo-version functions of golang.org/x/mod/module, with the same
// names and the same answers, and no module outside the standard library
// (ADR 0156 §4, published by ADR 0159 §4).
//
//	semver.IsValid("v1.2.3-rc.1+build.5")             // true
//	semver.Compare("v1.9.12", "v1.10.0")              // -1: numbers, not text
//	semver.Compare("v1.0.0-beta.11", "v1.0.0-beta.2") // 1: 11 > 2
//	semver.Prerelease("v1.0.0-rc.1+build")            // "-rc.1"
//	slices.SortFunc(tags, semver.Compare)             // unreadable strings first
//
// # The grammar
//
// A version is vMAJOR.MINOR.PATCH, optionally followed by -PRERELEASE and then
// +BUILD: numbers without a leading zero, and dot-separated identifiers of
// ASCII letters, digits and hyphens, a pre-release identifier made of digits
// only carrying no leading zero either. As in x/mod, the "v" is required, and
// "vMAJOR" and "vMAJOR.MINOR" are shorthands for "vMAJOR.0.0" and
// "vMAJOR.MINOR.0" — with neither a pre-release nor build metadata.
//
// # Precedence
//
// [Compare] follows SemVer §11: MAJOR, MINOR and PATCH numerically, a release
// above every one of its pre-releases, then pre-release identifiers left to
// right — numbers numerically, other identifiers in ASCII order, a number
// below any other identifier, a longer list above a shorter one it extends.
// Build metadata takes no part. Numbers are compared as decimal strings, so a
// component past the range of uint64 still orders exactly.
//
// # Invalid input is an answer, not an error
//
// Nothing here returns an error. [IsValid] says whether a string is a version.
// [Compare] places every invalid string below every version and equal to every
// other invalid string, so it is a total order slices.SortFunc can use, and
// [Prerelease] of an invalid string is "", as it is of a release.
//
// # Pseudo-versions
//
// A Go pseudo-version names a commit rather than a release:
//
//	vX.0.0-yyyymmddhhmmss-abcdefabcdef         no earlier tag
//	vX.Y.Z-0.yyyymmddhhmmss-abcdefabcdef       after the release vX.Y.(Z-1)
//	vX.Y.Z-pre.0.yyyymmddhhmmss-abcdefabcdef   after the pre-release vX.Y.Z-pre
//
// each optionally followed by build metadata such as "+incompatible".
// [IsPseudoVersion] recognises one; [PseudoVersionRev] and [PseudoVersionTime]
// read the revision and the commit time it carries, answering false where
// x/mod answered an error. To [Compare] a pseudo-version is an ordinary
// pre-release, which is what places it between the tags around it.
//
// # What is not here
//
// Six functions, the ones the SDK uses. There is no Sort —
// slices.SortFunc(list, semver.Compare) is one — no Max, which x/mod itself
// deprecated, and no Canonical, Major, MajorMinor or Build: each is an
// addition the day something needs it.
//
// Nothing here allocates.
package semver

import (
	"time"

	ksemver "github.com/kitsunium/sdk/internal/kernel/semver"
)

// IsValid reports whether v is a version: SemVer 2.0.0 with a leading "v",
// or one of the shorthands "vMAJOR" and "vMAJOR.MINOR".
func IsValid(v string) bool {
	//: the kernel owns the grammar; this facade only forwards.
	return ksemver.IsValid(v)
}

// Compare returns -1, 0 or +1 as v orders before, equal to or after w under
// SemVer precedence. Build metadata is ignored, so v1.0.0+a and v1.0.0+b
// compare equal. An invalid string orders below every version and equal to
// every other invalid string.
func Compare(v, w string) int {
	//: the kernel owns precedence; this facade only forwards.
	return ksemver.Compare(v, w)
}

// Prerelease returns the pre-release part of v with its leading hyphen —
// "-rc.1" for "v1.0.0-rc.1+build" — or "" when v has none: a release, a
// shorthand, or a string that is not a version, which [IsValid] tells apart.
func Prerelease(v string) string {
	//: the kernel owns the grammar; this facade only forwards.
	return ksemver.Prerelease(v)
}

// IsPseudoVersion reports whether v is a Go pseudo-version, in any of its
// three shapes, with or without build metadata. It recognises the shape: a
// stamp whose digits are no real instant still makes one, and
// [PseudoVersionTime] is what reports that the time does not read.
func IsPseudoVersion(v string) bool {
	//: the kernel owns the grammar; this facade only forwards.
	return ksemver.IsPseudoVersion(v)
}

// PseudoVersionRev returns the revision a pseudo-version names — by the
// toolchain's convention a commit's first twelve hexadecimal digits — and
// reports whether v is a pseudo-version at all.
func PseudoVersionRev(v string) (string, bool) {
	//: the kernel owns the grammar; this facade only forwards.
	return ksemver.PseudoVersionRev(v)
}

// PseudoVersionTime returns the commit time a pseudo-version carries, in UTC
// and to the second, and reports whether it could be read: false when v is
// not a pseudo-version, or when its fourteen digits are no real instant.
func PseudoVersionTime(v string) (time.Time, bool) {
	//: the kernel owns the grammar; this facade only forwards.
	return ksemver.PseudoVersionTime(v)
}
