// Package secret — the secret-name grammar, the one every store shares.
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// MaxNameLen is the longest secret name, in bytes. It is the length of a DNS
// label (RFC 1123), and the grammar is that label's: long enough for any name
// a person chooses, short enough for every filesystem and every environment.
const MaxNameLen int = 63

// ValidateName reports whether name is a secret name, returning [InvalidName]
// when it is not.
//
// A name is 1 to [MaxNameLen] characters of lowercase ASCII letters, digits
// and '-', starting and ending with a letter or a digit — the DNS label
// grammar. Every store validates with this function, and the alphabet is
// closed rather than merely filtered because a name crosses several naming
// systems at once:
//
//   - it is a FILE name in the file store, so it carries no path separator, no
//     leading dot and no character a filesystem treats specially;
//   - it is LOWERCASE only, because two names differing only by case would be
//     two secrets on Linux and one file on the case-insensitive filesystems
//     macOS and Windows ship by default;
//   - it maps to an environment VARIABLE by upper-casing and turning '-' into
//     '_', and that mapping is one-to-one only because '_' itself is not in the
//     alphabet — with it, "smtp-url" and "smtp_url" would both read SMTP_URL.
//
// The refusal names the clause and never the rejected string. A valid name is
// not a secret and travels in other verdicts' fields; an INVALID one is
// whatever a caller passed, and the classic way to pass something invalid is
// to hand the value where the name belonged.
func ValidateName(name string) error {
	//: an empty name designates nothing.
	if name == "" {
		//: refused, naming the clause.
		return errs.Wrap(InvalidName, errs.WrapParams{}, errs.String("problem", "empty"))
	}
	//: a name past the bound cannot be a DNS label, and is not one anybody typed.
	if len(name) > MaxNameLen {
		//: refused; the length is diagnostic and the string is not repeated.
		return errs.Wrap(InvalidName, errs.WrapParams{},
			errs.String("problem", "too long"), errs.Int("length", len(name)))
	}
	//: the first and last characters anchor the label; a hyphen may not.
	if !isAnchor(name[0]) || !isAnchor(name[len(name)-1]) {
		//: refused, naming the clause only.
		return errs.Wrap(InvalidName, errs.WrapParams{},
			errs.String("problem", "must start and end with a letter or a digit"))
	}
	//: every byte in between comes from the closed alphabet.
	for index := range len(name) {
		//: a byte outside it is refused, never mapped or dropped.
		if !isAnchor(name[index]) && name[index] != '-' {
			//: refused, naming the clause and the offending position only.
			return errs.Wrap(InvalidName, errs.WrapParams{},
				errs.String("problem", "character outside a-z, 0-9 and '-'"), errs.Int("position", index))
		}
	}
	//: a name every store can spell.
	return nil
}

// isAnchor reports whether b is a lowercase ASCII letter or a digit — the
// characters a name may start and end with.
func isAnchor(b byte) bool {
	//: two contiguous ASCII ranges, and nothing outside them.
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
