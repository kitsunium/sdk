package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// PasswordPolicy is the policy of one secret field that holds a password's
// hash: how long a password is at least, and how many former passwords it
// refuses. [Store].Passwords declares it; Set, Change and Verify apply it.
type PasswordPolicy[T any] = ikit.PasswordPolicyService[T]

// PasswordOption configures a password policy: [MinLength], [NotReused].
type PasswordOption = ikit.PasswordConfigurer

// MinLength refuses a password shorter than n characters, counted as NIST
// counts them: one per Unicode code point. The default is 15, NIST's figure
// for a password used alone; 8 is the floor NIST allows with a second
// factor, and a smaller n is refused. There is no composition rule, ever:
// NIST says SHALL NOT.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MinLength(n int) PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MinLength(n)
}

// NotReused refuses a password that the current hash, or any of the n
// former ones, verifies — n from 1 to 100. It is a compliance policy, off by
// default: PCI DSS asks for it, OWASP ASVS 4.0.3 asks most products not to
// impose it. kit keeps the field's last n hashes, so it implies history=n,
// and a tag whose history is smaller than n is refused.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func NotReused(n int) PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NotReused(n)
}

// NotCommon refuses a password among the ten thousand most common ones —
// the SDK's password.IsCommon, which compares them without regard to case —:
// the blocklist NIST SP 800-63B-4 requires a verifier to check, beyond the
// three thousand OWASP ASVS 5.0 (6.2.4) asks for. Every policy checks it (ADR
// 0007 §2: on by default once the SDK has the list); the option says so
// where a policy is declared. Beside NIST's fifteen characters the list has
// little left to refuse; beside a shorter MinLength, it matters.
func NotCommon() PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NotCommon()
}
