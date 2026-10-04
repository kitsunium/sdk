// Package secret — the subject grammar, and the port that keeps the wrapped
// data key of each subject (ADR 0142).
package secret

import (
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
