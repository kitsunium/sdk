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

// minLength is MinLength's body: decl_gen.go writes MinLength, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func minLength(n int) PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MinLength(n)
}

// notReused is NotReused's body: decl_gen.go writes NotReused, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func notReused(n int) PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NotReused(n)
}

// notCommon is NotCommon's body: decl_gen.go writes NotCommon, from the
// design, as one call of it.
func notCommon() PasswordOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NotCommon()
}
