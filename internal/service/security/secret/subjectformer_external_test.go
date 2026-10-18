package secret_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/security/secret"
)

// formerName is the root secret's name before it was renamed: every key it
// wrapped is bound to it.
const formerName string = "old-data-key"

// renamedFixture is a deployment whose root secret was renamed: the keys
// filed under formerName's versions, and a new root under rootName, in the
// same store.
type renamedFixture struct {
	*subjectFixture
	former *svcsecret.Keyring
	// boxes are the boxes sealed before the rename, by subject.
	boxes map[string][]byte
}

// newRenamedFixture seals one box for each of subjects under a root named
// formerName — rotated once, so the keys are wrapped under two versions —,
// then makes the new root, rootName, at version 1.
func newRenamedFixture(t *testing.T, subjects ...string) *renamedFixture {
	t.Helper()
	f := &renamedFixture{subjectFixture: newSubjectFixture(t), boxes: map[string][]byte{}}
	putRandomVersion(t, f.roots, formerName)
	former, err := svcsecret.NewKeyring(f.roots, formerName)
	if err != nil {
		t.Fatal(err)
	}
	f.former = former
	before, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: former, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	for i, subject := range subjects {
		if i == len(subjects)/2 {
			putRandomVersion(t, f.roots, formerName)
		}
		f.boxes[subject] = seal(t, before, subject, "value of "+subject)
	}
	return f
}

// renamed is one process of the renamed deployment: the new root, and the
// former one when former is set.
func (f *renamedFixture) renamed(t *testing.T, former bool) *svcsecret.SubjectKeys {
	t.Helper()
	cfg := svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store}
	if former {
		cfg.Former = f.former
	}
	keys, err := svcsecret.NewSubjectKeys(cfg)
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	return keys
}

// A root secret renamed is a new keyring name, and every wrapped key is bound
// to its root's name: under the new root alone, nothing the former root
// wrapped opens; with Former, everything does, every version of it, and a
// new subject's key is wrapped under the new root only.
func TestSubjectKeysFormerRootOpensWhatItWrapped(t *testing.T) {
	t.Parallel()
	f := newRenamedFixture(t, "user:1", "user:2", "user:3", "user:4")
	alone := f.renamed(t, false)
	for _, box := range f.boxes {
		openFails(t, alone, box, coresecret.CodeSubjectKeyUnreadable)
	}
	keys := f.renamed(t, true)
	for subject, box := range f.boxes {
		mustOpen(t, keys, box, "value of "+subject)
	}
	// An existing subject seals under the key it has; a new one is wrapped
	// under the new root, which the former alone does not open.
	mustOpen(t, keys, seal(t, keys, "user:1", "more about 1"), "more about 1")
	fresh := seal(t, keys, "user:5", "value of user:5")
	mustOpen(t, alone, fresh, "value of user:5")
	onlyFormer, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.former, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	openFails(t, onlyFormer, fresh, coresecret.CodeSubjectKeyUnreadable)
}

