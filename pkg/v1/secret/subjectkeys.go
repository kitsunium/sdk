// Package secret — subject keys: one data key per subject under a rotating
// root, and the erasure that destroys it (ADR 0142).
package secret

import (
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// MaxSubjectLen is the longest subject reference, in bytes.
const MaxSubjectLen int = coresecret.MaxSubjectLen

// SubjectKeys keeps one data key per subject, wrapped by a root [Keyring]:
// Seal and Open under a subject's key, Destroy to erase everything it sealed,
// Rewrap after the root rotates.
type SubjectKeys = svcsecret.SubjectKeys

// SubjectKeysConfig parameterises [NewSubjectKeys].
type SubjectKeysConfig = svcsecret.SubjectKeysConfig

// SubjectKeyStore keeps one wrapped data key per subject. The caller
// implements it over its own storage — Insert and Replace atomic across every
// process — or uses [NewMemorySubjectKeyStore]. It is frozen at five methods;
// a new capability arrives as a sibling interface.
type SubjectKeyStore = coresecret.SubjectKeyStore

// WrappedKey is one subject's wrapped data key, as a [SubjectKeyStore] keeps
// it.
type WrappedKey = coresecret.SubjectKeyValue

// RewrapReport says what one [SubjectKeys].Rewrap pass did, key by key.
type RewrapReport = svcsecret.RewrapValue

var (
	// InvalidSubject is returned for a subject outside the grammar; the
	// rejected string is never repeated.
	InvalidSubject = coresecret.InvalidSubject
	// KeyDestroyed is returned by SubjectKeys.Open for a box whose data key is
	// not held: the value was erased.
	KeyDestroyed = svcsecret.KeyDestroyed
	// SubjectKeyUnreadable is returned when a subject's data key is held and
	// does not unwrap under the root: a fault, never an erasure.
	SubjectKeyUnreadable = svcsecret.SubjectKeyUnreadable
)

// ValidateSubject reports whether subject is a subject reference, returning
// [InvalidSubject] when it is not: 1 to [MaxSubjectLen] bytes of a-z, 0-9,
// '-', '_', '.' and ':', starting with a letter or a digit. A subject is kept
// in clear, so it is a reference derived from an identity, never the identity.
func ValidateSubject(subject string) error {
	//: delegate to the core grammar.
	return coresecret.ValidateSubject(subject)
}

// NewSubjectKeys returns the engine that keeps one data key per subject under
// cfg.Root, filed in cfg.Store. It refuses a nil root or store, a negative
// cache, and a cache without a TTL — the TTL bounds how long a key another
// process destroyed keeps opening here — with [InvalidConfig].
func NewSubjectKeys(cfg SubjectKeysConfig) (keys *SubjectKeys, err error) {
	//: delegate to the service constructor.
	return svcsecret.NewSubjectKeys(cfg)
}

// NewMemorySubjectKeyStore returns a [SubjectKeyStore] that keeps the wrapped
// keys in this process's memory — for tests and development, where the data
// they protect dies with the process too.
func NewMemorySubjectKeyStore() SubjectKeyStore {
	//: delegate to the service constructor.
	return svcsecret.NewMemorySubjectKeyStore()
}

// SubjectOf returns the subject a box sealed by [SubjectKeys].Seal names,
// without opening it, or [SealInvalid] for anything that is not such a box.
func SubjectOf(box []byte) (subject string, err error) {
	//: delegate to the service parser.
	return svcsecret.SubjectOf(box)
}
