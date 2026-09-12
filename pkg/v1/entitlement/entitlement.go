//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/entitlement .

// Package entitlement verifies a machine's entitlement to run a product:
// local key material plus a vendor-signed roster, turned into a grant or a
// typed refusal.
//
// It is the thin public facade over internal/service/entitlement.
//
// # The trust model, and its ceiling
//
// One anchor: the vendor's ed25519 public key, linked into the consuming
// binary. Everything else — the roster, the per-subject keys, the host serving
// them — is untrusted input. A hostile endpoint can serve what it likes; it
// cannot forge a signature made with the vendor's private half.
//
// What that cannot survive is a patched binary. A check running on someone
// else's machine is removable by definition. This stops casual sharing and
// makes revocation real for cooperative installs, and it is deliberately not
// built as if it were more.
//
// # Identity is yours to supply
//
// Proving possession needs key material in a format the standard library
// cannot parse. Rather than pull golang.org/x/crypto/ssh — and through it the
// SDK-wide-banned golang.org/x/sys — into every consumer's graph, the machine's
// half of the proof is a PORT:
//
//	type Identity interface {
//		Discover() (subject string, err error)
//		Fingerprint(subject string) (fingerprint string, err error)
//		ProvePossession(subject string) error
//	}
//
// An ssh implementation ships under third-party/entitlement for consumers who
// want one and accept the dependency. A consumer that already handles its own
// key material — which is the common case for a product that enrols its users —
// implements three methods instead and keeps its graph at nineteen modules
// rather than a hundred and one. ADR 0078 has the measurement.
//
// # Cannot decide is never no
//
// A cold Verify reaches the network FIRST. Only when no origin answers does the
// cached bundle substitute, and it goes back through the signature check on
// every read, so a frozen copy stops authorising at its own expiry. Failure
// with nothing cached reports RosterUnreachable and names the NETWORK, because
// reporting an outage as a revocation is the one wrong answer.
//
// What is NOT answered, and cannot be locally, is a frozen CLOCK: every source
// of time an offline process can read belongs to the party being checked. The
// ratchet raises the cost; it does not close the hole.
package entitlement

