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
//
// Frozen means frozen: it is reachable through a pkg/v1 type alias, so ADR 0039
// binds and ADR 0040 §4 removes the v0 licence from it — the version does not
// matter, because structural satisfaction breaks downstream implementations at
// any version. It is extended by a SIBLING, and BoundProver below is the first
// one.
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
	//
	// It proves possession of whatever material this implementation holds NOW,
	// which is not the same claim as "possession of the key the roster
	// authorised" — nothing the roster said reaches this method. BoundProver is
	// the sibling that carries the missing half; an implementation that can make
	// the stronger claim implements it, and the engine prefers it when present.
	ProvePossession(subject string) error
}

// BoundProver is Identity's sibling for the one claim the three methods cannot
// make: possession of the key the ROSTER authorised, rather than of whatever
// this machine holds at the moment it is asked.
//
// # The gap it closes
//
// The verification engine asks Fingerprint what this machine presents, compares
// the answer ITSELF against the roster's entry, and then asks ProvePossession
// for a proof. The authorised fingerprint never traverses the port, so an
// implementation cannot bind the proof to it: between the two calls the material
// may be replaced — a rotation, a mounted volume remounted, a deliberate swap —
// and the implementation signs with what it finds on the second call. A consumer
// can DETECT that by remembering the key object across the two calls and
// refusing a proof with nothing remembered, which is as far as the three methods
// reach; it cannot be told which key it was supposed to be holding.
//
// # Why a sibling and not a fourth parameter
//
// Identity is reachable through a pkg/v1 type alias, so ADR 0039's rule binds
// and ADR 0040 §4 says the version does not matter: a published interface is
// extended by a sibling, never by widening, because Go satisfies interfaces
// STRUCTURALLY and every downstream implementation breaks at compile time with
// no deprecation window. Widening ProvePossession would be worse than adding a
// method — it invalidates a method implementers have already written — and it
// would not even buy the guarantee that tempts it: an implementation can satisfy
// a two-parameter signature and ignore the second argument, so binding is the
// implementation's choice under either shape. The sibling buys the same real
// property and breaks nobody.
//
// # What its ABSENCE means, stated rather than left to be discovered
//
// An Identity that does not implement this keeps the three-method behaviour
// exactly: the engine falls back to ProvePossession and the window between the
// two calls stays open. That is not a mechanism this package can close from
// here, and pretending otherwise would be the claim that outruns the code.
type BoundProver interface {
	// ProvePossessionFor demonstrates that this machine holds the private half
	// of the key whose PUBLISHED fingerprint is authorised — the value the
	// roster records for this subject, in the roster's own spelling.
	//
	// A nil error is the proof, for the same reason it is on ProvePossession.
	// An implementation that finds it holds different material refuses with
	// ErrKeyMismatch: that is what "a local key that does not match the
	// published fingerprint" means, so the refusal taxonomy does not grow.
	//
	// authorised is compared however the implementation compares fingerprints —
	// byte equality, in the roster's spelling, is what Fingerprint's own
	// contract already requires.
	ProvePossessionFor(subject, authorised string) error
}
