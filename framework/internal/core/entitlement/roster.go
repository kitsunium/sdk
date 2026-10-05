package entitlement

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RosterLifetime bounds how long a verified grant survives without a fresh
// fetch. It is the single dial of the whole scheme: it caps both how long a
// revoked subject keeps working and how long a hostile endpoint can replay a
// genuine roster.
const RosterLifetime time.Duration = 24 * time.Hour

// ciEntitlementStage names the CI-seat lookup in the stage field of every
// refusal it raises. A constant rather than four literals: the four refusals
// are one operation, and a typo in one of them would split it in a log query.
const ciEntitlementStage string = "ci_entitlement"

// subjectLookupStage names the device-subject lookup in the stage field of the
// refusal it raises.
const subjectLookupStage string = "subject_lookup"

// CIEntitlementFor returns what the roster grants an account's CI runs.
//
// Absence is not an error worth distinguishing from a refusal here: either
// way the caller falls back to the device path, which is what an
// unentitled CI run should do.
//
// All four refusals render the SAME sentence, which is deliberate: a refusal
// naming the account it refused hands the party being kept out a way to read
// the roster one probe at a time. Which account, and which deadline, travel as
// FIELDS — errs.FieldsOf reaches them, err.Error() does not — and the condition
// field is what keeps "no roster at all" distinguishable from "this account has
// no seat" for the operator who is entitled to the difference.
func (r *RosterValue) CIEntitlementFor(accountID string, now time.Time) (entitlement CIEntitlementValue, err error) {
	//: A nil roster would panic on the lookup below. Refusing instead is the
	//: only acceptable answer: this path must never do worse than fall back
	//: to the device check, and taking the process down over a CI seat is
	//: very much worse.
	if r == nil {
		//: Report the refusal.
		return CIEntitlementValue{}, errs.Wrap(ErrCINotEntitled, errs.WrapParams{},
			errs.String("stage", ciEntitlementStage),
			errs.String("condition", "no roster to check against"))
	}
	//: An empty id cannot match anything, and treating it as a lookup would
	//: let a token with no owner claim whatever an empty key happened to hold.
	if accountID == "" {
		//: Refuse rather than look up nothing.
		return CIEntitlementValue{}, errs.Wrap(ErrCINotEntitled, errs.WrapParams{},
			errs.String("stage", ciEntitlementStage),
			errs.String("condition", "no account id to match"))
	}
	recorded, listed := r.CIAccounts[accountID]
	//: An account the roster does not list gets no free seat. That covers a
	//: licence whose last device was revoked, one that never recorded an id,
	//: and an account with no licence at all.
	if !listed {
		//: Report the refusal, with the account in a field rather than in the
		//: sentence.
		return CIEntitlementValue{}, errs.Wrap(ErrCINotEntitled, errs.WrapParams{},
			errs.String("stage", ciEntitlementStage),
			errs.String("condition", "no entry for this account id"),
			errs.String("account_id", accountID))
	}
	//: A term that has closed stops CI as surely as it stops a device. Zero
	//: means none was recorded, not one that closed in 1970.
	if !recorded.ExpiresAt.IsZero() && now.After(recorded.ExpiresAt) {
		//: Report the expired entitlement. The deadline is a field for the
		//: same reason the account is: it is a fact about the licence, and a
		//: refusal is not where an unentitled caller learns facts about it.
		return CIEntitlementValue{}, errs.Wrap(ErrCINotEntitled, errs.WrapParams{},
			errs.String("stage", ciEntitlementStage),
			errs.String("condition", "the account's CI seat has expired"),
			errs.String("account_id", accountID),
			errs.String("expired_at", recorded.ExpiresAt.UTC().Format(time.RFC3339)))
	}
	//: Entitled, for as long as the licence is.
	return recorded, nil
}

// SubjectFor returns the roster's record for a subject. A missing entry
// reports ErrRevoked, but the roster is a stateless snapshot with no
// history: absence is exactly how a withdrawn licence looks, and it is
// indistinguishable here from a subject whose enrolment was never approved
// in the first place. Callers surfacing this to a human must name both
// possibilities rather than assert the more alarming one as fact.
//
// The refused subject travels as a FIELD and not in the sentence. A revocation
// that quotes back the uuid it refused confirms that uuid to whoever presented
// it, which is a thing a refusal has no business doing; errs.FieldsOf still
// hands it to the operator's log.
//
// It is total on a nil receiver, which its sibling CIEntitlementFor has always
// been. Reachable through the Roster alias in framework/entitlement, where a
// consumer holding a nil roster got a panic from one method and a refusal from
// the other.
func (r *RosterValue) SubjectFor(uuid string) (subject SubjectValue, err error) {
	//: A nil roster dereferences on the lookup below. CIEntitlementFor has
	//: refused this since it was written; this one panicked, and the two
	//: answers to the same input were never a decision anyone took. The
	//: device path is the MORE important of the two to keep total: a licence
	//: check that takes the process down has failed in the one way worse than
	//: refusing, and the refusal is what a caller already handles.
	if r == nil {
		//: Same sentinel the absent-subject case reports, for the same
		//: reason — a roster that does not vouch for this subject, whether
		//: because it omits them or because there is no roster at all, is a
		//: refusal and never a grant. ErrRosterUnreachable would have been
		//: the wrong answer: it means "cannot decide, retry", and a caller
		//: obeying it would retry a nil pointer forever.
		return SubjectValue{}, errs.Wrap(ErrRevoked, errs.WrapParams{},
			errs.String("stage", subjectLookupStage),
			errs.String("condition", "no roster to check against"),
			errs.String("subject", uuid))
	}
	sv, listed := r.Subjects[uuid]
	//: Absent from a roster that itself verified is not broken, but it is
	//: not unambiguously "revoked" either — see the doc comment above.
	if !listed || sv.Fingerprint == "" {
		//: A subject the roster lists with no fingerprint is a broken
		//: publisher rather than a withdrawal, and the two are one sentinel
		//: on purpose. Only the field says which happened.
		condition := "no entry for this uuid"
		//: Listed means the entry exists and its fingerprint is empty.
		if listed {
			//: Name the publisher defect rather than the withdrawal.
			condition = "the entry carries no fingerprint"
		}
		//: Report the sentinel so the caller can exit with the right code;
		//: the ambiguity is the caller's to explain, not this package's.
		return SubjectValue{}, errs.Wrap(ErrRevoked, errs.WrapParams{},
			errs.String("stage", subjectLookupStage),
			errs.String("condition", condition),
			errs.String("subject", uuid))
	}
	//: Return the recorded entry for comparison against the local key.
	return sv, nil
}
