//go:build windows

// Package lock — the two hypothetical accounts a DACL is evaluated against,
// and why one map keyed by SID is the wrong shape.
package lock

// tokenSet is the accounts the "could anybody create an entry here?" question
// is asked on behalf of.
//
// # Why two, and why not a map keyed by the ACE's SID
//
// The obvious implementation accumulates denied rights under each ACE's
// literal identifier and subtracts them from a later allow carrying the SAME
// identifier. It is wrong, because the two identifiers this rule cares about
// are not independent: EVERY authenticated account holds Everyone (S-1-1-0)
// AND Authenticated Users (S-1-5-11). So an ACL that denies Everyone
// FILE_ADD_FILE and then allows Authenticated Users FILE_ADD_FILE grants that
// right to nobody at all — and the SID-keyed version reports it as granted and
// refuses a directory that is perfectly safe.
//
// Modelling the accounts instead of the identifiers makes that fall out: a
// deny reaches every account holding the SID it names, and an allow is
// measured against what each of those accounts has already been denied.
//
// Two accounts are enough for the two identifiers in [anyoneSids]. A third
// identifier would need a third account, which is the honest cost of this
// shape and is why widening that list is a decision with its own evidence.
type tokenSet struct {
	// unauthenticated holds Everyone and nothing else — the anonymous caller.
	unauthenticated uint32
	// authenticated holds Everyone AND Authenticated Users, which is what any
	// account that has logged in holds.
	authenticated uint32
}

// newTokens returns the two accounts with nothing denied to either.
func newTokens() *tokenSet {
	//: a zero denial mask is the correct start: an ACL with no deny entries
	//: denies nothing.
	return &tokenSet{}
}

// apply folds one access-allowed or access-denied entry into the accounts it
// names, and reports the planting rights an allow leaves standing.
//
// mask must already have its generic rights expanded — see [expandGeneric] —
// because a denial is only subtracted from an allow that speaks the same
// vocabulary.
func (t *tokenSet) apply(sid string, aceType byte, mask uint32) (granted uint32) {
	reachesUnauthenticated := sid == sidEveryone
	//: a denial is recorded against every account holding the identifier it
	//: names, and grants nothing itself.
	if aceType == accessDeniedAceType {
		t.authenticated |= mask
		//: Authenticated Users does not reach the anonymous account.
		if reachesUnauthenticated {
			t.unauthenticated |= mask
		}
		//: nothing granted.
		return 0
	}
	granted = mask & plantRights &^ t.authenticated
	//: an allow addressed to Everyone also reaches the anonymous account,
	//: which may have been denied less than the authenticated one.
	if reachesUnauthenticated {
		granted |= mask & plantRights &^ t.unauthenticated
	}
	//: what at least one account is left holding.
	return granted
}