// Rewrap moves every key the former root wrapped to the new root's newest
// version, one compare-and-swap each, and touches no box: once it reports no
// error, everything opens under the new root alone, and the next pass moves
// nothing. A subject erased before the move stays erased: its key is not
// filed, so nothing brings it back.
func TestSubjectKeysRewrapMovesEveryKeyFromTheFormerRoot(t *testing.T) {
	t.Parallel()
	f := newRenamedFixture(t, "user:1", "user:2", "user:3", "user:4")
	erased := f.renamed(t, true)
	if destroyed, err := erased.Destroy(t.Context(), "user:4"); !destroyed || err != nil {
		t.Fatalf("Destroy = (%v, %v)", destroyed, err)
	}
	kept := map[string][]byte{}
	for subject, box := range f.boxes {
		kept[subject] = bytes.Clone(box)
	}
	keys := f.renamed(t, true)
	report, err := keys.Rewrap(t.Context())
	if want := (svcsecret.RewrapValue{Root: 1, Rewrapped: 3, FromFormer: 3}); err != nil || report != want {
		t.Fatalf("Rewrap = (%+v, %v), want %+v", report, err, want)
	}
	if got := wrappedVersions(t, f.store); !slices.Equal(got, []int{1, 1, 1}) {
		t.Fatalf("wrapped under %v, want every key under the new root's version 1", got)
	}
	alone := f.renamed(t, false)
	for subject, box := range f.boxes {
		if !bytes.Equal(box, kept[subject]) {
			t.Fatalf("the move touched a box of %s", subject)
		}
		if subject == "user:4" {
			openFails(t, alone, box, coresecret.CodeKeyDestroyed)
			openFails(t, keys, box, coresecret.CodeKeyDestroyed)
			continue
		}
		mustOpen(t, alone, box, "value of "+subject)
	}
	if again, againErr := keys.Rewrap(t.Context()); againErr != nil || again != (svcsecret.RewrapValue{Root: 1, Current: 3}) {
		t.Fatalf("second Rewrap = (%+v, %v), want three current keys", again, againErr)
	}
	if oldest, oldestErr := alone.OldestRoot(t.Context()); oldest != 1 || oldestErr != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (1, nil)", oldest, oldestErr)
	}
}

// A pass stopped half-way leaves each key under one root or the other — a
// key is replaced whole or not at all —, both of which open, and the next
// pass moves the rest.
func TestSubjectKeysAMoveStoppedHalfWayResumes(t *testing.T) {
	t.Parallel()
	f := newRenamedFixture(t, "user:1", "user:2", "user:3", "user:4", "user:5", "user:6")
	keys := f.renamed(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	stopping := &stopAfter{SubjectKeyStore: f.store, replaces: 2, cancel: cancel}
	stopped, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.root, Former: f.former, Store: stopping})
	if err != nil {
		t.Fatal(err)
	}
	report, err := stopped.Rewrap(ctx)
	if err == nil || report.FromFormer != 2 {
		t.Fatalf("the stopped pass = (%+v, %v), want two keys moved and the context's error", report, err)
	}
	for subject, box := range f.boxes {
		mustOpen(t, keys, box, "value of "+subject)
	}
	report, err = keys.Rewrap(t.Context())
	if want := (svcsecret.RewrapValue{Root: 1, Current: 2, Rewrapped: 4, FromFormer: 4}); err != nil || report != want {
		t.Fatalf("the resumed pass = (%+v, %v), want %+v", report, err, want)
	}
	alone := f.renamed(t, false)
	for subject, box := range f.boxes {
		mustOpen(t, alone, box, "value of "+subject)
	}
}

// A former root that keeps no version — renamed before it wrapped anything —
// moves nothing and stops nothing; a root given as its own former is refused.
func TestSubjectKeysFormerRootEdges(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	missing, err := svcsecret.NewKeyring(f.roots, formerName)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.root, Former: missing, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	box := seal(t, keys, "user:1", "one")
	if report, err := keys.Rewrap(t.Context()); err != nil || report != (svcsecret.RewrapValue{Root: 1, Current: 1}) {
		t.Fatalf("Rewrap with an empty former root = (%+v, %v)", report, err)
	}
	mustOpen(t, keys, box, "one")
	if _, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.root, Former: f.root, Store: f.store}); !errs.HasCode(err, coresecret.CodeInvalidConfig) {
		t.Fatalf("a root as its own former: %v", err)
	}
}

// stopAfter is a store that cancels its pass once it replaced replaces keys.
type stopAfter struct {
	coresecret.SubjectKeyStore
	replaces int
	cancel   context.CancelFunc
}

// Replace replaces, then cancels the pass once enough keys were replaced.
func (s *stopAfter) Replace(ctx context.Context, subject string, current, next []byte) (bool, error) {
	done, err := s.SubjectKeyStore.Replace(ctx, subject, current, next)
	if done {
		s.replaces--
		if s.replaces == 0 {
			s.cancel()
		}
	}
	return done, err
}
