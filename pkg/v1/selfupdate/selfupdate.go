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

// Fetcher performs the HTTP GETs a self-update needs. It aliases the core port.
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
