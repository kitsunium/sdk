// Package secret — what a rotation of the root costs the subject keys: one
// re-wrap per subject, and a version kept while a key still needs it.
package secret

import (
	"context"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RewrapValue reports what one [SubjectKeys.Rewrap] pass did, key by key.
//
// pkg/v1/security/secret aliases it as RewrapReport.
type RewrapValue struct {
	// Root is the root version the pass wrapped under: the newest it read.
	Root int
	// Current counts the keys already wrapped under Root, each opened to
	// prove it, and the keys a later rotation already wrapped under a newer
	// version, which the next pass checks.
	Current int
	// Rewrapped counts the keys the pass moved to Root.
	Rewrapped int
	// Skipped counts the keys destroyed, or re-wrapped by another writer,
	// while the pass ran. They are left as they are: a re-wrap never brings
	// back a destroyed key.
	Skipped int
	// Unreadable counts the keys that did not unwrap as one data key —
	// wrapped under a version no longer kept, under another root secret or
	// for another purpose, altered, or not one key long — and the entries
	// whose subject is outside the grammar. They are left as they are, and
	// the pass returns [coresecret.SubjectKeyUnreadable].
	Unreadable int
}

// rewrapOutcome is what the pass did with one key.
type rewrapOutcome uint8

// The four outcomes, counted by RewrapValue.
const (
	// outcomeCurrent: already under the newest version.
	outcomeCurrent rewrapOutcome = iota
	// outcomeRewrapped: moved to the newest version.
	outcomeRewrapped
	// outcomeSkipped: destroyed or changed meanwhile.
	outcomeSkipped
	// outcomeUnreadable: did not unwrap.
	outcomeUnreadable
)

// Rewrap moves every data key to the root's newest version: each key wrapped
// under an older one is unwrapped, wrapped again, and filed with a
// compare-and-swap, so a key destroyed or re-wrapped meanwhile is left alone.
// No box is touched — this is why a root rotation costs one small write per
// subject and never a pass over the data.
//
// Run it after every rotation of the root (the rotator's OnRotate), and at
// start-up to finish a pass that was interrupted: a key already under the
// newest version costs one unwrap, which is also what makes Unreadable exact —
// every key the pass counts has been opened. It reads the root once, so a
// rotation during the pass leaves keys under the version it read, which the
// next pass moves, and a key filed meanwhile under a NEWER version is counted
// current and left for that pass.
//
// It returns the report with a nil error when every key is under the newest
// version, the report with [coresecret.SubjectKeyUnreadable] when some did not unwrap —
// they are counted, the pass goes on without them — and the report so far with
// the store's or the context's error when the pass could not finish.
func (s *SubjectKeys) Rewrap(ctx context.Context) (report RewrapValue, err error) {
	view, viewErr := s.root.view(ctx, wrapLabel)
	//: no root, or a root that cannot seal.
	if viewErr != nil {
		//: the root's verdict.
		return RewrapValue{}, viewErr
	}
	defer view.close()
	report.Root = view.newest
	//: every filed key, once.
	for entry, allErr := range s.store.All(ctx) {
		//: the store could not list its keys.
		if allErr != nil {
			//: what was done, and StoreUnavailable.
			return report, storeFailure(allErr, "all", "")
		}
		//: the caller gave up: what was done stays done.
		if ctxErr := ctx.Err(); ctxErr != nil {
			//: the context's own error.
			return report, ctxErr
		}
		outcome, stepErr := s.rewrapOne(ctx, view, entry)
		//: the store failed on a replace, or the root on a seal.
		if stepErr != nil {
			//: what was done, and the failure.
			return report, stepErr
		}
		report.count(outcome)
	}
	//: keys that stay under versions nothing may prune.
	if report.Unreadable > 0 {
		//: SubjectKeyUnreadable, with the count; the report says the rest.
		return report, errs.Wrap(coresecret.SubjectKeyUnreadable, errs.WrapParams{},
			errs.Int("unreadable", report.Unreadable), errs.Int("root", report.Root))
	}
	//: every key is under the newest version.
	return report, nil
}

// rewrapOne moves one key to the view's newest version.
func (s *SubjectKeys) rewrapOne(ctx context.Context, view *rootView, entry coresecret.SubjectKeyValue) (outcome rewrapOutcome, err error) {
	version, _, parsed := parseHeader(entry.Wrapped)
	//: filed by a rotation newer than the one this pass read: not ours to move.
	if parsed && version > view.newest && coresecret.ValidateSubject(entry.Subject) == nil {
		//: current; the next pass opens it.
		return outcomeCurrent, nil
	}
	dek, opened := unwrapIn(view, entry)
	//: a subject nobody can name, a version gone, altered, or not one key.
	if !opened {
		//: counted; the key is left as it is.
		return outcomeUnreadable, nil
	}
	defer clear(dek)
	//: already where the pass is moving everything, and proven so.
	if version == view.newest {
		//: nothing to write.
		return outcomeCurrent, nil
	}
	next, sealErr := view.seal(dek, wrapAAD(entry.Subject))
	//: sealing fails only on an unregistered scheme, which the imports rule out.
	if sealErr != nil {
		//: the crypto verdict, unchanged.
		return outcomeUnreadable, sealErr
	}
	replaced, replaceErr := s.store.Replace(ctx, entry.Subject, entry.Wrapped, next)
	//: the store failed.
	if replaceErr != nil {
		//: StoreUnavailable.
		return outcomeUnreadable, storeFailure(replaceErr, "replace", entry.Subject)
	}
	//: destroyed, or re-wrapped by another writer, since All yielded it.
	if !replaced {
		//: left alone.
		return outcomeSkipped, nil
	}
	//: moved.
	return outcomeRewrapped, nil
}

// count adds one outcome to the report.
func (r *RewrapValue) count(outcome rewrapOutcome) {
	//: one counter per outcome.
	switch outcome {
	//: nothing to do.
	case outcomeCurrent:
		r.Current++
	//: moved.
	case outcomeRewrapped:
		r.Rewrapped++
	//: left alone.
	case outcomeSkipped:
		r.Skipped++
	//: did not unwrap.
	case outcomeUnreadable:
		r.Unreadable++
	}
}

// OldestRoot returns the oldest root version a data key is still wrapped
// under, or 0 when the store holds no key. It is the question a rotation of
// the root must ask before it prunes, and its signature is the one
// RotatorConfig.InUse takes:
//
//	rotator, err := NewRotator(RotatorConfig{..., InUse: keys.OldestRoot})
//
// It reads the root once and OPENS every key under the version it names —
// about a microsecond each — and counts only the keys that open as one data
// key. A key that does not — its version already pruned, altered, not a
// keyring box — is lost whatever is kept, and counting it would stop every
// later prune for nothing: the root's versions would pile up, and a backed-up
// wrapped key would keep opening under them long after its subject's
// erasure. Such keys are Rewrap's to report. A cancelled scan stops at the
// next key, and any failure is returned: the rotator then prunes nothing.
func (s *SubjectKeys) OldestRoot(ctx context.Context) (version int, err error) {
	//: a scan asked to stop before it starts reads nothing.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the context's own error.
		return 0, ctxErr
	}
	view, viewErr := s.root.view(ctx, wrapLabel)
	//: no root, or a root that cannot seal: unknown is never "none".
	if viewErr != nil {
		//: the root's verdict.
		return 0, viewErr
	}
	defer view.close()
	oldest := 0
	//: every filed key, once.
	for entry, allErr := range s.store.All(ctx) {
		//: the store could not list its keys: the rotation must not prune.
		if allErr != nil {
			//: StoreUnavailable.
			return 0, storeFailure(allErr, "all", "")
		}
		//: a cancelled scan answers nothing a rotation may rely on.
		if ctxErr := ctx.Err(); ctxErr != nil {
			//: the context's own error.
			return 0, ctxErr
		}
		wrappedUnder, opens := openedUnder(view, entry)
		//: nothing keeps a key that does not open: it pins no version.
		if !opens {
			continue
		}
		//: the lowest version a live key needs, so far.
		if oldest == 0 || wrappedUnder < oldest {
			oldest = wrappedUnder
		}
	}
	//: the oldest version in use, or 0.
	return oldest, nil
}

