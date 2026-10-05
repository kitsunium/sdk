// Package semver — the parser: a version cut into what precedence reads, in
// one pass over its bytes and without a copy of any of them.
//
// Package semver — Go pseudo-versions: the three shapes the modules reference
// defines, recognised by where their stamp sits, and the revision and time
// that stamp carries.
//
// Package semver orders and reads version strings under Semantic Versioning
// 2.0.0, written the way Go writes them: with a leading "v". It is a kernel
// primitive — stdlib-only, and domain-neutral down to the signatures: every
// function takes a version string and answers about it, with no release, no
// update and no product anywhere in the API.
//
// # The grammar
//
// A version is
//
//	vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]
//
// where MAJOR, MINOR and PATCH are decimal numbers without a leading zero,
// PRERELEASE and BUILD are non-empty lists of dot-separated identifiers made of
// ASCII letters, digits and hyphens, and a pre-release identifier made of
// digits only carries no leading zero either. That is SemVer 2.0.0 with Go's
// two departures from it, the same two golang.org/x/mod/semver makes: the "v"
// is required, and "vMAJOR" and "vMAJOR.MINOR" are accepted as shorthands for
// "vMAJOR.0.0" and "vMAJOR.MINOR.0" — with neither a pre-release nor build
// metadata, which only a full version may carry.
//
// # Precedence
//
// [Compare] orders two versions as SemVer §11 does: by MAJOR, MINOR and PATCH
// numerically, then a release above every one of its pre-releases, then
// pre-release identifiers left to right — numbers numerically, other
// identifiers in ASCII order, a number below any other identifier, and a
// longer list above a shorter one it extends. Build metadata takes no part, so
// two versions differing only in it compare equal while remaining two strings.
//
// Numbers are compared as decimal strings — the longer is the larger, then
// digit by digit — so a component past the range of uint64 still orders
// exactly, and nothing is converted.
//
// # Invalid input is an answer, not an error
//
// No function here returns an error. [IsValid] says whether a string is a
// version; [Prerelease] of a string that is not one is "", as it is of a
// release; and [Compare] places every invalid string below every valid one and
// equal to every other invalid one, so a list sorted with it is totally
// ordered and the strings it cannot read come first. That is x/mod's contract,
// kept so a call site moves from one package to the other unchanged.
//
// # What is not here
//
// The six functions the SDK calls, and nothing else. There is no Sort:
// slices.SortFunc(list, semver.Compare) is one, and slices.SortStableFunc keeps
// versions of equal precedence — one version with different build metadata —
// in the order they came. There is no Max, which x/mod itself deprecated. And
// there is no Canonical, Major, MajorMinor or Build: nothing calls them, and
// each is an addition the day something does.
//
// # Pseudo-versions
//
// A Go pseudo-version names a commit rather than a release, in one of the
// three shapes the Go modules reference defines, each optionally followed by
// build metadata such as "+incompatible":
//
//	vX.0.0-yyyymmddhhmmss-abcdefabcdef         no earlier tag
//	vX.Y.Z-0.yyyymmddhhmmss-abcdefabcdef       after the release vX.Y.(Z-1)
//	vX.Y.Z-pre.0.yyyymmddhhmmss-abcdefabcdef   after the pre-release vX.Y.Z-pre
//
// [IsPseudoVersion] recognises one, [PseudoVersionRev] reads the revision it
// names and [PseudoVersionTime] the commit time it carries. To [Compare], a
// pseudo-version is an ordinary pre-release, which is what places it between
// the tags around it.
//
// # Cost
//
// Every reading is a substring of its input, found in one pass over its bytes,
// so nothing here allocates; BENCH.md has the figures, and a race-off
// allocation gate pins the claim.
package semver
