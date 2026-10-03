package secret_test

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"slices"
	"testing"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/security/secret"
)

// rootVersions returns the version numbers the root store keeps, newest first.
func (f *subjectFixture) rootVersions(t *testing.T) []int {
	t.Helper()
	versions, err := f.roots.Versions(t.Context(), rootName)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	return numbers(versions)
}

// wrappedVersions returns, per filed key, the root version its header names.
func wrappedVersions(t *testing.T, store coresecret.SubjectKeyStore) []int {
	t.Helper()
	var out []int
	for _, entry := range filed(t, store) {
		out = append(out, int(entry.Wrapped[4]))
	}
	return out
}

// TestSubjectKeysRewrapMovesOneSmallKeyPerSubject is the rotation ADR 0142
// promises: after the root rotates, a re-wrap moves each subject's key to the
// newest version, never touches a box, and the old version can then be
// pruned without a single box going dark.
func TestSubjectKeysRewrapMovesOneSmallKeyPerSubject(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 16)
	boxes := map[string][]byte{}
	for _, subject := range []string{"user:1", "user:2", "user:3"} {
		boxes[subject] = seal(t, keys, subject, "value of "+subject)
	}
	kept := map[string][]byte{}
	for subject, box := range boxes {
		kept[subject] = bytes.Clone(box)
	}
	putRandomVersion(t, f.roots, rootName)
	report, err := keys.Rewrap(t.Context())
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if want := (svcsecret.RewrapValue{Root: 2, Rewrapped: 3}); report != want {
		t.Fatalf("Rewrap = %+v, want %+v", report, want)
	}
	if got := wrappedVersions(t, f.store); !slices.Equal(got, []int{2, 2, 2}) {
		t.Fatalf("wrapped under %v, want every key under version 2", got)
	}
	if oldest, oldestErr := keys.OldestRoot(t.Context()); oldest != 2 || oldestErr != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (2, nil)", oldest, oldestErr)
	}
	//: version 1 retired: every box still opens, in this process and a new one.
	if pruneErr := f.roots.Prune(t.Context(), rootName, 1); pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	fresh := f.process(t, 0)
	for subject, box := range boxes {
		if !bytes.Equal(box, kept[subject]) {
			t.Fatalf("the re-wrap touched a box of %s", subject)
		}
		mustOpen(t, fresh, box, "value of "+subject)
	}
	//: a second pass has nothing to do but read three headers.
	if again, againErr := keys.Rewrap(t.Context()); againErr != nil || again != (svcsecret.RewrapValue{Root: 2, Current: 3}) {
		t.Fatalf("second Rewrap = (%+v, %v), want three current keys", again, againErr)
	}
}

// TestSubjectKeysAPrunedVersionIsAFaultNotAnErasure pins what happens when a
// root version is pruned while a key is still wrapped under it — the loss
// RotatorConfig.InUse exists to prevent: every verdict says SubjectKeyUnreadable,
// none says KeyDestroyed, and only an explicit Destroy clears the way.
func TestSubjectKeysAPrunedVersionIsAFaultNotAnErasure(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	box := seal(t, keys, "user:1", "value")
	putRandomVersion(t, f.roots, rootName)
	if pruneErr := f.roots.Prune(t.Context(), rootName, 1); pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	openFails(t, keys, box, coresecret.CodeSubjectKeyUnreadable)
	if _, sealErr := keys.Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Seal = %v, want SubjectKeyUnreadable", sealErr)
	}
	report, err := keys.Rewrap(t.Context())
	if !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) || report.Unreadable != 1 {
		t.Fatalf("Rewrap = (%+v, %v), want one unreadable key and SubjectKeyUnreadable", report, err)
	}
	if destroyed, destroyErr := keys.Destroy(t.Context(), "user:1"); !destroyed || destroyErr != nil {
		t.Fatalf("Destroy = (%v, %v), want (true, nil)", destroyed, destroyErr)
	}
	seal(t, keys, "user:1", "a new key under version 2")
}

