package secret

import (
	"slices"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// checkPut refuses a Put every store must refuse, before any backend work: a
// malformed name, and an empty secret — empty reports the value's IsZero, so
// the check never holds the secret itself.
func checkPut(name string, empty bool) error {
	//: the name first, so a bad name is reported as a bad name.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName, naming the clause.
		return nameErr
	}
	//: an empty secret is an unfilled field, never a version.
	if empty {
		//: EmptyValue, naming the secret — its name is not secret.
		return errs.Wrap(coresecret.EmptyValue, errs.WrapParams{}, errs.String("secret", name))
	}
	//: a Put every store can honour.
	return nil
}

// checkPrune refuses a Prune every store must refuse: a malformed name, and a
// keep that would delete the secret.
func checkPrune(name string, keep int) error {
	//: the name first.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return nameErr
	}
	//: keeping nothing is deleting, which pruning never does.
	if keep < 1 {
		//: InvalidKeep, with the number the caller passed.
		return errs.Wrap(coresecret.InvalidKeep, errs.WrapParams{},
			errs.String("secret", name), errs.Int("keep", keep))
	}
	//: a Prune every store can honour.
	return nil
}

// notFound is the NotFound verdict for one secret.
func notFound(name string) error {
	//: the name is not secret and is what an operator needs to act.
	return errs.Wrap(coresecret.NotFound, errs.WrapParams{}, errs.String("secret", name))
}

// nextVersion is the number a new version of a secret takes: one past the
// highest ever kept, or 1 for a secret with no version.
//
// It reads the HIGHEST number rather than counting the versions, because a
// prune removes versions without renumbering the rest — counting would hand a
// pruned number back out, and a reused number would open an old box with a new
// key.
func nextVersion(versions []coresecret.VersionValue) int {
	//: the empty history starts at one.
	if len(versions) == 0 {
		//: the first version.
		return 1
	}
	//: the newest is at the head; every store keeps that order.
	return versions[0].Version + 1
}

// prunedBelow returns the versions of a newest-first history numbered at or
// above floor, and the newest whatever floor says — pruning never deletes a
// secret. The bool reports whether anything was dropped, so a store with
// nothing to drop writes nothing.
func prunedBelow(versions []coresecret.VersionValue, floor int) (kept []coresecret.VersionValue, dropped bool) {
	//: the head is the newest and is always kept.
	keep := 1
	//: every further version at or above the floor stays; the history is
	//: newest-first, so the first one below it ends the kept prefix.
	for keep < len(versions) && versions[keep].Version >= floor {
		keep++
	}
	//: a copy all the same, so the caller owns what it gets.
	return slices.Clone(versions[:keep]), keep < len(versions)
}

// checkPruneBelow refuses a PruneBelow every store must refuse: a malformed
// name. Any floor is meaningful — one at or below 1 drops nothing.
func checkPruneBelow(name string) error {
	//: InvalidName, or nothing.
	return coresecret.ValidateName(name)
}

// pruned returns the newest keep versions of a newest-first history.
func pruned(versions []coresecret.VersionValue, keep int) []coresecret.VersionValue {
	//: a history already within the bound is kept whole.
	if len(versions) <= keep {
		//: a copy all the same, so the caller owns what it gets.
		return slices.Clone(versions)
	}
	//: the head is the newest, so the first keep entries are the ones to keep.
	return slices.Clone(versions[:keep])
}
