// Package entitlement - the signed roster: the only statement the binary trusts,
// and only because the vendor signed it. Origin is irrelevant here; a
// substituted endpoint can serve any bytes but cannot forge the signature.
package entitlement

import (
	"fmt"
	"time"
)

// RosterLifetime bounds how long a verified grant survives without a fresh
// fetch. It is the single dial of the whole scheme: it caps both how long a
// revoked subject keeps working and how long a hostile endpoint can replay a
// genuine roster.
const RosterLifetime time.Duration = 24 * time.Hour

// RosterValue is the signed statement of who may run the linter. It carries
// no secret: every field is publishable, which is why it can live in a public
// repository. Authority comes from the detached signature, never from the
// origin that served the bytes.
type RosterValue struct {
	// IssuedAt is when the vendor signed this roster.
	IssuedAt time.Time `json:"iat"`
	// ExpiresAt closes the roster's own signing-freshness window. Past it
	// the roster is refused even though its signature still verifies. This
	// bounds replay and revocation latency for the roster as a whole; it is
	// independent of any one subject's ExpiresAt below.
	ExpiresAt time.Time `json:"exp"`
	// Subjects maps a client UUID to what the roster records for it.
	Subjects map[string]SubjectValue `json:"subjects"`
	// RequiredVersion is the lowest binary version allowed to run, as a
	// SemVer tag ("v1.5.14"). Empty means no floor, which is how a roster
	// published before this field existed behaves — absence must not lock
	// out every client that predates it.
	//
	// It lives HERE, in the signed roster, rather than being read from a
	// release API, and that placement is the whole design. The roster is
	// already fetched on every cold start and already authenticated against
	// the compiled-in anchor, so a mandatory update inherits both properties
	// for free: it cannot be skipped by staying offline (the fetch is
	// mandatory), and it cannot be forged by redirecting the endpoint (the
	// signature is checked first). A version read from an unauthenticated
	// API would be defeated by one line in /etc/hosts.
	RequiredVersion string `json:"minv,omitempty"`
	// CIAccounts maps a GitHub account's NUMERIC id to what the roster grants
	// its CI runs. A run inside that account's Actions may be authorised
	// without spending one of the licence's device seats.
	//
	// Keyed by id and never by login: a login can be renamed, and a released
	// one can be claimed by somebody else, so matching on the name would turn
	// a freed handle into a way in.
	//
	// Absent for a roster that grants no CI entitlement, which is also how
	// every roster published before this field existed behaves — absence must
	// not be read as anything but "no CI seat".
	CIAccounts map[string]CIEntitlementValue `json:"ci,omitempty"`
	// CIRelaxed restores the historical fall-through: inside GitHub Actions a
	// failed CI seat stops being a refusal and the device path answers next.
	//
	// The DEFAULT is the strict direction, which is the inversion this field
	// records. A CI seat used to be tried first and every failure swallowed,
	// so a device key dropped onto a runner bought unlimited CI without any CI
	// entitlement — the one hole a licence scheme aimed at CI cannot leave
	// open. Requiring the seat is now what happens unless this says otherwise.
	//
	// It lives in the signed roster, and that placement is the whole design.
	// "A run in Actions need not prove it is one" is a statement only the
	// vendor is entitled to make, and it has to travel somewhere the party
	// being checked cannot rewrite — a flag or an environment variable would
	// be set by that very party. Note the direction: this field can only
	// RELAX, so forging it is the one thing an attacker would want and the one
	// thing the signature prevents; absence, which is what any tampering
	// produces, is the strict reading.
	//
	// The strictness applies only when InCI reports both runner variables
	// present. A laptop never had a seat to prove, and refusing one would be a
	// far worse defect than the hole this closes.
	CIRelaxed bool `json:"cilax,omitempty"`
}

