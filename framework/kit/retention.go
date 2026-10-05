package kit

import (
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// EraseAfter erases a record's personal data delay after the instant since
// returns — its closing, say; false means "not yet": an open record is never
// due. delay is a duration, or a duration setting.
func EraseAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.EraseAfter[T, D](delay, since)
}

// EraseAt erases a record's personal data at the instant at returns: one the
// product computed and stored on the record — a retention in calendar
// months, one that depends on a tenant.
func EraseAt[T any](at func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.EraseAt[T](at)
}

// DeleteAfter deletes a record delay after the instant since returns.
func DeleteAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DeleteAfter[T, D](delay, since)
}

// DeleteAt deletes a record at the instant at returns.
func DeleteAt[T any](at func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DeleteAt[T](at)
}

// Anonymise says what an erased record keeps: fn runs first, and generalises
// into unclassified fields — a birth date to its year, an address to its
// region; then kit clears the rest. kit cannot prove that what remains
// identifies no one: by declaring the function, the product asserts it.
func Anonymise[T any](fn func(*T)) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Anonymise[T](fn)
}

// HeldUntil holds each record until the instant until returns: the law's own
// retention, such as an invoice kept ten years (GDPR art. 17(3)(b)). reason
// is the legal ground, for the register. A held record is exported and
// updated like any other, and neither erased nor deleted.
func HeldUntil[T any](until func(T) (time.Time, bool), reason string) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.HeldUntil[T](until, reason)
}

// purpose is Purpose's body: decl_gen.go writes Purpose, from the
// design, as one call of it.
func purpose(text string) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Purpose(text)
}

// deleteOnErasure is DeleteOnErasure's body: decl_gen.go writes DeleteOnErasure, from the
// design, as one call of it.
func deleteOnErasure() StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DeleteOnErasure()
}

// retentionByProduct is RetentionByProduct's body: decl_gen.go writes RetentionByProduct, from the
// design, as one call of it.
func retentionByProduct(limits string) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RetentionByProduct(limits)
}
