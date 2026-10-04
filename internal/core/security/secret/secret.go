// Package secret declares the secret domain (ADR 0096): the [Value] a secret
// travels in, the [Store] port that keeps named secrets as numbered versions,
// the [VersionValue] a store hands back, and the one name grammar every store
// shares. Concrete stores — in memory, the process environment, a directory on
// disk — the keyring that sees a secret's versions as keys, and the rotator
// that mints them live in internal/service/security/secret; this package owns only the
// contract.
//
// # What the domain is for
//
// A secret has three properties no other value in a program has, and each of
// them is a mechanism here rather than a convention a caller must remember:
//
//   - it must not be WRITTEN DOWN by accident. [Value] renders as a fixed
//     placeholder in every rendering there is, so a struct printed while
//     debugging, a configuration dumped as JSON, and an attribute a logger
//     formats all print the placeholder, and only an explicit Reveal gets the
//     bytes out;
//   - it CHANGES. A key that is never rotated is a key whose compromise is
//     permanent, so a store keeps numbered versions instead of one value, and
//     the newest is current while the older ones stay readable until pruned;
//   - it comes from SOMEWHERE ELSE — an orchestrator, a mounted volume, a
//     directory the process owns. The [Store] port is the seam that lets the
//     same code read any of them, and the one a Vault, KMS or cloud secret
//     manager plugs into later without the code that reads secrets changing.
//
// # There is no registry
//
// Like lock (ADR 0052) and session (ADR 0045), this domain has no
// name->implementation registry. Which store holds a deployment's secrets is a
// decision made in code where the program is wired: resolving it from a
// configuration string would let a typo turn an encrypted store into an
// environment lookup, and the first symptom would be a missing secret in
// production.
package secret

import (
	"time"
)

// VersionValue is one version of one secret: which secret, which version, the
// secret itself, and when the store recorded it.
//
// It renders safely: [Value] redacts itself, so %+v of a VersionValue, or its
// JSON, prints the name, the number and the instant, and the placeholder where
// the secret would be.
//
// pkg/v1/security/secret aliases it as Versioned. It is a published concrete shape the
// SDK RETURNS, so ADR 0040 applies with its full cost: a caller destructuring
// it positionally breaks on an added field, and the licence to change it ends
// at v1.
type VersionValue struct {
	// Name is the secret's name, in the grammar [ValidateName] accepts.
	Name string
	// Version is this version's number: 1 for the first, strictly increasing,
	// never reused.
	Version int
	// Value is the secret.
	Value Value
	// Created is when the store recorded this version, from the store's own
	// clock. A store that cannot know — the environment does not say when a
	// variable was set — reports the zero time, and says so in its doc.
	Created time.Time
}
