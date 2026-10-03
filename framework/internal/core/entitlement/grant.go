// Package entitlement - the record of a verification that succeeded. A daemon
// ages against it: memory of a past check must not outlive the roster window,
// otherwise revocation would never reach a long-running process.
package entitlement

import "time"

// GrantValue records a verification that succeeded, and how long that
// verification may be remembered.
//
// The timestamp alone is not enough to age against, which is the defect this
// type carried until NotAfter existed. A grant used to expire at
// VerifiedAt+RosterLifetime and nothing else, so a daemon that happened to
// verify late in a roster's window kept serving for a further 24 hours after
// that window closed — up to 47 hours after the vendor last signed anything,
// in a scheme whose single dial is a 24-hour bound. It outlived the subject's
// OWN term too: a licence expiring at noon left a daemon that verified at
// 11:59 serving until the following day.
type GrantValue struct {
	// Subject is the client UUID that was verified.
	Subject string
	// VerifiedAt is when the roster was last successfully authenticated.
	VerifiedAt time.Time
	// NotAfter is the instant this grant stops authorising anything: the
	// EARLIEST of the verification's own lifetime and every document the
	// verification rested on — the roster's expiry, the subject's term, and
	// for a CI seat the expiry of the Actions token that proved the run.
	// Each bounds something different, so the tightest is the only correct
	// answer — a grant may not outlive the document that authorised it.
	//
	// The zero value means "no deadline was computed", which is how a grant
	// seeded from a bare timestamp behaves: serve.go does exactly that at
	// start-up, from the root gate's verification, with no roster in scope.
	// The fallback is the verification's own lifetime rather than reading a
	// zero deadline as already-past, which would refuse a daemon at the very
	// instant it starts.
	//
	// Which is why a caller that wants to ACT on the deadline should read
	// Deadline() and not this field: the method resolves that fallback and the
	// field cannot. Honouring either is the consumer's job — nothing in this
	// SDK revokes anything when the instant passes. See Expired.
	NotAfter time.Time
	// Offline records that no origin answered and the decision rested on the
	// last bundle this machine authenticated.
	//
	// It changes nothing about the grant's authority: an offline roster
	// passed the identical signature and window checks, and NotAfter above
	// already bounds it by that roster's own expiry. It exists so the fact can
	// be SAID. A binary quietly running on a copy of a document it can no
	// longer fetch is the one state where "it worked" and "it is still true"
	// come apart, and the operator is the only party who can act on that.
	Offline bool
}

// GrantDeadline computes NotAfter from the verification's own lifetime and
// however many further bounds the caller can name.
//
// A zero expiry means "not recorded" throughout this package — an absent
// subject term, or a roster field a predecessor never wrote — and must never
// be read as "already expired". Zero values are therefore skipped rather than
// minimised over: otherwise the earliest bound would always be the zero time
// and every grant would be born dead.
//
// # Why the bounds are variadic
//
// Because "a grant may not outlive the document that authorised it" is a rule
// about however many documents there were, and the CI path has three. It was
// written as two fixed parameters — the roster's window and the subject's term —
// which are exactly the two a DEVICE grant rests on. A CI seat rests on a third:
// the Actions token that proved the run is real, whose own expiry GitHub sets
// minutes out. ciseat.go therefore bounded the seat by the roster and the
// account's term and by nothing else, so a grant established by a 30-minute proof
// survived it by up to a day — the invariant NotAfter's own comment states,
// contradicted by the one caller that had a document the signature said the least
// about.
//
// Variadic rather than a fourth parameter so the two existing call sites compile
// unchanged and read unchanged; a caller with nothing extra to name passes
// nothing extra.
//
// # No skew here, deliberately
//
// checkTiming allows clockSkew when ADMITTING an Actions token, which is the
// permissive direction and the right one there: refusing a genuine token over two
// minutes of drift would break a working runner. Adding the same allowance to a
// BOUND would run the opposite way — it would extend the grant past the proof —
// so the bound is the claim as written.
func GrantDeadline(verifiedAt time.Time, bounds ...time.Time) time.Time {
	deadline := verifiedAt.Add(RosterLifetime)
	//: Each bound closes something the grant rests on — the roster's window, a
	//: subject's term, the proof a CI run was real — so the tightest of them is
	//: the only answer that keeps the grant inside all of them.
	for _, bound := range bounds {
		//: Zero is "not recorded" and never "already expired", which is what
		//: makes a roster predating a field harmless.
		if !bound.IsZero() && bound.Before(deadline) {
			deadline = bound
		}
	}
	//: The tightest bound in play.
	return deadline
}

