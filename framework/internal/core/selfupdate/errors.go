// Package selfupdate — the sentinels every implementation of this domain returns.
//
// The first five are the trust chain, and their ORDER is the contract: a build
// with no vendor key cannot install at all; a manifest with no signature is not
// authenticated; a signature that does not verify ends it; only then is the
// archive's digest compared against a manifest that is now trusted. A digest
// checked against an unauthenticated manifest proves nothing, which is why
// CodeChecksumMismatch can only be reached after CodeSignatureInvalid was not.
package selfupdate

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// NoVendorKey is returned when the running build carries no vendor key. It
	// is a REFUSAL, not a degradation: without a key nothing could have
	// authenticated the release, so there is no weaker check to fall back to.
	NoVendorKey = errs.Define(CodeNoVendorKey, "NO_VENDOR_KEY",
		"this build cannot verify a release and will not install one",
		"core/selfupdate: no vendor public key was linked into this build")

	// SignatureMissing is returned when the detached signature over the checksum
	// manifest could not be fetched.
	SignatureMissing = errs.Define(CodeSignatureMissing, "SIGNATURE_MISSING",
		"the release is not signed",
		"core/selfupdate: the detached signature over the checksum manifest was absent")

	// SignatureInvalid is returned when the checksum manifest's signature does
	// not verify against the vendor key.
	SignatureInvalid = errs.Define(CodeSignatureInvalid, "SIGNATURE_INVALID",
		"the release signature did not verify",
		"core/selfupdate: the manifest signature failed ed25519 verification against the vendor key")

	// ChecksumMissing is returned when the authenticated manifest has no entry
	// for the archive being installed.
	ChecksumMissing = errs.Define(CodeChecksumMissing, "CHECKSUM_MISSING",
		"the release has no checksum for this platform",
		"core/selfupdate: the authenticated manifest carries no entry for the requested asset")

	// ChecksumMismatch is returned when the archive's digest does not match its
	// authenticated manifest entry.
	ChecksumMismatch = errs.Define(CodeChecksumMismatch, "CHECKSUM_MISMATCH",
		"the release archive does not match its checksum",
		"core/selfupdate: the archive digest differs from its authenticated manifest entry")

	// ArchiveTooLarge is returned when a release archive exceeds the size cap.
	// The cap exists because the bytes are buffered in memory before anything
	// has vouched for them.
	ArchiveTooLarge = errs.Define(CodeArchiveTooLarge, "ARCHIVE_TOO_LARGE",
		"the release archive is larger than this build accepts",
		"core/selfupdate: the archive exceeded the in-memory size cap")

	// APIBodyTooLarge is returned when a release-metadata response exceeds its
	// cap.
	APIBodyTooLarge = errs.Define(CodeAPIBodyTooLarge, "API_BODY_TOO_LARGE",
		"the release metadata is larger than this build accepts",
		"core/selfupdate: the release API response exceeded its read cap")

	// InsecureRedirect is returned when a redirect leaves HTTPS or exceeds the
	// hop bound. Following it would hand the archive fetch to a plaintext hop.
	InsecureRedirect = errs.Define(CodeInsecureRedirect, "INSECURE_REDIRECT",
		"the release host redirected somewhere this build will not follow",
		"core/selfupdate: a redirect left https or exceeded the hop bound")

	// UnexpectedStatus is returned when the release host answers with a status
	// the update path does not accept.
	UnexpectedStatus = errs.Define(CodeUnexpectedStatus, "UNEXPECTED_STATUS",
		"the release host answered unexpectedly",
		"core/selfupdate: the release host returned a status the update path does not accept")

	// DownloadFailed is returned when a download does not complete.
	DownloadFailed = errs.Define(CodeDownloadFailed, "DOWNLOAD_FAILED",
		"the release could not be downloaded",
		"core/selfupdate: the release download did not complete")

	// DevBuild is returned when a version check runs on a build with no release
	// version to compare against.
	DevBuild = errs.Define(CodeDevBuild, "DEV_BUILD",
		"this build has no version to compare against a release",
		"core/selfupdate: the running build carries no release version")

	// CandidateNotFound is returned when a requested release candidate does not
	// exist.
	CandidateNotFound = errs.Define(CodeCandidateNotFound, "CANDIDATE_NOT_FOUND",
		"no such release candidate",
		"core/selfupdate: the requested candidate tag was not found on the release host")

	// NotPrerelease is returned when a tag requested as a candidate is reported
	// as a stable release.
	NotPrerelease = errs.Define(CodeNotPrerelease, "NOT_PRERELEASE",
		"that tag is a stable release, not a candidate",
		"core/selfupdate: the requested tag is not marked prerelease")

	// DraftRelease is returned for a draft release, which has no downloadable
	// asset.
	DraftRelease = errs.Define(CodeDraftRelease, "DRAFT_RELEASE",
		"that release is still a draft",
		"core/selfupdate: the requested release is a draft and exposes no asset")

	// InvalidTag is returned for a tag that is not a valid semantic version.
	InvalidTag = errs.Define(CodeInvalidTag, "INVALID_TAG",
		"that is not a valid version tag",
		"core/selfupdate: the tag is not a valid semantic version")

	// BinaryNotInArchive is returned when a verified archive does not contain
	// the expected binary. The archive is authentic; it is simply not what was
	// expected, which is a packaging fault rather than an attack.
	BinaryNotInArchive = errs.Define(CodeBinaryNotInArchive, "BINARY_NOT_IN_ARCHIVE",
		"the release archive does not contain the expected binary",
		"core/selfupdate: the verified archive holds no entry matching the binary name")

	// UnknownArchive is returned for an asset whose container format is not
	// handled.
	UnknownArchive = errs.Define(CodeUnknownArchive, "UNKNOWN_ARCHIVE",
		"the release archive format is not supported",
		"core/selfupdate: the asset is neither a tar.gz nor a zip")

	// ElevationNotAuthorised is returned when replacing the binary needs
	// privilege the operator did not opt into. Escalating without being asked is
	// the one thing an updater must never do quietly.
	ElevationNotAuthorised = errs.Define(CodeElevationNotAuthorised, "ELEVATION_NOT_AUTHORISED",
		"replacing this binary needs privilege that was not authorised",
		"core/selfupdate: the replacement required elevation and the opt-in was absent")
)
