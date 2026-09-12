// Package entitlement is the contract for machine entitlement: turning local
// key material plus a vendor-signed roster into a grant, or a typed refusal.
//
// The trust model has exactly one anchor: the vendor's ed25519 public key,
// linked into the consuming binary. Everything else — the roster, the
// per-subject keys, the host serving them — is untrusted input. A hostile
// endpoint can serve whatever it likes; it cannot forge a signature made with
// the vendor's private half.
//
// What that anchor cannot do is survive a patched binary. A check running on
// someone else's machine is removable by definition. This domain stops casual
// sharing and makes revocation real for cooperative installs; it is not a wall
// against a determined attacker, and is deliberately not built as if it were.
//
// # Why Identity is a port
//
// Proving possession needs key material the user already has, in a format the
// standard library cannot parse — OpenSSH, via golang.org/x/crypto/ssh, which
// brings golang.org/x/sys and is banned SDK-wide (ADR 0078). Naming the
// capability as a port keeps the MECHANISM here, stdlib-only, and puts the one
// implementation that needs a vendor dependency where a vendor dependency is
// allowed: third-party/, which a consumer opts into.
//
// A consumer that does not want that dependency supplies its own Identity. That
// is not a theoretical escape hatch — the product this was versed from does
// exactly that, keeping its 327 lines of SSH handling rather than pulling the
// root module's whole graph for them.
//
// There is **no registry**. A registry's key would name an identity scheme, and
// the whole trust chain is anchored to one vendor key for one product.
package entitlement

// Identity is the machine's half of the proof: which subject this machine
// claims to be, and evidence that it holds the private key the roster publishes
// a fingerprint for.
//
// It is FROZEN at three methods, and the split is what makes the refusals
// distinguishable. Discover answers "who are we", Fingerprint answers "what do
// we present", ProvePossession answers "can we back it up" — and a caller that
// fails the third after passing the second has key material it cannot use,
// which is a different operator situation from having none at all.
type Identity interface {
	// Discover returns the subject identifier this machine is enrolled as.
	//
	// It refuses rather than choosing when more than one identity is present:
	// picking one silently would make revocation unverifiable, since the
	// operator could not tell which identity was checked.
	Discover() (subject string, err error)
	// Fingerprint returns the published fingerprint of the subject's public
	// key, in whatever spelling the roster uses. It is compared by byte
	// equality against the roster's entry and never parsed.
	Fingerprint(subject string) (fingerprint string, err error)
	// ProvePossession demonstrates that this machine holds the private half of
	// the key whose fingerprint Fingerprint returned. A nil error is the proof;
	// there is deliberately no value to inspect, because anything returned here
	// would be a second thing to verify.
	ProvePossession(subject string) error
}
