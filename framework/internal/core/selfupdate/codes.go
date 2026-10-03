// Package selfupdate — the error-code range owned by this domain (ADR 0005 §Registry).
package selfupdate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// CodeNoVendorKey identifies an install attempted by a build that carries no
// vendor key, so nothing could have authenticated the release.
const CodeNoVendorKey errs.Code = 0x00_02_22_01 // 0.2.34.1

// CodeSignatureMissing identifies a release whose detached signature over the
// checksum manifest could not be fetched.
const CodeSignatureMissing errs.Code = 0x00_02_22_02 // 0.2.34.2

// CodeSignatureInvalid identifies a checksum manifest whose signature did not
// verify against the vendor key.
const CodeSignatureInvalid errs.Code = 0x00_02_22_03 // 0.2.34.3

// CodeChecksumMissing identifies an archive with no entry in the authenticated
// checksum manifest.
const CodeChecksumMissing errs.Code = 0x00_02_22_04 // 0.2.34.4

// CodeChecksumMismatch identifies an archive whose digest did not match its
// authenticated manifest entry.
const CodeChecksumMismatch errs.Code = 0x00_02_22_05 // 0.2.34.5

// CodeArchiveTooLarge identifies a release archive exceeding the size cap.
const CodeArchiveTooLarge errs.Code = 0x00_02_22_06 // 0.2.34.6

// CodeAPIBodyTooLarge identifies a release-metadata response exceeding its cap.
const CodeAPIBodyTooLarge errs.Code = 0x00_02_22_07 // 0.2.34.7

// CodeInsecureRedirect identifies a redirect leaving HTTPS, or exceeding the
// hop bound.
const CodeInsecureRedirect errs.Code = 0x00_02_22_08 // 0.2.34.8

// CodeUnexpectedStatus identifies a release host answering with a status the
// update path does not accept.
const CodeUnexpectedStatus errs.Code = 0x00_02_22_09 // 0.2.34.9

// CodeDownloadFailed identifies a download that did not complete.
const CodeDownloadFailed errs.Code = 0x00_02_22_0A // 0.2.34.10

// CodeDevBuild identifies a version check on a build with no release version to
// compare against.
const CodeDevBuild errs.Code = 0x00_02_22_0B // 0.2.34.11

// CodeCandidateNotFound identifies a requested release candidate that does not
// exist.
const CodeCandidateNotFound errs.Code = 0x00_02_22_0C // 0.2.34.12

// CodeNotPrerelease identifies a tag requested as a candidate that the host
// reports as a stable release.
const CodeNotPrerelease errs.Code = 0x00_02_22_0D // 0.2.34.13

// CodeDraftRelease identifies a draft release, which has no downloadable asset.
const CodeDraftRelease errs.Code = 0x00_02_22_0E // 0.2.34.14

// CodeInvalidTag identifies a tag that is not a valid semantic version.
const CodeInvalidTag errs.Code = 0x00_02_22_0F // 0.2.34.15

// CodeBinaryNotInArchive identifies a verified archive that does not contain the
// expected binary.
const CodeBinaryNotInArchive errs.Code = 0x00_02_22_10 // 0.2.34.16

// CodeUnknownArchive identifies an asset whose container format is not handled.
const CodeUnknownArchive errs.Code = 0x00_02_22_11 // 0.2.34.17

// CodeElevationNotAuthorised identifies a replacement needing privilege the
// operator did not opt into.
const CodeElevationNotAuthorised errs.Code = 0x00_02_22_12 // 0.2.34.18
