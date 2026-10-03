// Package secret — the subject grammar, and the port that keeps the wrapped
// data key of each subject (ADR 0142).
package secret

import (
	"context"
	"iter"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MaxSubjectLen is the longest subject, in bytes: room for a SHA-512 digest in
// hexadecimal, the longest reference a caller has a reason to derive.
const MaxSubjectLen int = 128

// ValidateSubject reports whether subject is a subject reference, returning
// [InvalidSubject] when it is not.
//
// A subject names whoever or whatever a data key belongs to — a person, a
// tenant, a record — as the caller files it. It is 1 to [MaxSubjectLen] bytes
// of lowercase ASCII letters, digits, '-', '_', '.' and ':', starting with a
// letter or a digit. The alphabet is closed because a subject is a KEY in the
// caller's storage and a field of every box sealed under it:
//
//   - it is LOWERCASE only, because a store on a case-insensitive collation —
//     the default of several SQL engines — would file "Ab" and "aB" as one
//     subject, and two subjects sharing one data key erase each other;
//   - it carries no separator, space or control byte, so it stands in a
//     storage key, a log field and a box header without being quoted.
//
// A subject is a REFERENCE, never an identity: it is kept in clear in the
// store and in every box, so it must not be the e-mail address or the name it
// stands for. An HMAC of the identity under a key the caller keeps
// (pkg/v1/crypto/mac), written in hexadecimal, fits the grammar and tells whoever
// reads the store nothing.
//
// The refusal names the clause and never the rejected string: the string most
// likely to be refused here is the identity that should have been hashed.
func ValidateSubject(subject string) error {
	//: an empty subject designates nobody.
	if subject == "" {
		//: refused, naming the clause.
		return errs.Wrap(InvalidSubject, errs.WrapParams{}, errs.String("problem", "empty"))
	}
	//: a subject past the bound is not a reference anybody derived.
	if len(subject) > MaxSubjectLen {
		//: refused; the length is diagnostic and the string is not repeated.
		return errs.Wrap(InvalidSubject, errs.WrapParams{},
			errs.String("problem", "too long"), errs.Int("length", len(subject)))
	}
	//: the first character anchors the reference: no leading dot, dash or colon.
	if !isAnchor(subject[0]) {
		//: refused, naming the clause only.
		return errs.Wrap(InvalidSubject, errs.WrapParams{},
			errs.String("problem", "must start with a letter or a digit"))
	}
	//: every byte comes from the closed alphabet.
	for index := range len(subject) {
		//: a byte outside it is refused, never mapped, folded or dropped.
		if !isSubjectByte(subject[index]) {
			//: refused, naming the clause and the offending position only.
			return errs.Wrap(InvalidSubject, errs.WrapParams{},
				errs.String("problem", "character outside a-z, 0-9, '-', '_', '.' and ':'"), errs.Int("position", index))
		}
	}
	//: a subject every store can file.
	return nil
}

// isSubjectByte reports whether b belongs to the subject alphabet.
func isSubjectByte(b byte) bool {
	//: the anchors, and the four separators a derived reference uses.
	return isAnchor(b) || b == '-' || b == '_' || b == '.' || b == ':'
}

// SubjectKeyStore keeps one wrapped data key per subject: the bytes the
// subject-key engine (internal/service/security/secret, SubjectKeys) seals a subject's
// data key into under the root keyring, which nothing but that engine, holding
// the root they were wrapped under, can open. The caller implements it over
// its own storage — a document store, a table — or takes the memory one the
// service ships.
//
// Implementations MUST be safe for concurrent use, and Insert and Replace MUST
// be atomic across every process that shares the storage. That is the whole
// of what the engine asks, and a store that cannot promise it LOSES data
// rather than failing:
//
//   - an Insert that is not insert-if-absent lets two processes both create
//     the first key of one subject, and every box sealed under the key that
//     was overwritten opens nowhere again;
//   - a Replace that is not compare-and-swap lets a re-wrap write back a key an
//     erasure destroyed while the re-wrap ran, and the erasure is undone.
//
// Absent and taken are answers, not errors, as in the state-machine port (ADR
// 0120): Get reports found, Insert inserted, Replace replaced and Delete
// deleted — each false, with a nil error, for the ordinary case — so an error
// from a method always means the store FAILED.
//
// The engine validates every subject with [ValidateSubject] before it calls,
// and never touches a slice it handed over once the call returns; a store
// keeps its own copy, and a slice it returns belongs to the caller.
//
// The method set is FROZEN at five (ADR 0039): pkg/v1/security/secret aliases this
// interface, so a sixth method would break every implementation downstream. A
// new capability — an index that answers the oldest root version without a
// scan — arrives as a SIBLING interface reached by type assertion.
type SubjectKeyStore interface {
	// Get returns the wrapped key filed under subject, and found == false —
	// with a nil error — when there is none.
	Get(ctx context.Context, subject string) (wrapped []byte, found bool, err error)
	// Insert files wrapped under subject when no key is filed there. When one
	// is, it stores nothing and reports inserted == false with a nil error.
	Insert(ctx context.Context, subject string, wrapped []byte) (inserted bool, err error)
	// Replace files next under subject only while subject still holds
	// current, byte for byte. When it holds anything else — or nothing,
	// because the key was destroyed meanwhile — it stores nothing and reports
	// replaced == false with a nil error: a re-wrap never brings back a key
	// an erasure destroyed.
	Replace(ctx context.Context, subject string, current, next []byte) (replaced bool, err error)
	// Delete removes the key filed under subject and reports whether there
	// was one. It is the destruction a cryptographic erase rests on, so the
	// key must leave the storage the store READS — a document store folds
	// what it overwrote, a table deletes the row. Copies the backend keeps
	// elsewhere — a write-ahead log, a replica, a backup — stay wrapped under
	// the root version that sealed them, and open only while it is kept.
	Delete(ctx context.Context, subject string) (deleted bool, err error)
	// All yields every filed key once, then stops; an error ends the
	// sequence. The engine replaces keys while it ranges, so an
	// implementation holds no lock across a yield, and a key filed during
	// the sequence may or may not be yielded.
	All(ctx context.Context) iter.Seq2[SubjectKeyValue, error]
}

// SubjectKeyValue is one subject's wrapped data key, as a [SubjectKeyStore]
// keeps it and yields it from All.
//
// pkg/v1/security/secret aliases it as WrappedKey. It is a published concrete shape
// the SDK hands to callers, so ADR 0040 applies: a caller destructuring it
// positionally breaks on an added field, and the licence to change it ends at
// v1.
type SubjectKeyValue struct {
	// Subject is whom the key belongs to, in the grammar [ValidateSubject]
	// accepts.
	Subject string
	// Wrapped is the data key sealed under the root keyring: ciphertext,
	// bound to Subject, readable by nothing without the root.
	Wrapped []byte
}