// openedUnder reports the root version entry's key is wrapped under, and
// whether it opens there as one data key. The key is cleared at once: the
// question is where it lives, not what it is.
func openedUnder(view *rootView, entry coresecret.SubjectKeyValue) (version int, opens bool) {
	version, _, _ = parseHeader(entry.Wrapped)
	dek, opened := unwrapIn(view, entry)
	clear(dek)
	//: the version, when the key is alive under it.
	return version, opened
}

// unwrapIn opens entry's wrapped key under the view and returns the data key,
// which the caller clears. It reports false for everything the load path
// would refuse as unreadable: a subject outside the grammar, a version the
// view does not hold, a wrapper altered or sealed for another purpose, and a
// plaintext that is not one key long.
func unwrapIn(view *rootView, entry coresecret.SubjectKeyValue) (dek []byte, opened bool) {
	//: an entry this engine could never have filed.
	if coresecret.ValidateSubject(entry.Subject) != nil {
		//: not a key.
		return nil, false
	}
	dek, openErr := view.open(entry.Wrapped, wrapAAD(entry.Subject))
	//: a version gone or unusable, another root, another purpose, or altered.
	if openErr != nil {
		//: not a key.
		return nil, false
	}
	//: authenticated, but not what this engine wraps.
	if len(dek) != corecrypto.KeyLen {
		clear(dek)
		//: not a key.
		return nil, false
	}
	//: the data key.
	return dek, true
}
