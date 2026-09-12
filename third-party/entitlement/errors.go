// Package entitlement verifies a machine's entitlement to run a product.
//
// The trust model has exactly one anchor: vendorPublicKey, compiled into the
// binary. Everything else — the roster, the per-client public keys, the
// repository serving them — is untrusted input. A hostile endpoint (DNS
// redirection, a proxy, an S3 bucket mimicking raw.githubusercontent.com) can
// serve whatever it likes; it cannot forge a signature made with the vendor's
// private half, so the roster it serves is either genuine or rejected.
//
// What that anchor cannot do is survive a patched binary. A check running on
// someone else's machine is removable by definition. This package stops
// casual sharing and makes revocation real for cooperative installs; it is
// not a wall against a determined attacker, and is deliberately not built as
// if it were.
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// Sentinel errors distinguishing why authorization failed. Callers map them
// to exit codes, so each one names a distinct operator situation rather than
// a generic failure.
var (
	// ErrNoLicense reports that no licence key was found on this machine.
	// The caller has never enrolled, or the key was removed.
	ErrNoLicense = errs.Define(CodeNoLicence, "NO_LICENCE",
		"no entitlement key was found on this machine",
		"third-party/entitlement: no key material for any subject")
	// ErrRosterUnsigned reports that the roster carried no valid vendor
	// signature. This is the spoofing signal: a substituted endpoint cannot
	// produce one.
	ErrRosterUnsigned = errs.Define(CodeRosterUnsigned, "ROSTER_UNSIGNED",
		"the roster is not signed by the expected vendor",
		"third-party/entitlement: the roster signature did not verify against the vendor key")
	// ErrRosterStale reports that the roster verified but its validity
	// window has closed. It bounds how long a revoked client keeps working
	// offline, and how long a hostile endpoint can replay a genuine roster.
	//
	// Those are the same bound, and cache.go is what finally made the first
	// half of that sentence describe something: an offline client re-reads
	// the last bundle it authenticated and is refused HERE the moment that
	// bundle's window closes — at most RosterLifetime after it was signed,
	// whoever is replaying it and from wherever.
	ErrRosterStale = errs.Define(CodeRosterStale, "ROSTER_STALE",
		"the roster has expired",
		"third-party/entitlement: the roster verified but its validity window has closed")
	// ErrRevoked reports that the licence UUID is absent from a roster that
	// was itself valid — the subject was removed upstream.
	ErrRevoked = errs.Define(CodeRevoked, "REVOKED",
		"this entitlement has been revoked",
		"third-party/entitlement: the subject is absent from a roster that was itself valid")
	// ErrLicenseExpired reports that the subject's own validity window has
	// closed, even though the roster itself is fresh and still lists them.
	// Distinct from ErrRosterStale (the whole roster's signing freshness)
	// and from ErrRevoked (removed from the roster entirely): renewal, not
	// rotation or re-enrolment, is what resolves this one.
	ErrLicenseExpired = errs.Define(CodeLicenceExpired, "LICENCE_EXPIRED",
		"this entitlement has expired",
		"third-party/entitlement: the subject's own validity window has closed")
	// ErrKeyMismatch reports that the local key does not match the
	// fingerprint the roster records for this UUID, which is what a copied
	// .pub file looks like: the public half is published, so possession of
	// it proves nothing.
	ErrKeyMismatch = errs.Define(CodeKeyMismatch, "KEY_MISMATCH",
		"the local key does not match the published fingerprint",
		"third-party/entitlement: the local key fingerprint differs from the roster's")
	// ErrRosterUnreachable reports that no origin could be reached AND no
	// usable bundle was cached, so no authorization decision could be made at
	// all. It is deliberately distinct from a refusal: the caller knows
	// nothing, rather than knowing the answer is no.
	//
	// It names the NETWORK even when the cache is what failed last. The cache
	// is a fallback nobody configured and most operators have never heard of;
	// pointing them at it would send them to fix the wrong thing, when what
	// happened is that every publication point was unreachable.
	ErrRosterUnreachable = errs.Define(CodeRosterUnreachable, "ROSTER_UNREACHABLE",
		"the roster could not be reached",
		"third-party/entitlement: no origin answered and nothing was cached — cannot decide, which is not the same as no")
	// ErrAmbiguousLicense reports that the key directory holds more than one
	// usable licence identity, so which one this machine presents cannot be
	// decided here. It is deliberately distinct from ErrNoLicense: the
	// machine is enrolled, possibly several times over, and the fix is to
	// remove the identities that do not belong rather than to enrol.
	//
	// Guessing was the previous behaviour — the lexicographically first UUID
	// won — which let a stray .pub decide which licence got verified, which
	// one a rotation overwrote, and which one `license status` reported.
	ErrAmbiguousLicense = errs.Define(CodeAmbiguousLicence, "AMBIGUOUS_LICENCE",
		"more than one entitlement identity is present on this machine",
		"third-party/entitlement: several subjects are enrolled and no automatic choice is safe")
	// ErrCIUnverifiable reports that a claim of running inside CI could not
	// be authenticated: no token, a malformed one, a bad signature, or one
	// minted for someone else. It never means "not in CI" — it means the
	// claim was not provable, and the caller falls back to the ordinary
	// device path rather than granting a free seat.
	ErrCIUnverifiable = errs.Define(CodeCIUnverifiable, "CI_UNVERIFIABLE",
		"this run could not be proven to be a CI run",
		"third-party/entitlement: the CI provenance token was absent or unverifiable")
	// ErrCIUnknownKey reports that the token names a signing key the fetched
	// key set does not publish. Distinct because it is the one CI failure
	// with a remedy the client can apply itself: refresh the key set once,
	// since GitHub rotates keys, then refuse if it still does not appear.
	ErrCIUnknownKey = errs.Define(CodeCIUnknownKey, "CI_UNKNOWN_KEY",
		"the CI token is signed by a key the issuer does not publish",
		"third-party/entitlement: no JWKS entry matched the token's kid")
	// ErrCINotEntitled reports that the CI run authenticated, but the account
	// it belongs to is not one the roster covers. The token is genuine; the
	// licence simply does not extend to it.
	ErrCINotEntitled = errs.Define(CodeCINotEntitled, "CI_NOT_ENTITLED",
		"this CI account is not covered by an entitlement",
		"third-party/entitlement: the roster carries no CI seat for this account")
	// ErrNoPossession reports that the private half could not be exercised,
	// so holding the public key was never turned into proof of ownership.
	ErrNoPossession = errs.Define(CodeNoPossession, "NO_POSSESSION",
		"possession of the private key could not be proven",
		"third-party/entitlement: the possession challenge was not answered correctly")
	// ErrClockRegressed reports that this machine's clock reads EARLIER than
	// the newest vendor-signed instant it has ever authenticated.
	//
	// It is the one statement about time this package can make without the
	// network, and it is worth making because the alternative is to believe a
	// clock the holder controls. A roster is signed evidence that time was at
	// least its IssuedAt; a clock that has since moved backwards past that
	// point is either broken or being moved, and both are the holder's to fix.
	//
	// What it does NOT catch is a FROZEN clock: parked exactly on the mark,
	// nothing local can tell that time has passed. Nor does it survive
	// deletion — the mark lives on a disk its holder owns. It bounds the
	// careless case and the casual one, which is this package's whole remit.
	ErrClockRegressed = errs.Define(CodeClockRegressed, "CLOCK_REGRESSED",
		"this machine's clock is behind the last signed document it verified",
		"third-party/entitlement: the anti-rollback ratchet refused a clock earlier than the recorded high-water mark")
	// ErrUpdateRequired reports that the roster demands a newer binary than
	// this one. It is NOT a licence failure — the subject is perfectly
	// entitled — so it carries its own sentinel and its own exit code: the
	// resolving action is an upgrade, and telling somebody their licence is
	// broken when it is not would send them to the wrong place entirely.
	ErrUpdateRequired = errs.Define(CodeUpdateRequired, "UPDATE_REQUIRED",
		"a newer version is required",
		"third-party/entitlement: the running build is below the floor the roster mandates")
)
