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
