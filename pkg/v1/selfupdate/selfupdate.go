//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/selfupdate .

// Package selfupdate replaces the running binary with a newer signed release.
//
// It is the thin public facade over internal/service/selfupdate: the value types
// and ports are aliases of the core selfupdate types and the functions delegate
// straight to the service implementation.
//
// # The order is the security property
//
// The hard part of a self-update is not the download. It is the order in which a
// candidate becomes trusted, and this package fixes it:
//
//  1. a detached ed25519 signature over the checksum manifest, verified against
//     the vendor key linked into the build;
//  2. the archive's SHA-256 against that now-authenticated manifest;
//  3. only then does anything touch the disk.
//
// A digest checked against an unauthenticated manifest proves nothing — an
// attacker who can substitute the archive can substitute the manifest beside it.
// Reversing steps 1 and 2 therefore turns the whole chain into decoration, which
// is why the order is asserted by the suite rather than left to a comment.
//
// A build with no vendor key installs NOTHING. That direction is deliberate: the
// alternative, skipping verification when no key is present, makes the security
// property depend on a build flag nobody checks.
//
// # Usage
//
//	src := selfupdate.Source{
//		Owner:      "acme",
//		StableRepo: "widget",
//		Product:    "widget",
//	}
//	svc := selfupdate.New(version, src).WithVendorKey(vendorKey)
//
//	info, err := svc.CheckForUpdate()
//	if err != nil { /* errs.HasCode(err, selfupdate.CodeDevBuild) … */ }
//	if info.Available {
//		_, err = svc.Upgrade()
//	}
//
// # Consent is separate from the upgrade, and escalation is separate again
//
// An explicit upgrade command is a deliberate act and needs no permission. An
// upgrade a caller did NOT ask for — a version floor enforced mid-run — does,
// and Source.AuthoriseUnattendedUpgrade is where that is decided: an explicit
// environment value settles it in either direction, otherwise a human is asked,
// otherwise the answer is no. Silence is not permission.
//
// Replacing a binary in a directory the user cannot write needs a SECOND opt-in.
// The two are deliberately different variables: opting into unattended upgrades
// must not silently grant privilege escalation.
//
// Both variable names are derived from Source.Product — `widget` yields
// WIDGET_AUTO_UPGRADE and WIDGET_ALLOW_SUDO — by uppercasing and folding
// punctuation to underscore.
//
// # Two limits a caller must know before relying on this
//
// **A compromised release host can serve an OLDER signed release.** The manifest
// is signed, but it carries the version-independent asset name and no signed
// release tag, so a host that answers a request for v2 with v1's manifest,
// signature and archive passes every check here and installs v1. Verification
// proves the bytes came from the vendor; it does not prove they are the version
// that was asked for. Closing it needs the tag INSIDE the signed document,
// which is a release-format decision rather than a code one.
//
// **Windows is not supported for the replacement step.** Archive and binary
// naming handle it, but Windows will not let a running executable be renamed
// over, and the privilege fallback is `sudo -n mv`. Every update that reaches
// replacement on Windows fails.
//
// Both are recorded in ADR 0077 §Deferred rather than left to be discovered.
//
// # What this package does not do
//
// It does not roll back. The replacement is atomic (temp file, chmod, rename)
// so there is no window where the binary is half-written, but once the rename
// lands the previous version is gone. A caller that needs to return to it keeps
// its own copy.
package selfupdate

import (
	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcupd "github.com/kitsunium/sdk/internal/service/selfupdate"
)

// CodeNoVendorKey identifies an install attempted by a build carrying no vendor
// key. Match it with errs.HasCode: it is the one refusal a retry never fixes.
const CodeNoVendorKey errs.Code = coreupd.CodeNoVendorKey

// CodeSignatureMissing identifies a release whose manifest signature was absent.
const CodeSignatureMissing errs.Code = coreupd.CodeSignatureMissing

// CodeSignatureInvalid identifies a manifest signature that did not verify. With
// CodeSignatureMissing it is the supply-chain pair: no retry helps, and no manual
// install is safe.
const CodeSignatureInvalid errs.Code = coreupd.CodeSignatureInvalid

// CodeChecksumMissing identifies an archive with no entry in the authenticated
// manifest.
const CodeChecksumMissing errs.Code = coreupd.CodeChecksumMissing

