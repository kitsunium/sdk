// Package secret — what a rotation of the root costs the subject keys: one
// re-wrap per subject, and a version kept while a key still needs it.
package secret

import (
	"context"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// RewrapValue reports what one [SubjectKeys.Rewrap] pass did, key by key.
//
// pkg/v1/secret aliases it as RewrapReport.
type RewrapValue struct {
	// Root is the root version the pass wrapped under: the newest it read.
	Root int
	// Current counts the keys already wrapped under Root.
	Current int
	// Rewrapped counts the keys the pass moved to Root.
	Rewrapped int
	// Skipped counts the keys destroyed, or re-wrapped by another writer,
	// while the pass ran. They are left as they are: a re-wrap never brings
	// back a destroyed key.
	Skipped int
	// Unreadable counts the keys that did not unwrap — wrapped under a
	// version no longer kept, under another root secret, or altered — and the
	// entries whose subject is outside the grammar. They are left as they
	// are, and the pass returns [SubjectKeyUnreadable].
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
// newest version costs a header read. It reads the root once, so a rotation
// during the pass leaves keys under the version it read, which the next pass
// moves.
//
// It returns the report with a nil error when every key is under the newest
// version, the report with [SubjectKeyUnreadable] when some did not unwrap —
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
		return report, errs.Wrap(SubjectKeyUnreadable, errs.WrapParams{},
			errs.Int("unreadable", report.Unreadable), errs.Int("root", report.Root))
	}
	//: every key is under the newest version.
	return report, nil
}

// rewrapOne moves one key to the view's newest version.
func (s *SubjectKeys) rewrapOne(ctx context.Context, view *rootView, entry coresecret.SubjectKeyValue) (outcome rewrapOutcome, err error) {
	//: an entry this engine could never have filed.
	if coresecret.ValidateSubject(entry.Subject) != nil {
		//: counted; nothing is written under a subject nobody can name.
		return outcomeUnreadable, nil
	}
	version, _, parsed := parseHeader(entry.Wrapped)
	//: already where the pass is moving everything.
	if parsed && version == view.newest {
		//: a header read, and nothing else.
		return outcomeCurrent, nil
	}
	aad := wrapAAD(entry.Subject)
	dek, openErr := view.open(entry.Wrapped, aad)
	//: under a version no longer kept, another root, or altered.
	if openErr != nil {
		//: counted; the key is left as it is.
		return outcomeUnreadable, nil
	}
	defer clear(dek)
	next, sealErr := view.seal(dek, aad)
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
// It reads every key's header and opens none. An entry that is not a
// keyring box is skipped: it opens under no version, so keeping one for it
// saves nothing. Any failure is returned, and the rotator then prunes nothing.
func (s *SubjectKeys) OldestRoot(ctx context.Context) (version int, err error) {
	oldest := 0
	//: every filed key, once.
	for entry, allErr := range s.store.All(ctx) {
		//: the store could not list its keys: the rotation must not prune.
		if allErr != nil {
			//: StoreUnavailable.
			return 0, storeFailure(allErr, "all", "")
		}
		wrapped, _, parsed := parseHeader(entry.Wrapped)
		//: not a keyring box: no version could open it.
		if !parsed {
			continue
		}
		//: the lowest version seen so far.
		if oldest == 0 || wrapped < oldest {
			oldest = wrapped
		}
	}
	//: a cancelled scan answers nothing a rotation may rely on.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the context's own error.
		return 0, ctxErr
	}
	//: the oldest version in use, or 0.
	return oldest, nil
}
