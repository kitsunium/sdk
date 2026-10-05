package entitlement

import (
	"time"

	coreent "github.com/kitsunium/sdk/framework/internal/core/entitlement"
	svcent "github.com/kitsunium/sdk/framework/internal/service/entitlement"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
// None is annotated `error`, and that is deliberate. The annotation would erase
// the concrete type, and with it Code, Reason, Public and ExitCode — every one
// of which a consumer reads directly off the sentinel to render a message or
// exit a process. Without it they are reachable only through a type assertion
// to a type a consumer cannot name, since internal/kernel/errs is internal.
// pkg/v1/app/lock, pkg/v1/data/cache and pkg/v1/security/authz all declare theirs this way;
// this package shipped as the outlier, when it was pkg/v1/entitlement.
// TestTheSentinelsKeepTheirConcreteType
// fails the BUILD if the annotation comes back.
//
// A consumer that only needs ONE distinction needs this one: ErrRosterUnreachable
// says "cannot decide", and treating it as "decided no" turns an outage into a
// revocation.
var (
	// ErrNoLicense identifies a machine with no entitlement key at all.
	ErrNoLicense = coreent.ErrNoLicense

	// ErrRosterUnsigned identifies a roster carrying no valid vendor signature.
	ErrRosterUnsigned = coreent.ErrRosterUnsigned

	// ErrRosterStale identifies a roster that verified but no longer authorises:
	// its own window has closed, or a newer signed decision has superseded it.
	ErrRosterStale = coreent.ErrRosterStale

	// ErrRevoked identifies a subject absent from a roster that was itself valid.
	ErrRevoked = coreent.ErrRevoked

	// ErrLicenseExpired identifies a subject whose own term has closed.
	ErrLicenseExpired = coreent.ErrLicenseExpired

	// ErrKeyMismatch identifies a local key that does not match the published fingerprint.
	ErrKeyMismatch = coreent.ErrKeyMismatch

	// ErrRosterUnreachable identifies a check that could not decide either way.
	ErrRosterUnreachable = coreent.ErrRosterUnreachable

	// ErrAmbiguousLicense identifies more than one identity on one machine.
	ErrAmbiguousLicense = coreent.ErrAmbiguousLicense

	// ErrCIUnverifiable identifies a CI run whose provenance could not be proven.
	ErrCIUnverifiable = coreent.ErrCIUnverifiable

	// ErrCIUnknownKey identifies a CI token signed by a key the issuer does not publish.
	ErrCIUnknownKey = coreent.ErrCIUnknownKey

	// ErrCINotEntitled identifies a CI account the roster does not cover.
	ErrCINotEntitled = coreent.ErrCINotEntitled

	// ErrNoPossession identifies a holder who could not prove possession.
	ErrNoPossession = coreent.ErrNoPossession

	// ErrClockRegressed identifies a clock behind the last signed document this machine verified.
	ErrClockRegressed = coreent.ErrClockRegressed

	// ErrUpdateRequired identifies a build below the floor the roster mandates.
	ErrUpdateRequired = coreent.ErrUpdateRequired
)

// Identity is the machine's half of the proof. It aliases the core port.
type Identity = coreent.Identity

// BoundProver is Identity's sibling for the one claim the three methods cannot
// make: possession of the key the ROSTER authorised, rather than of whatever
// this machine holds when it is asked.
//
// Implement it when your key custody can bind, and the verifier will prefer it:
//
//	func (k *MyKeys) ProvePossessionFor(subject, authorised string) error
//
// Without it the engine asks Fingerprint what you present, compares that answer
// itself, and then asks ProvePossession — so material replaced between the two
// calls is signed for, and the strongest thing an implementation can do unaided
// is notice that something changed without learning what it should have been.
//
// It is a SIBLING rather than a parameter on ProvePossession because Identity is
// published through this alias and Go satisfies interfaces structurally: widening
// it would break every implementation at compile time with no deprecation window,
// and would not even guarantee the binding — an implementation can accept the
// parameter and ignore it. ADR 0039, ADR 0040 §4, ADR 0092.
//
// An Identity that does not implement this is fully supported and unchanged. The
// window stays open for it, which is said here rather than left to be found.
type BoundProver = coreent.BoundProver

// Roster is a published, vendor-signed entitlement roster.
type Roster = coreent.RosterValue

// Subject is one entry in a roster: an identity and what it is entitled to.
type Subject = coreent.SubjectValue

// CIEntitlement is a roster's seat for a continuous-integration account.
type CIEntitlement = coreent.CIEntitlementValue

// Grant is a verified entitlement, with the deadline past which it must be
// re-verified and whether it was decided offline.
//
// Read Deadline() rather than the NotAfter field when you intend to ACT on the
// deadline: NotAfter's zero value means "no deadline was computed" — the shape a
// grant seeded from a bare timestamp carries — and the method resolves the
// fallback the field cannot express. Expired(now) is the same question asked the
// other way round, and the two are one rule by construction.
//
// Honouring it is YOURS. See the package comment.
type Grant = coreent.GrantValue

// Origin is one place a roster is published.
type Origin = coreent.OriginValue

// Product names the vendor whose roster this binary trusts. It aliases the
// service type: these are one engine's construction parameters (ADR 0074).
type Product = svcent.ProductValue

// Service verifies entitlement. It aliases the service handle (ADR 0074).
type Service = svcent.Service

// New returns a verifier for the given identity, vendor key and product.
//
// One anchor, which is the one-element list NewWithAnchors takes — not a second
// representation of the same thing, so no path can disagree with the multi-anchor
// one about what a single key means.
func New(identity Identity, vendor []byte, product *Product) *Service {
	//: delegate verbatim to the service implementation.
	return svcent.NewService(identity, vendor, product)
}

// newWithAnchors is NewWithAnchors's body: decl_gen.go writes NewWithAnchors, from the
// design, as one call of it.
func newWithAnchors(identity Identity, anchors [][]byte, product *Product) *Service {
	//: delegate verbatim to the service implementation.
	return svcent.NewServiceWithAnchors(identity, anchors, product)
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

// BearerFetch performs the one request a [Service] makes with a credential:
// the GitHub Actions ID-token mint on the CI path, which sends the runner's
// bearer token. It is a function rather than a [Getter] method because the
// roster fetch must never carry one.
//
// [Service].WithBearerFetch replaces the default, and only a test should need
// to: the default refuses every redirect, so the runner's credential cannot be
// forwarded to a destination nobody vouched for, and a replacement takes that
// duty on. It aliases the service type (ADR 0074).
type BearerFetch = svcent.BearerFetch

// RoughtimeServer is one Roughtime server a [Service] may ask for a signed
// statement of the current time, and the ed25519 key its answer must be signed
// with: the key, not the address, is the whole trust decision.
//
// [Service].WithTimeServers takes the list. The check is off by default, and
// ADVISORY: a server that does not answer, or whose answer does not verify, is
// no signal and the verification carries on, while one that answers verifiably
// and disagrees with the local clock by more than five minutes, widened by its
// own stated uncertainty, refuses with [ErrClockRegressed]. No server ships with
// the SDK: this client has been checked against its own test fixture and never
// against a live server, so adding one is a decision for whoever can watch it
// verify a real answer. It aliases the service type (ADR 0074).
type RoughtimeServer = svcent.RoughtimeServerValue

// UpdateRequiredError carries the two versions a version-floor refusal is
// about. It exists as a TYPE rather than a message because a caller that cannot
// read the required version out of the refusal re-runs the upgrade, gets the
// same refusal, and loops forever.
type UpdateRequiredError = svcent.UpdateRequiredError

// newWithGetter is NewWithGetter's body: decl_gen.go writes NewWithGetter, from the
// design, as one call of it.
func newWithGetter(client Getter, identity Identity, vendor []byte, product *Product) *Service {
	//: delegate verbatim to the service implementation.
	return svcent.NewServiceWithGetter(client, identity, vendor, product)
}

// requiresUpdate is RequiresUpdate's body: decl_gen.go writes RequiresUpdate, from the
// design, as one call of it.
func requiresUpdate(current, floor string) bool {
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
