// Package selfupdate — range 0.3.66.* (ADR 0005 §Registry, allocated in
// codeRangeOwners per ADR 0035).
//
// The domain's own range is 0.2.34.* and it holds every refusal the CONTRACT
// names: an unsigned release, a digest that does not match, a redirect this
// build will not follow. Those are the outcomes a caller of pkg/v1/selfupdate
// branches on, and none of them is redeclared here.
//
// What lives in this range is the other half — the failures an IMPLEMENTATION
// has and a contract does not: a JSON body that will not decode, a tar that
// will not open, a temp file that cannot be created next to the running
// binary. The core has no vocabulary for them because a different
// implementation of the same port would fail in different places.
package selfupdate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.66.0 - 0.3.66.255

// CodeCandidateTagRequired identifies a candidate install asked for without
// naming which candidate.
const CodeCandidateTagRequired errs.Code = 0x00_03_42_01 // 0.3.66.1

// CodeReleaseMetadataUnreadable identifies a release-API answer that arrived
// whole and did not decode as release metadata.
const CodeReleaseMetadataUnreadable errs.Code = 0x00_03_42_02 // 0.3.66.2

// CodeArchiveUnreadable identifies a release archive that could not be
// buffered or unpacked.
//
// It is deliberately NOT the domain's ChecksumMismatch: by the time anything
// here runs the archive has already been authenticated and its digest matched,
// so a container that will not open is a packaging or transfer fault and never
// a supply-chain signal. Reporting it as one would tell an operator not to
// install a release that is provably the vendor's.
const CodeArchiveUnreadable errs.Code = 0x00_03_42_03 // 0.3.66.3

// CodeExecutablePathUnresolved identifies an update that cannot name the file
// it would replace.
const CodeExecutablePathUnresolved errs.Code = 0x00_03_42_04 // 0.3.66.4

// CodeStagingFailed identifies a replacement that failed BEFORE the rename, so
// the running binary was never touched.
//
// The boundary between this and [CodeReplacementFailed] is the only thing an
// operator needs from these two, and it is the rename: everything under this
// code leaves the installed binary byte-for-byte as it was.
const CodeStagingFailed errs.Code = 0x00_03_42_05 // 0.3.66.5

// CodeReplacementFailed identifies a rename over the running executable that
// did not land.
const CodeReplacementFailed errs.Code = 0x00_03_42_06 // 0.3.66.6

// CodeElevationFailed identifies an escalation the operator DID authorise and
// that the system refused anyway.
//
// It is not the domain's ElevationNotAuthorised, and the difference is the
// whole remedy: that one is fixed by setting one environment variable, this one
// is not fixed by setting anything.
const CodeElevationFailed errs.Code = 0x00_03_42_07 // 0.3.66.7
