package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// EraseAfter erases a record's personal data delay after the instant since
// returns — its closing, say; false means "not yet": an open record is never
// due. delay is a duration, or a duration setting.
func EraseAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.EraseAfter[T, D](delay, since)
}

// EraseAt erases a record's personal data at the instant at returns: one the
// product computed and stored on the record — a retention in calendar
// months, one that depends on a tenant.
func EraseAt[T any](at func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.EraseAt[T](at)
}

// DeleteAfter deletes a record delay after the instant since returns.
func DeleteAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.DeleteAfter[T, D](delay, since)
}

// DeleteAt deletes a record at the instant at returns.
func DeleteAt[T any](at func(T) (time.Time, bool)) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.DeleteAt[T](at)
}

// Anonymise says what an erased record keeps: fn runs first, and generalises
// into unclassified fields — a birth date to its year, an address to its
// region; then the framework clears the rest. The framework cannot prove that what remains
// identifies no one: by declaring the function, the product asserts it.
func Anonymise[T any](fn func(*T)) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Anonymise[T](fn)
}

// HeldUntil holds each record until the instant until returns: the law's own
// retention, such as an invoice kept ten years (GDPR art. 17(3)(b)). reason
// is the legal ground, for the register. A held record is exported and
// updated like any other, and neither erased nor deleted.
func HeldUntil[T any](until func(T) (time.Time, bool), reason string) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.HeldUntil[T](until, reason)
}

// Purpose says why the store keeps its records (GDPR art. 30(1)(b)): the
// register lists it, and a person's export carries it.
func Purpose(text string) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Purpose(text)
}

// DeleteOnErasure makes a person's erasure (app.Erase) delete the store's
// records instead of clearing them, as sessions and accounts want.
func DeleteOnErasure() StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.DeleteOnErasure()
}

// RetentionByProduct says the product keeps the store's retention itself:
// its own code erases and deletes what it no longer needs — a record whose
// life the product's own rules end, a lifecycle the framework cannot read from the
// entity. limits says, in the product's words, for how long and how, which
// the register publishes as the store's time limits (GDPR art. 30(1)(f));
// "" says only that the product keeps them.
//
// The framework then runs no retention for the store, and warns of neither a missing
// retention nor a missing subject. Its classified fields keep every other
// promise: sealed at rest, never shown, exported with their person — when
// the store names one — and erased by app.Erase and Store.Erase. It goes
// with app.Purpose, app.HeldUntil, app.Anonymise and app.DeleteOnErasure;
// with app.EraseAfter, app.EraseAt, app.DeleteAfter or app.DeleteAt it is
// refused: a store's retention is the framework's or the product's, never both.
func RetentionByProduct(limits string) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.RetentionByProduct(limits)
}
