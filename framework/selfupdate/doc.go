// Package selfupdate replaces the running binary with a newer signed release.
//
// It is the framework's public package over framework/internal/service/selfupdate
// (ADR 0158): the value types and ports are aliases of the core selfupdate types
// and the functions delegate straight to the service implementation, which is
// built on pkg/v1 and nothing below it.
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
// A product whose updates are silent by design builds its Service
// WithAutomaticConsent — its own consent, given at build, which
// Service.AuthoriseUnattendedUpgrade reads and an operator still overrules
// with <PREFIX>_AUTO_UPGRADE=0. It grants no escalation, and WithoutElevation
// forbids escalation outright, whatever <PREFIX>_ALLOW_SUDO says (ADR 0150).
//
// # Keys that rotate, a signature that names its release, a probe that rolls back
//
// Service.WithVendorKeys links several keys, in order, and a release verifies
// against any of them: a rotation publishes under the new key while builds that
// carry both accept it, and neither side has to be updated first (ADR 0150).
// Service.WithSignatureDomain makes each signature cover a domain — so a key
// that also signs other documents cannot have one read as a release — and makes
// the signed manifest say which tag it is and until when it may be installed
// ("# tag v1.4.0", "# expires 2026-12-31T00:00:00Z"): an older release replayed
// under a newer name, or a stale one, is refused. Service.WithProbe keeps the
// previous binary as <binary>.prev and runs the new one with the product's
// probe arguments; a probe that fails puts the previous one back
// (CodeProbeFailed).
//
// # Two limits a caller must know before relying on this
//
// **Without a signature domain, a compromised release host can serve an OLDER
// signed release.** The historical manifest is signed but carries no release
// tag, so a host that answers a request for v2 with v1's manifest, signature
// and archive passes every check and installs v1. WithSignatureDomain closes it
// by putting the tag and an expiry INSIDE the signed document; a product that
// has not adopted the format keeps the gap.
//
// **Windows is not supported for the replacement step.** Windows will not let
// a running executable be renamed over, so on a Windows build Upgrade refuses
// with the SDK's UNSUPPORTED_PLATFORM — errors.Is(err, proc.UnsupportedPlatform)
// — before it downloads anything. CheckForUpdate works there: a product can
// still tell its user that a newer release exists. A build with no vendor key
// is told about the key first, on every platform.
//
// Both are recorded in ADR 0077 §Deferred rather than left to be discovered,
// and the Windows refusal in ADR 0095.
//
// # What this package does not do
//
// It does not roll back on its own. The replacement is atomic (temp file,
// chmod, rename) so there is no window where the binary is half-written, and
// without WithProbe the previous version is gone once the rename lands; with a
// probe it is kept as <binary>.prev and put back when the probe fails.
package selfupdate