// CodeChecksumMismatch identifies an archive whose digest did not match its
// authenticated manifest entry.
const CodeChecksumMismatch errs.Code = coreupd.CodeChecksumMismatch

// CodeArchiveTooLarge identifies a release archive over the in-memory cap.
const CodeArchiveTooLarge errs.Code = coreupd.CodeArchiveTooLarge

// CodeAPIBodyTooLarge identifies release metadata over its read cap.
const CodeAPIBodyTooLarge errs.Code = coreupd.CodeAPIBodyTooLarge

// CodeInsecureRedirect identifies a redirect leaving HTTPS or over the hop bound.
const CodeInsecureRedirect errs.Code = coreupd.CodeInsecureRedirect

// CodeUnexpectedStatus identifies an unusable status from the release host.
const CodeUnexpectedStatus errs.Code = coreupd.CodeUnexpectedStatus

// CodeDownloadFailed identifies a download that did not complete. It is the
// transient one: a retry is reasonable.
const CodeDownloadFailed errs.Code = coreupd.CodeDownloadFailed

// CodeDevBuild identifies a check on a build with no release version.
const CodeDevBuild errs.Code = coreupd.CodeDevBuild

// CodeCandidateNotFound identifies a requested candidate that does not exist.
const CodeCandidateNotFound errs.Code = coreupd.CodeCandidateNotFound

// CodeNotPrerelease identifies a tag requested as a candidate that is stable.
const CodeNotPrerelease errs.Code = coreupd.CodeNotPrerelease

// CodeDraftRelease identifies a draft release, which exposes no asset.
const CodeDraftRelease errs.Code = coreupd.CodeDraftRelease

// CodeInvalidTag identifies a tag that is not a valid semantic version.
const CodeInvalidTag errs.Code = coreupd.CodeInvalidTag

// CodeBinaryNotInArchive identifies a verified archive missing the binary.
const CodeBinaryNotInArchive errs.Code = coreupd.CodeBinaryNotInArchive

// CodeUnknownArchive identifies an asset in an unhandled container format.
const CodeUnknownArchive errs.Code = coreupd.CodeUnknownArchive

// CodeElevationNotAuthorised identifies a replacement needing privilege the
// operator did not opt into. Recoverable, but only through the opt-in.
const CodeElevationNotAuthorised errs.Code = coreupd.CodeElevationNotAuthorised

// The seven codes below are the IMPLEMENTATION's, range 0.3.66.*, for failures
// this domain's contract does not name because a different implementation of
// the same port would fail in different places. They are re-exported for the
// same reason the eighteen above are: a code a consumer can match on but cannot
// NAME is only half a public surface, and matching a whole range with
// errs.NewPrefixMatcher is routing rather than classification.
//
// None of them is a supply-chain signal. Every one is either a local fault or a
// transfer that stopped, which is why the three classes in the package README
// keep their membership unchanged.

// CodeCandidateTagRequired identifies a candidate install asked for without
// naming which candidate. It wraps errors.ErrUnsupported, so a caller matching
// that instead still matches.
const CodeCandidateTagRequired errs.Code = svcupd.CodeCandidateTagRequired

// CodeReleaseMetadataUnreadable identifies a release-API answer that arrived
// whole and did not decode. The bytes came, so no transport retry applies.
const CodeReleaseMetadataUnreadable errs.Code = svcupd.CodeReleaseMetadataUnreadable

// CodeArchiveUnreadable identifies a release archive that would not unpack. It
// is raised only AFTER the signature and digest have both verified, so it is a
// packaging or transfer fault and never a supply-chain signal.
const CodeArchiveUnreadable errs.Code = svcupd.CodeArchiveUnreadable

// CodeExecutablePathUnresolved identifies an update that cannot name the file
// it would replace. Nothing is attempted after it.
const CodeExecutablePathUnresolved errs.Code = svcupd.CodeExecutablePathUnresolved

// CodeStagingFailed identifies a replacement that failed BEFORE the rename, so
// the running binary is byte-for-byte as it was. That is the whole difference
// between this and CodeReplacementFailed, and it is the fact an operator needs.
const CodeStagingFailed errs.Code = svcupd.CodeStagingFailed

// CodeReplacementFailed identifies a rename over the running executable that did
// not land.
const CodeReplacementFailed errs.Code = svcupd.CodeReplacementFailed

// CodeElevationFailed identifies an escalation the operator DID authorise and
// that the system refused anyway. Unlike CodeElevationNotAuthorised, no
// environment variable repairs it.
const CodeElevationFailed errs.Code = svcupd.CodeElevationFailed