// Deadline reports the instant this grant stops authorising anything — the
// EFFECTIVE one, which is not always the NotAfter field.
//
// # Why the field is not enough
//
// NotAfter's zero value means "no deadline was computed", and Expired then ages
// the grant against VerifiedAt+RosterLifetime instead. That fallback lived
// inside Expired and nowhere else, so a caller reading the FIELD to schedule its
// next check read the zero instant and had no way to derive the deadline the SDK
// would actually apply. Its only remaining option was to poll — which is
// precisely how a grant that expires a second after a check keeps authorising
// until the next tick, whatever the tick is.
//
// With the effective instant readable, a consumer can wake AT the deadline
// rather than sometime after it: arm a timer on Deadline(), or pass it to a
// context. That is the SDK's whole contribution to the problem, and the rest is
// stated in Expired's own comment.
//
// Expired is defined in terms of this method rather than repeating the fallback,
// because two copies of one rule is how a field and a method come to disagree
// about when a grant died.
//
// A POINTER receiver for the same reason Expired has one — see there.
func (g *GrantValue) Deadline() time.Time {
	//: A grant seeded without a roster in scope carries no deadline; its bound
	//: is the verification's own lifetime, as this type's only bound was before
	//: NotAfter existed. Reading zero as a past instant would make every such
	//: grant born dead.
	if g.NotAfter.IsZero() {
		//: The verification's own lifetime.
		return g.VerifiedAt.Add(RosterLifetime)
	}
	//: The tightest bound the verification recorded.
	return g.NotAfter
}

// Expired reports whether a grant may no longer be relied on. A daemon that
// cannot refresh must stop trusting its own memory eventually, otherwise
// revocation would never reach it.
//
// # Honouring the deadline is the CONSUMER's job, and nothing here enforces it
//
// This is a value type. Verify computes a grant, hands it back and keeps no
// reference to it: there is no goroutine, no timer and no callback anywhere in
// this SDK that revokes anything when Deadline passes, and a process holding an
// expired grant is not interrupted. Whatever gates work on a grant has to ask.
//
// So a grant expiring a second after a check keeps authorising until the
// consumer looks again, and how long that is, is the consumer's tick — not a
// property of this domain. What the domain owes is a deadline that can be acted
// on WITHOUT polling, which is what Deadline is for: schedule on it, and the
// question is asked when the answer changes rather than on a cadence.
//
// Said here rather than only in an ADR, because a consumer reading this method
// is exactly the reader who might otherwise assume something upstream is
// watching the clock for them.
//
// A POINTER receiver on a read-only method, which is unusual and deliberate:
// the struct outgrew KTN-VAR-BIGSTRUCT's by-value threshold when Offline was
// added, and every caller passes this as a METHOD VALUE
// (`licenseStillValidFrom(svc, refusal, grant.Expired)`) that would otherwise
// copy the whole struct into the closure on every watchdog tick.
//
// It is a method-set change and therefore a source-level API change, named here
// rather than left to be discovered: Expired is no longer in GrantValue's VALUE
// method set, so a NON-ADDRESSABLE grant — a map entry, a composite literal
// used inline — stops compiling against it. Every caller in this repository
// holds an addressable grant, and pkg/license has no consumer outside the
// module, which is what makes the trade payable; a released SDK would not.
func (g *GrantValue) Expired(now time.Time) bool {
	//: Through Deadline, never past it: the zero-NotAfter fallback is one rule
	//: and it lives in one place, or a caller scheduling on the deadline and
	//: this method eventually disagree about when the grant died.
	return now.After(g.Deadline())
}
