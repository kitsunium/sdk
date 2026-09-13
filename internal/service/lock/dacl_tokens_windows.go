//go:build windows

// Package lock — the hypothetical accounts a DACL is evaluated against, and
// why one map keyed by SID is the wrong shape.
package lock

// tokenSet is the accounts the "could anybody interfere here?" question is
// asked on behalf of.
//
// # Why accounts, and why not a map keyed by the ACE's SID
//
// The obvious implementation accumulates denied rights under each ACE's
// literal identifier and subtracts them from a later allow carrying the SAME
// identifier. It is wrong, because the identifiers this rule cares about are
// not independent: EVERY authenticated account holds Everyone (S-1-1-0) AND
// Authenticated Users (S-1-5-11), and every local interactive one holds
// BUILTIN\Users (S-1-5-32-545) on top. So an ACL that denies Everyone
// FILE_DELETE_CHILD and then allows Authenticated Users FILE_DELETE_CHILD
// grants that right to nobody at all — and the SID-keyed version reports it as
// granted and refuses a directory that is perfectly safe.
//
// Modelling the accounts instead of the identifiers makes that fall out: a
// deny reaches every account holding the SID it names, and an allow is
// measured against what each of those accounts has already been denied.
//
// ADR 0084 §D3b said a third identifier would need a third account. ADR 0086
// adds the identifier, and this is that third account.
type tokenSet struct {
	// accounts is one entry per caller this rule models, in no particular
	// order — the verdict is a union, so nothing depends on their sequence.
	accounts []accountState
}

// accountState is one hypothetical caller: the identifiers it holds, and the
// rights the entries read so far have denied it.
type accountState struct {
	// holds is every identifier in this caller's token that [anyoneSids]
	// names.
	holds []string
	// denied accumulates across the walk, because a denial constrains every
	// later allow and never the other way round.
	denied uint32
}

// newTokens returns the modelled accounts with nothing denied to any of them.
//
// Three, for the three identifiers in [anyoneSids], nested the way Windows
// nests them by default: the anonymous caller holds only Everyone, an
// authenticated one adds Authenticated Users, and a local interactive one adds
// BUILTIN\Users. The middle account is not redundant even though Windows ships
// Authenticated Users as a MEMBER of BUILTIN\Users — that membership is a
// default an administrator can edit, and modelling it away would let a deny
// addressed to the group cancel an allow addressed to the identifier.
func newTokens() *tokenSet {
	//: a zero denial mask is the correct start: an ACL with no deny entries
	//: denies nothing.
	return &tokenSet{accounts: []accountState{
		{holds: []string{sidEveryone}},
		{holds: []string{sidEveryone, sidAuthenticatedUsers}},
		{holds: []string{sidEveryone, sidAuthenticatedUsers, sidBuiltinUsers}},
	}}
}

// apply folds one entry into the accounts it names, and reports the wanted
// rights an allow leaves standing for at least one of them.
//
// mask must already have its generic rights expanded — see [expandGeneric] —
// because a denial is only subtracted from an allow that speaks the same
// vocabulary. wanted is the subset of rights the caller's question is about.
//
// A CONDITIONAL entry carries an expression this file does not evaluate, and
// the two dispositions are resolved in the one direction that cannot weaken
// the verdict: a conditional allow is read as granting, and a conditional deny
// is read as denying nothing. The alternative — evaluating neither and
// skipping both — would make "add a condition" a way to put a grant where this
// rule cannot see it.
//
// A denial is recorded WITHOUT the context it applied in — one mask per
// account rather than one per question — so a deny that reached only the
// directory also subtracts from a later grant that reaches only the files.
// That can only subtract more than it should, which is the accepting
// direction this whole check fails in.
func (t *tokenSet) apply(sid string, shape aceShape, mask, wanted uint32) (granted uint32) {
	//: one fold per modelled account; the verdict is the union, because one
	//: account left holding the right is enough.
	for index := range t.accounts {
		account := &t.accounts[index]
		//: an identifier this account does not hold constrains it in neither
		//: direction.
		if !account.holdsSid(sid) {
			//: next account.
			continue
		}
		//: a denial is recorded against the account and grants nothing itself.
		if !shape.allow {
			account.deny(shape, mask)
			//: next account.
			continue
		}
		granted |= mask & wanted &^ account.denied
	}
	//: what at least one account is left holding.
	return granted
}

// deny records what one entry takes away from this account.
func (a *accountState) deny(shape aceShape, mask uint32) {
	//: a deny whose condition this file cannot evaluate may never fire, so it
	//: takes nothing away — the reading that cannot turn a refusal into an
	//: acceptance.
	if shape.conditional {
		//: nothing recorded.
		return
	}
	a.denied |= mask
}

// holdsSid reports whether this account's token carries the identifier.
func (a *accountState) holdsSid(sid string) bool {
	//: at most three entries, compared by byte equality on the canonical
	//: string form.
	for _, held := range a.holds {
		//: a match ends the search.
		if held == sid {
			//: the entry reaches this account.
			return true
		}
	}
	//: it does not.
	return false
}
