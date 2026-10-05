// Package entitlement verifies a machine's entitlement to run a product:
// local key material plus a vendor-signed roster, turned into a grant or a
// typed refusal.
//
// It is the framework's public package over framework/internal/service/entitlement
// (ADR 0158): a distribution mechanism, built on pkg/v1 and nothing below it.
//
// # The trust model, and its ceiling
//
// One signer: the vendor. What the consuming binary links in is that signer's
// ed25519 public key, or — while a key is being rotated — an ordered list of the
// keys it accepts. Everything else — the roster, the per-subject keys, the host
// serving them — is untrusted input. A hostile endpoint can serve what it likes;
// it cannot forge a signature made with the vendor's private half.
//
// What that cannot survive is a patched binary. A check running on someone
// else's machine is removable by definition. This stops casual sharing and
// makes revocation real for cooperative installs, and it is deliberately not
// built as if it were more.
//
// # The anchor is a list, because one key has no way out
//
// A binary linking in exactly one anchor cannot be moved to another: rosters
// signed by a new key are refused against the anchor it holds — before anything
// reads the version floor that would have told it to upgrade — and the release
// carrying the new anchor is refused by the installation that needs it. Blocked
// from both sides, with no path left in band.
//
// NewWithAnchors takes an ORDERED list and a roster is authentic when it verifies
// against any entry, which is all rotation needs: publish under B, installations
// carrying {A, B} take it, installations carrying only {A} keep reading A until
// they are updated. A bundle already cached under either key is still a valid
// proof of time, so the anti-rollback ratchet is not reset by a rotation.
//
// The cost is real and is not hidden: an anchor on the list is a key whose
// compromise is accepted while it is listed. The list is therefore capped and is
// a BUILD decision — nothing at runtime can extend it — and an empty one refuses
// every document rather than accepting every document.
//
// # Identity is yours to supply
//
// Proving possession needs key material in a format the standard library
// cannot parse. Rather than pull golang.org/x/crypto/ssh — and through it
// golang.org/x/sys, which the SDK and its framework ban — into every consumer's
// graph, the machine's half of the proof is a PORT:
//
//	type Identity interface {
//		Discover() (subject string, err error)
//		Fingerprint(subject string) (fingerprint string, err error)
//		ProvePossession(subject string) error
//	}
//
// An ssh implementation ships as the connector module framework/connectors/ssh,
// for consumers who want one and accept the dependency: requiring it is what
// brings golang.org/x/crypto and golang.org/x/sys into a graph. A consumer that
// already handles its own key material — which is the common case for a product
// that enrols its users — implements three methods instead and never requires
// that module. ADR 0078 has the original measurement, ADR 0079 the split and
// ADR 0158 the move into the framework.
//
// # A proof can be bound to the key the roster authorised
//
// Identity's three methods cannot express "prove possession of the key the roster
// approved": the engine asks what you present, compares that answer itself, and
// then asks for a proof, so the authorised value never reaches your code. Anything
// that replaced the material in between is signed for.
//
// BoundProver is the sibling that carries it. Implement
// ProvePossessionFor(subject, authorised) and the verifier prefers it; do not, and
// nothing changes — the three-method port is frozen and stays supported. The SDK's
// own ssh implementation implements both.
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
//
// # An older genuine roster cannot undo a newer decision
//
// The ratchet also bears on ACCEPTANCE, not only on what gets cached. A roster
// the vendor signed but has since replaced is refused before it reaches the
// decision, so a lagging mirror or a substituted origin cannot restore a revoked
// subject, a rotated-out fingerprint, a lower mandatory-update floor, a withdrawn
// CI account or a relaxed CI policy. Equal signing instants are accepted when the
// signed payload is identical — which is every ordinary re-verification — and
// refused when it is not.
//
// It is CONDITIONAL on the cached bundle persisting, because that bundle IS the
// mark: the guarantee lapses for a Service built with no cache, for an install
// the filesystem refused, and for one that stood down on a contended lock. None
// of the three refuses instead; a contended lock turned into a refused licence
// would be worse than the replay it would prevent.
//
// # Honouring the deadline is the consumer's job
//
// Verify computes a Grant, hands it back and keeps no reference to it. There is
// no goroutine, no timer and no callback in this SDK that revokes anything when
// a grant's deadline passes, and a process holding an expired grant is not
// interrupted. Whatever gates work on a grant has to ask.
//
// So a grant expiring a second after a check keeps authorising until you look
// again, and how long that is, is your tick rather than a property of this
// package. What the package owes you is a deadline you can act on WITHOUT
// polling, and that is Grant.Deadline(): the EFFECTIVE instant, with the
// zero-NotAfter fallback already resolved. Schedule on it — a timer, a context
// deadline — and the question gets asked when the answer changes instead of on a
// cadence. Grant.Expired(now) is the same rule asked the other way round.
//
// # A CI seat cannot outlive its token
//
// BEHAVIOUR CHANGE. A grant issued for a proven GitHub Actions run is now bounded
// by the Actions token's own expiry as well as by the roster's window and the
// account's term. The effective bound on a CI seat therefore drops from up to 24 h
// to at most 30 minutes — maxTokenLifetime, and in practice the few minutes a real
// token carries. A long-running process on a runner that aged against a CI grant
// for hours must now re-verify inside that window.
//
// No clock skew is added to the bound. The two-minute allowance exists to ADMIT a
// token whose clock disagrees slightly, which is permissive and correct; applying
// it to a bound would extend the grant past the proof. Together they are what makes
// the exposure of a leaked token statable at all: one free seat PER VERIFIER for up
// to 32 minutes. A short token bounds the DURATION a stolen proof keeps working,
// never the NUMBER of verifiers that will accept it at once.
package entitlement