// TestRotatorInUseNeverPrunesAVersionAKeyNeeds pins the guard: with keys
// still wrapped under version 1, rotations keep version 1 however far past
// Keep they go; once the keys are re-wrapped, the next rotation prunes back
// to Keep.
func TestRotatorInUseNeverPrunesAVersionAKeyNeeds(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	box := seal(t, keys, "user:1", "value")
	rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store:  f.roots,
		Name:   rootName,
		Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 2, Generate: svcsecret.Random(32)},
		Clock:  f.clk,
		InUse:  keys.OldestRoot,
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	for range 3 {
		if _, rotateErr := rotator.Rotate(t.Context()); rotateErr != nil {
			t.Fatalf("Rotate: %v", rotateErr)
		}
	}
	if got := f.rootVersions(t); !slices.Equal(got, []int{4, 3, 2, 1}) {
		t.Fatalf("root keeps %v, want 4..1: version 1 still wraps a key", got)
	}
	mustOpen(t, f.process(t, 0), box, "value")
	if _, rewrapErr := keys.Rewrap(t.Context()); rewrapErr != nil {
		t.Fatalf("Rewrap: %v", rewrapErr)
	}
	if _, rotateErr := rotator.Rotate(t.Context()); rotateErr != nil {
		t.Fatalf("Rotate: %v", rotateErr)
	}
	if got := f.rootVersions(t); !slices.Equal(got, []int{5, 4}) {
		t.Fatalf("root keeps %v after the re-wrap, want Keep=2: [5 4]", got)
	}
	mustOpen(t, f.process(t, 0), box, "value")
}

// errNoAnswer is what an InUse that cannot answer returns.
var errNoAnswer = errors.New("cannot tell which versions are in use")

// TestRotatorInUseFailurePrunesNothing pins that "unknown" is never read as
// "none": the rotation happens, its error is the InUse one, and every version
// stays.
func TestRotatorInUseFailurePrunesNothing(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store:  f.roots,
		Name:   rootName,
		Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 2, Generate: svcsecret.Random(32)},
		Clock:  f.clk,
		InUse:  func(context.Context) (int, error) { return 0, errNoAnswer },
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	for range 3 {
		created, rotateErr := rotator.Rotate(t.Context())
		if created.Version == 0 || !errors.Is(rotateErr, errNoAnswer) {
			t.Fatalf("Rotate = (%d, %v), want a new version and the InUse error", created.Version, rotateErr)
		}
	}
	if got := f.rootVersions(t); !slices.Equal(got, []int{4, 3, 2, 1}) {
		t.Fatalf("root keeps %v, want every version: nothing may be pruned on an unknown", got)
	}
}

// destroyingStore wraps a store and destroys each key between the moment All
// yields it and the moment the caller acts on it — an erasure landing in the
// middle of a re-wrap.
type destroyingStore struct {
	coresecret.SubjectKeyStore
}

// All yields each key, then deletes it before the caller's next step.
func (s destroyingStore) All(ctx context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
	return func(yield func(coresecret.SubjectKeyValue, error) bool) {
		for entry, err := range s.SubjectKeyStore.All(ctx) {
			if err == nil {
				if _, deleteErr := s.Delete(ctx, entry.Subject); deleteErr != nil {
					entry, err = coresecret.SubjectKeyValue{}, deleteErr
				}
			}
			if !yield(entry, err) || err != nil {
				return
			}
		}
	}
}

// TestSubjectKeysRewrapNeverResurrectsADestroyedKey pins the compare-and-swap:
// a key destroyed while a re-wrap holds its old bytes is skipped, and stays
// destroyed.
func TestSubjectKeysRewrapNeverResurrectsADestroyedKey(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	box := seal(t, f.process(t, 0), "user:1", "value")
	putRandomVersion(t, f.roots, rootName)
	racing, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.root, Store: destroyingStore{f.store}})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	report, err := racing.Rewrap(t.Context())
	if err != nil || report != (svcsecret.RewrapValue{Root: 2, Skipped: 1}) {
		t.Fatalf("Rewrap = (%+v, %v), want one skipped key", report, err)
	}
	if entries := filed(t, f.store); len(entries) != 0 {
		t.Fatalf("the re-wrap brought back %d destroyed key(s)", len(entries))
	}
	openFails(t, f.process(t, 0), box, coresecret.CodeKeyDestroyed)
}