// CIEntitlementValue is what the roster grants one account's CI.
type CIEntitlementValue struct {
	// ExpiresAt closes the entitlement, and is the licence's own term so CI
	// stops exactly when the devices do. The zero value means no term was
	// recorded, treated as no expiry rather than an instantly-closed one —
	// the same rule SubjectValue follows, for the same reason.
	ExpiresAt time.Time `json:"exp"`
}

// CIEntitlementFor returns what the roster grants an account's CI runs.
//
// Absence is not an error worth distinguishing from a refusal here: either
// way the caller falls back to the device path, which is what an
// unentitled CI run should do.
func (r *RosterValue) CIEntitlementFor(accountID string, now time.Time) (entitlement CIEntitlementValue, err error) {
	//: A nil roster would panic on the lookup below. Refusing instead is the
	//: only acceptable answer: this path must never do worse than fall back
	//: to the device check, and taking the process down over a CI seat is
	//: very much worse.
	if r == nil {
		//: Report the refusal.
		return CIEntitlementValue{}, fmt.Errorf("%w: no roster to check against", ErrCINotEntitled)
	}
	//: An empty id cannot match anything, and treating it as a lookup would
	//: let a token with no owner claim whatever an empty key happened to hold.
	if accountID == "" {
		//: Refuse rather than look up nothing.
		return CIEntitlementValue{}, fmt.Errorf("%w: no account id to match", ErrCINotEntitled)
	}
	recorded, listed := r.CIAccounts[accountID]
	//: An account the roster does not list gets no free seat. That covers a
	//: licence whose last device was revoked, one that never recorded an id,
	//: and an account with no licence at all.
	if !listed {
		//: Report the refusal.
		return CIEntitlementValue{}, fmt.Errorf("%w: account %s", ErrCINotEntitled, accountID)
	}
	//: A term that has closed stops CI as surely as it stops a device. Zero
	//: means none was recorded, not one that closed in 1970.
	if !recorded.ExpiresAt.IsZero() && now.After(recorded.ExpiresAt) {
		//: Report the expired entitlement.
		return CIEntitlementValue{}, fmt.Errorf("%w: account %s expired at %s",
			ErrCINotEntitled, accountID, recorded.ExpiresAt.UTC().Format(time.RFC3339))
	}
	//: Entitled, for as long as the licence is.
	return recorded, nil
}

// SubjectValue is one enrolled client's entry: the fingerprint that proves
// which key is theirs, and the date past which their own entitlement closes
// regardless of how fresh the roster itself is.
type SubjectValue struct {
	// Fingerprint is the SHA256 fingerprint of the subject's public key.
	// Recording the fingerprint rather than the key itself keeps the roster
	// small and makes the comparison exact.
	Fingerprint string `json:"fp"`
	// ExpiresAt closes this subject's individual term. The zero value means
	// no term was recorded, which is treated as no expiry rather than an
	// instantly-closed one — a subject published before this field existed
	// must not be locked out by its mere absence.
	ExpiresAt time.Time `json:"exp"`
}

// SubjectFor returns the roster's record for a subject. A missing entry
// reports ErrRevoked, but the roster is a stateless snapshot with no
// history: absence is exactly how a withdrawn licence looks, and it is
// indistinguishable here from a subject whose enrolment was never approved
// in the first place. Callers surfacing this to a human must name both
// possibilities rather than assert the more alarming one as fact.
func (r *RosterValue) SubjectFor(uuid string) (subject SubjectValue, err error) {
	sv, ok := r.Subjects[uuid]
	//: Absent from a roster that itself verified is not broken, but it is
	//: not unambiguously "revoked" either — see the doc comment above.
	if !ok || sv.Fingerprint == "" {
		//: Report the sentinel so the caller can exit with the right code;
		//: the ambiguity is the caller's to explain, not this package's.
		return SubjectValue{}, fmt.Errorf("%w: %s", ErrRevoked, uuid)
	}
	//: Return the recorded entry for comparison against the local key.
	return sv, nil
}
