// Package selfupdate — the values a self-update reports.
package selfupdate

// UpdateValue is the outcome of a version check or an install: whether a newer
// release exists, and the two versions that were compared.
//
// Available is reported separately from a version comparison the caller could
// redo, because the comparison is semver-aware and a string compare of the two
// fields would disagree with it on a prerelease.
type UpdateValue struct {
	// CurrentVersion is the version of the running binary.
	CurrentVersion string
	// LatestVersion is the version that was found, or the empty string when
	// none was.
	LatestVersion string
	// Available reports whether LatestVersion is newer than CurrentVersion.
	Available bool
}

// CandidateValue is one release candidate: a developer channel entry that is
// never what a stable check returns.
//
// All three fields are carried VERBATIM as the release host reported them. They
// are displayed to an operator choosing a candidate, never parsed and never
// compared — the ordering decision is made on Tag by semver, elsewhere.
type CandidateValue struct {
	// Tag is the release tag, e.g. "v1.9.0-rc.3".
	Tag string
	// Name is the release's human title, which a host may leave empty.
	Name string
	// CreatedAt is the RFC 3339 publication timestamp, as text.
	CreatedAt string
}