// TestSubjectKeysABackupOpensOnlyWhileItsRootVersionIsKept pins the bound on
// what an erasure reaches, both sides of it: a wrapped key restored from a
// backup of the key store opens again while the root version that wrapped it
// is kept — which is why the doc says so — and never once that version is
// pruned.
func TestSubjectKeysABackupOpensOnlyWhileItsRootVersionIsKept(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	box := seal(t, keys, "user:1", "value")
	seal(t, keys, "user:2", "somebody else")
	backup := filed(t, f.store)[0]
	restore := func() {
		t.Helper()
		if inserted, err := f.store.Insert(t.Context(), backup.Subject, backup.Wrapped); !inserted || err != nil {
			t.Fatalf("restore = (%v, %v)", inserted, err)
		}
	}
	if _, err := keys.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	restore()
	mustOpen(t, keys, box, "value")
	if _, err := keys.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	//: the root rotates, every live key moves on, version 1 is retired.
	putRandomVersion(t, f.roots, rootName)
	if _, err := keys.Rewrap(t.Context()); err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if oldest, err := keys.OldestRoot(t.Context()); oldest != 2 || err != nil {
		t.Fatalf("OldestRoot = (%d, %v), want 2: nothing live needs version 1", oldest, err)
	}
	if err := f.roots.Prune(t.Context(), rootName, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	restore()
	openFails(t, keys, box, coresecret.CodeSubjectKeyUnreadable)
}

// TestSubjectKeysOldestRoot pins the question a rotation asks: 0 on an empty
// store, the lowest version in use otherwise, and an entry that is no keyring
// box skipped rather than pinning a version it could not use.
func TestSubjectKeysOldestRoot(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	if oldest, err := keys.OldestRoot(t.Context()); oldest != 0 || err != nil {
		t.Fatalf("OldestRoot of an empty store = (%d, %v), want (0, nil)", oldest, err)
	}
	seal(t, keys, "user:1", "under version 1")
	putRandomVersion(t, f.roots, rootName)
	seal(t, keys, "user:2", "under version 2")
	if inserted, err := f.store.Insert(t.Context(), "garbage", []byte("not a keyring box")); !inserted || err != nil {
		t.Fatalf("Insert: (%v, %v)", inserted, err)
	}
	if oldest, err := keys.OldestRoot(t.Context()); oldest != 1 || err != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (1, nil)", oldest, err)
	}
	report, err := keys.Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 2, Current: 1, Rewrapped: 1, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap = (%+v, %v), want the garbage counted unreadable", report, err)
	}
	if oldest, err := keys.OldestRoot(t.Context()); oldest != 2 || err != nil {
		t.Fatalf("OldestRoot after the re-wrap = (%d, %v), want (2, nil)", oldest, err)
	}
}

// TestSubjectKeysRewrapStopsWithItsContext pins that a cancelled pass stops,
// reports what it did, and leaves the rest for the next pass.
func TestSubjectKeysRewrapStopsWithItsContext(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	seal(t, keys, "user:1", "value")
	putRandomVersion(t, f.roots, rootName)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := keys.Rewrap(ctx)
	if !errors.Is(err, context.Canceled) || report.Rewrapped != 0 {
		t.Fatalf("Rewrap on a cancelled context = (%+v, %v), want nothing done and context.Canceled", report, err)
	}
	if _, oldestErr := keys.OldestRoot(ctx); !errors.Is(oldestErr, context.Canceled) {
		t.Fatalf("OldestRoot on a cancelled context = %v, want context.Canceled", oldestErr)
	}
	if again, againErr := keys.Rewrap(t.Context()); againErr != nil || again.Rewrapped != 1 {
		t.Fatalf("the next pass = (%+v, %v), want the key moved", again, againErr)
	}
}

// TestSubjectKeysRewrapWithoutARoot pins that a pass over a root with no
// version, or whose newest version is not a key, is refused before any key is
// read.
func TestSubjectKeysRewrapWithoutARoot(t *testing.T) {
	t.Parallel()
	roots := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	root, err := svcsecret.NewKeyring(roots, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: root, Store: svcsecret.NewMemorySubjectKeyStore()})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	if _, rewrapErr := keys.Rewrap(t.Context()); !errs.HasCode(rewrapErr, coresecret.CodeNotFound) {
		t.Fatalf("Rewrap without a root = %v, want NotFound", rewrapErr)
	}
	if _, putErr := roots.Put(t.Context(), rootName, coresecret.FromString("a password, not a key")); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	if _, rewrapErr := keys.Rewrap(t.Context()); !errs.HasCode(rewrapErr, coresecret.CodeKeyMaterialInvalid) {
		t.Fatalf("Rewrap under a password = %v, want KeyMaterialInvalid", rewrapErr)
	}
}

// TestSubjectKeysRewrapSkipsAnUnusableOldVersion pins that an OLD root
// version which is not key material costs only the keys wrapped under it —
// counted unreadable — and not the pass.
func TestSubjectKeysRewrapSkipsAnUnusableOldVersion(t *testing.T) {
	t.Parallel()
	roots := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	if _, err := roots.Put(t.Context(), rootName, coresecret.FromString("an old password")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	store := svcsecret.NewMemorySubjectKeyStore()
	if inserted, err := store.Insert(t.Context(), "user:0", []byte{0x01, 0, 0, 0, 1, 0xaa}); !inserted || err != nil {
		t.Fatalf("Insert: (%v, %v)", inserted, err)
	}
	putRandomVersion(t, roots, rootName)
	root, err := svcsecret.NewKeyring(roots, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: root, Store: store, CacheSize: 4, CacheTTL: time.Minute})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	box := seal(t, keys, "user:1", "value")
	report, err := keys.Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 2, Current: 1, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap = (%+v, %v), want the key under the password counted unreadable", report, err)
	}
	mustOpen(t, keys, box, "value")
}