import (
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// RosterLifetime is how long a signed roster stays usable. It bounds how long a
// revoked subject keeps working offline, and how long a captured roster can be
// replayed — the same bound, read from either side.
const RosterLifetime time.Duration = coreent.RosterLifetime

// CodeNoLicence identifies a machine with no entitlement key at all.
const CodeNoLicence errs.Code = coreent.CodeNoLicence

// CodeRosterUnsigned identifies a roster carrying no valid vendor signature —
// the spoofing signal, since a substituted endpoint cannot produce one.
const CodeRosterUnsigned errs.Code = coreent.CodeRosterUnsigned

// CodeRosterStale identifies a roster that verified but whose window has closed.
const CodeRosterStale errs.Code = coreent.CodeRosterStale

// CodeRevoked identifies a subject absent from a roster that was itself valid.
const CodeRevoked errs.Code = coreent.CodeRevoked

// CodeLicenceExpired identifies a subject whose own term has closed.
const CodeLicenceExpired errs.Code = coreent.CodeLicenceExpired

// CodeKeyMismatch identifies a local key that does not match the published
// fingerprint.
const CodeKeyMismatch errs.Code = coreent.CodeKeyMismatch

// CodeRosterUnreachable identifies a check that could not decide either way. It
// is NOT a refusal, and a caller that treats it as one turns an outage into a
// revocation.
const CodeRosterUnreachable errs.Code = coreent.CodeRosterUnreachable

// CodeAmbiguousLicence identifies more than one identity on one machine, which
// no automatic choice can resolve safely.
const CodeAmbiguousLicence errs.Code = coreent.CodeAmbiguousLicence

// CodeCIUnverifiable identifies a CI run whose provenance could not be proven.
const CodeCIUnverifiable errs.Code = coreent.CodeCIUnverifiable

// CodeCIUnknownKey identifies a CI token signed by a key the issuer does not
// publish.
const CodeCIUnknownKey errs.Code = coreent.CodeCIUnknownKey

// CodeCINotEntitled identifies a CI account the roster does not cover.
const CodeCINotEntitled errs.Code = coreent.CodeCINotEntitled

// CodeNoPossession identifies a holder who could not prove possession.
const CodeNoPossession errs.Code = coreent.CodeNoPossession

// CodeClockRegressed identifies a clock behind the last signed document this
// machine verified — the anti-rollback ratchet.
const CodeClockRegressed errs.Code = coreent.CodeClockRegressed

// CodeUpdateRequired identifies a build below the floor the roster mandates.
const CodeUpdateRequired errs.Code = coreent.CodeUpdateRequired

// CodeProductInvalid identifies a Product whose origin list could not survive
// the day it is needed — see Product.Validate.
const CodeProductInvalid errs.Code = coreent.CodeProductInvalid

// The fourteen sentinels, re-exported so errors.Is answers on them. They are
// the SAME values core declares, not copies, so a caller may compare across the
// facade boundary; each also carries the matching Code above, so errs.HasCode
// and errors.Is are two ways of asking one question.
//
// A consumer that only needs ONE distinction needs this one: ErrRosterUnreachable
// says "cannot decide", and treating it as "decided no" turns an outage into a
// revocation.
var (
	// ErrNoLicense identifies a machine with no entitlement key at all.
	ErrNoLicense error = coreent.ErrNoLicense

	// ErrRosterUnsigned identifies a roster carrying no valid vendor signature.
	ErrRosterUnsigned error = coreent.ErrRosterUnsigned

	// ErrRosterStale identifies a roster that verified but whose window has closed.
	ErrRosterStale error = coreent.ErrRosterStale

	// ErrRevoked identifies a subject absent from a roster that was itself valid.
	ErrRevoked error = coreent.ErrRevoked

	// ErrLicenseExpired identifies a subject whose own term has closed.
	ErrLicenseExpired error = coreent.ErrLicenseExpired

	// ErrKeyMismatch identifies a local key that does not match the published fingerprint.
	ErrKeyMismatch error = coreent.ErrKeyMismatch

	// ErrRosterUnreachable identifies a check that could not decide either way.
	ErrRosterUnreachable error = coreent.ErrRosterUnreachable

	// ErrAmbiguousLicense identifies more than one identity on one machine.
	ErrAmbiguousLicense error = coreent.ErrAmbiguousLicense

	// ErrCIUnverifiable identifies a CI run whose provenance could not be proven.
	ErrCIUnverifiable error = coreent.ErrCIUnverifiable

	// ErrCIUnknownKey identifies a CI token signed by a key the issuer does not publish.
	ErrCIUnknownKey error = coreent.ErrCIUnknownKey

	// ErrCINotEntitled identifies a CI account the roster does not cover.
	ErrCINotEntitled error = coreent.ErrCINotEntitled

	// ErrNoPossession identifies a holder who could not prove possession.
	ErrNoPossession error = coreent.ErrNoPossession

	// ErrClockRegressed identifies a clock behind the last signed document this machine verified.
	ErrClockRegressed error = coreent.ErrClockRegressed

	// ErrUpdateRequired identifies a build below the floor the roster mandates.
	ErrUpdateRequired error = coreent.ErrUpdateRequired
)

// Identity is the machine's half of the proof. It aliases the core port.
type Identity = coreent.Identity

// Roster is a published, vendor-signed entitlement roster.
type Roster = coreent.RosterValue

// Subject is one entry in a roster: an identity and what it is entitled to.
type Subject = coreent.SubjectValue

// CIEntitlement is a roster's seat for a continuous-integration account.
type CIEntitlement = coreent.CIEntitlementValue

// Grant is a verified entitlement, with the deadline past which it must be
// re-verified and whether it was decided offline.
type Grant = coreent.GrantValue

// Origin is one place a roster is published.
type Origin = coreent.OriginValue

// Product names the vendor whose roster this binary trusts. It aliases the
// service type: these are one engine's construction parameters (ADR 0074).
type Product = svcent.ProductValue

// Service verifies entitlement. It aliases the service handle (ADR 0074).
type Service = svcent.Service

// New returns a verifier for the given identity, vendor key and product.
func New(identity Identity, vendor []byte, product *Product) *Service {
	//: delegate verbatim to the service implementation.
	return svcent.NewService(identity, vendor, product)
}

// Bundle is the one-document roster form: the raw roster and its detached
// signature in a single object, because two documents cannot be fetched
// atomically. It aliases the service type (ADR 0074) — a caller BUILDS one to
// serve or to cache, which is why it is public at all.
type Bundle = svcent.BundleValue

// Getter performs the roster HTTP GETs. A consumer substitutes it to exercise
// the admission logic without a network, which is the only way to test a
// refusal path that depends on what an origin answered.
type Getter = svcent.Getter

// UpdateRequiredError carries the two versions a version-floor refusal is
// about. It exists as a TYPE rather than a message because a caller that cannot
// read the required version out of the refusal re-runs the upgrade, gets the
// same refusal, and loops forever.
type UpdateRequiredError = svcent.UpdateRequiredError

// NewWithGetter returns a verifier whose roster fetches go through client.
//
// It takes client, the HTTP surface the roster is fetched over; identity, the
// machine's half of the proof; vendor, the ed25519 public key the binary links
// in; and product, the vendor-specific facts — a nil product uses the
// documented fallbacks.
func NewWithGetter(client Getter, identity Identity, vendor []byte, product *Product) *Service {
	//: delegate verbatim to the service implementation.
	return svcent.NewServiceWithGetter(client, identity, vendor, product)
}

// RequiresUpdate reports whether current is below the floor a roster mandates.
//
// It takes the running build's version and the minimum the roster requires —
// an empty floor requires nothing — and reports whether the build is below it.
func RequiresUpdate(current, floor string) bool {
	//: delegate verbatim to the service implementation.
	return svcent.RequiresUpdate(current, floor)
}

// UpdateRefusal builds the typed refusal for a build below the roster's floor.
//
// It takes the running build's version and the minimum the roster requires, and
// returns an *UpdateRequiredError carrying both.
func UpdateRefusal(current, floor string) error {
	//: delegate verbatim to the service implementation.
	return svcent.UpdateRefusal(current, floor)
}