// CandidateListSentinel is the tag value meaning "list the candidates rather
// than install one".
//
// It exists because a CLI flag taking an optional value has to spell "the flag
// was given without a value" AS a value. A caller wires it into its own flag
// definition, so the sentinel has to be reachable from outside the package that
// consumes it.
const CandidateListSentinel string = svcupd.CandidateListSentinel

// The sentinels a caller matches with errors.Is, one for each of the codes
// above.
//
// A facade that publishes a code and withholds the value it identifies is half
// a surface: a caller can ask errs.HasCode "is this that failure?" and cannot
// write errors.Is(err, selfupdate.SignatureInvalid), which is the spelling Go
// programs actually use and the only one that composes with errors.Join. PR
// #184 fixed exactly this shape in the entitlement facade; lock, cache and
// authz already publish theirs. This package was the outlier.
//
// They are values rather than type aliases because a sentinel IS a value —
// there is nothing to alias. The declared type is left off so each keeps its
// *errs.Error, which is what lock and cache do, and what lets a caller reach
// Code(), Public() and ExitCode() without a second lookup. Matching works
// either way: errs.Error.Is compares (Code, Reason), so a sentinel matches a
// freshly wrapped error of the same identity and not merely the same pointer.
var (
	// NoVendorKey is returned when the running build carries no vendor key,
	// so nothing could have authenticated the release. A REFUSAL, not a
	// degradation: there is no weaker check to fall back to.
	NoVendorKey = coreupd.NoVendorKey

	// SignatureMissing is returned when the detached signature over the
	// checksum manifest could not be fetched. An unsigned release is never
	// installed and no environment variable reopens that.
	SignatureMissing = coreupd.SignatureMissing

	// SignatureInvalid is returned when the manifest's signature does not
	// verify against the vendor key. With SignatureMissing it is the
	// supply-chain pair: no retry helps and no manual install is safe.
	SignatureInvalid = coreupd.SignatureInvalid

	// ChecksumMissing is returned when the authenticated manifest has no entry
	// for the archive being installed.
	ChecksumMissing = coreupd.ChecksumMissing

	// ChecksumMismatch is returned when the archive's digest does not match
	// its authenticated manifest entry — a corrupted download, or a release
	// whose assets changed after it was signed.
	ChecksumMismatch = coreupd.ChecksumMismatch

	// ArchiveTooLarge is returned when a release archive exceeds the size cap,
	// which exists because the bytes are buffered before anything has vouched
	// for them. It is also what an oversized checksums.txt raises, deliberately
	// — a truncated manifest fails signature verification, and reporting a size
	// problem as a supply-chain one is the worst advice this package can give.
	ArchiveTooLarge = coreupd.ArchiveTooLarge

	// APIBodyTooLarge is returned when a release-metadata response exceeds its
	// read cap.
	APIBodyTooLarge = coreupd.APIBodyTooLarge

	// InsecureRedirect is returned when a redirect leaves HTTPS or exceeds the
	// hop bound. Following it would hand the archive fetch to a plaintext hop.
	InsecureRedirect = coreupd.InsecureRedirect

	// UnexpectedStatus is returned when the release host answers with a status
	// the update path does not accept. Retryable.
	UnexpectedStatus = coreupd.UnexpectedStatus

	// DownloadFailed is returned when a download does not complete, including
	// a transport failure with no HTTP status at all. It is the sentinel a
	// retry policy keys on.
	DownloadFailed = coreupd.DownloadFailed

	// DevBuild is returned when a version check runs on a build carrying no
	// release version, so there is nothing to compare a release against. It is
	// an ANSWER rather than a fault — CheckForUpdate on a `go build` binary
	// reaches it every time — which is why ExplainUpgradeFailure has no case
	// for it: "rebuild from source" is NoVendorKey's advice, not this one's.
	DevBuild = coreupd.DevBuild

	// CandidateNotFound is returned when a requested release candidate does
	// not exist.
	CandidateNotFound = coreupd.CandidateNotFound

	// NotPrerelease is returned when a tag requested as a candidate is
	// reported as a stable release.
	NotPrerelease = coreupd.NotPrerelease

	// DraftRelease is returned for a draft release, which exposes no asset.
	DraftRelease = coreupd.DraftRelease

	// InvalidTag is returned for a tag that is not a valid semantic version.
	InvalidTag = coreupd.InvalidTag

	// BinaryNotInArchive is returned when a verified archive does not contain
	// the expected binary. The archive is authentic; it is a packaging fault
	// rather than an attack.
	BinaryNotInArchive = coreupd.BinaryNotInArchive

	// UnknownArchive is returned for an asset whose container format is not
	// handled.
	UnknownArchive = coreupd.UnknownArchive

	// ElevationNotAuthorised is returned when replacing the binary needs
	// privilege the operator did not opt into. Recoverable — but only through
	// the opt-in, which ExplainUpgradeFailure names.
	ElevationNotAuthorised = coreupd.ElevationNotAuthorised

	// CandidateTagRequired is returned by DownloadCandidate for an empty tag.
	// It wraps errors.ErrUnsupported, so a caller matching that instead still
	// matches.
	CandidateTagRequired = svcupd.CandidateTagRequired

	// ReleaseMetadataUnreadable is returned when the release API answers
	// within its cap and the body does not decode. The bytes arrived, so no
	// transport retry applies.
	ReleaseMetadataUnreadable = svcupd.ReleaseMetadataUnreadable

	// ArchiveUnreadable is returned when a release archive will not unpack. It
	// is raised only AFTER the signature and digest have both verified, so it
	// is a packaging or transfer fault and never a supply-chain signal.
	ArchiveUnreadable = svcupd.ArchiveUnreadable

	// ExecutablePathUnresolved is returned when the process cannot work out
	// which file on disk it runs from. Nothing is attempted after it.
	ExecutablePathUnresolved = svcupd.ExecutablePathUnresolved

	// StagingFailed is returned when the replacement failed BEFORE the rename,
	// so the running binary is byte-for-byte as it was. That is the whole
	// difference between this and ReplacementFailed.
	StagingFailed = svcupd.StagingFailed

	// ReplacementFailed is returned when the rename over the running
	// executable did not land.
	ReplacementFailed = svcupd.ReplacementFailed

	// ElevationFailed is returned when an escalation the operator DID
	// authorise was refused by the system. Unlike ElevationNotAuthorised, no
	// environment variable repairs it.
	ElevationFailed = svcupd.ElevationFailed
)

