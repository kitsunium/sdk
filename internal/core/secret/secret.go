// Package secret declares the secret domain (ADR 0096): the [Value] a secret
// travels in, the [Store] port that keeps named secrets as numbered versions,
// the [VersionValue] a store hands back, and the one name grammar every store
// shares. Concrete stores — in memory, the process environment, a directory on
// disk — the keyring that sees a secret's versions as keys, and the rotator
// that mints them live in internal/service/secret; this package owns only the
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
	"context"
	"time"
)

// Store keeps named secrets, each as a sequence of numbered versions.
// Implementations MUST be safe for concurrent use.
//
// Versions are numbered from 1, strictly increasing, and a number is never
// reused — not after a prune, not after a restart — because a version number
// is how a sealed box or a signature names the key that made it, and a reused
// number would hand an old box to a new key.
//
// The method set is five operations wide and is FROZEN: pkg/v1/secret aliases
// this interface, so under ADR 0039 a sixth method would break every
// downstream implementation at compile time. A new capability — deleting a
// secret outright, a conditional put — arrives as a SIBLING interface reached
// by type assertion, never as a widened Store.
//
// Every method validates its name through [ValidateName] first and refuses a
// malformed one with [InvalidName] before touching the backend.
//
// IFACE-PLUGIN: the concrete stores stay unexported behind their constructors
// in internal/service/secret.
type Store interface {
	// Get returns the CURRENT version of name — the highest number kept. It
	// returns [NotFound] for a name that holds no version.
	Get(ctx context.Context, name string) (current VersionValue, err error)
	// Versions returns every kept version of name, NEWEST FIRST, so the head
	// of the slice is what Get returns. It returns [NotFound] for a name that
	// holds no version, rather than an empty slice a caller might iterate
	// over as if it had found a secret with no history.
	Versions(ctx context.Context, name string) (versions []VersionValue, err error)
	// Put stores value as a new version of name — one past the highest ever
	// kept, or 1 for a new name — and returns it. It refuses an empty value
	// with [EmptyValue], and a store that only reads refuses every Put with
	// [ReadOnly].
	Put(ctx context.Context, name string, value Value) (created VersionValue, err error)
	// Prune drops every version of name except the newest keep. keep below 1
	// is refused with [InvalidKeep]: pruning never deletes a secret. Pruning a
	// name that already holds no more than keep versions is not an error.
	Prune(ctx context.Context, name string, keep int) error
	// Names lists every name holding at least one version, sorted. It never
	// returns a value: a caller that wants one asks for it by name.
	Names(ctx context.Context) (names []string, err error)
}

// VersionValue is one version of one secret: which secret, which version, the
// secret itself, and when the store recorded it.
//
// It renders safely: [Value] redacts itself, so %+v of a VersionValue, or its
// JSON, prints the name, the number and the instant, and the placeholder where
// the secret would be.
//
// pkg/v1/secret aliases it as Versioned. It is a published concrete shape the
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
