//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/internal/core/selfupdate .

// Package selfupdate — the error-code range owned by this domain (ADR 0005 §Registry).
//
// Package selfupdate — the sentinels every implementation of this domain returns.
//
// The first five are the trust chain, and their ORDER is the contract: a build
// with no vendor key cannot install at all; a manifest with no signature is not
// authenticated; a signature that does not verify ends it; only then is the
// archive's digest compared against a manifest that is now trusted. A digest
// checked against an unauthenticated manifest proves nothing, which is why
// CodeChecksumMismatch can only be reached after CodeSignatureInvalid was not.
//
// Package selfupdate is the contract for a binary that replaces itself: the
// ports it needs from its environment, and the values it reports.
//
// The domain exists because the hard part of a self-update is not the download.
// It is the ORDER in which a candidate becomes trusted — a detached signature
// over a checksum manifest, then the archive's digest against that
// now-authenticated manifest, and only then anything touching the disk — and the
// fact that a process replacing its own executable has no second chance if it
// gets it wrong.
//
// Three ports, and each is a seam a test drives rather than an abstraction for
// its own sake: Getter is the network, FileSystem is the disk, Copier is the
// stream between them. There is **no registry**: a registry's key would name a
// release host, and the whole trust chain is anchored to a key pinned at build
// time for exactly one host.
package selfupdate