// Getter performs the HTTP GETs a self-update needs. It aliases the core port.
type Getter = coreupd.Getter

// FileSystem is the disk half of replacing a running binary. It aliases the core
// port.
type FileSystem = coreupd.FileSystem

// Copier streams the verified archive to its destination. It aliases the core
// port.
type Copier = coreupd.Copier

// Update is the outcome of a version check or an install. It aliases the core
// value type.
type Update = svcupd.UpdateValue

// Candidate is one release candidate. It aliases the core value type.
type Candidate = svcupd.CandidateValue

// Source says where releases come from and what they are called. It aliases the
// service type: these are one engine's construction parameters, which ADR 0074
// places with the engine rather than in the contract layer.
type Source = svcupd.SourceValue

// Service replaces the running binary with a newer signed release. It aliases
// the service type — the engine handle, per ADR 0074.
type Service = svcupd.Service

// New returns a Service for the given running version and release source.
//
// The returned Service carries NO vendor key and therefore installs nothing:
// chain WithVendorKey with the build's linked-in anchor. That is the safe
// direction — a Service that verified only when a key happened to be present
// would make the security property depend on a build flag.
func New(version string, src Source) *Service {
	//: delegate verbatim to the service implementation.
	return svcupd.NewService(version, src)
}

// NewWithDeps returns a Service with its three ports injected, for a caller that
// supplies its own HTTP policy or a test that supplies doubles. A nil fs or
// copier is legal on paths that never reach the disk.
func NewWithDeps(version string, src Source, client Getter, fs FileSystem, copier Copier) *Service {
	//: delegate verbatim to the service implementation.
	return svcupd.NewUpdaterWithDeps(version, src, client, fs, copier)
}

// StdinIsTerminal reports whether a human could answer a prompt on this
// process's standard input.
//
// It is a free function rather than something AuthoriseUnattendedUpgrade works
// out for itself, and that is the point: the consent decision stays testable
// without a pty, because the CALLER supplies the answer. Pass the result as the
// interactive argument.
func StdinIsTerminal() bool {
	//: delegate verbatim to the service implementation.
	return svcupd.StdinIsTerminal()
}
