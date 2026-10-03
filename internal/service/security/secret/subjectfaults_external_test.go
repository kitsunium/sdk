package secret_test

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/security/secret"
)

// wrapDomain opens the associated data a data key is wrapped with, spelled
// out here so the suite pins the layout ADR 0142 records: the domain string,
// NUL, then the subject.
const wrapDomain string = "kitsunium/secret subject key v1\x00"

// scriptedKeyStore wraps a store and answers, for each method a test scripts,
// with the script instead.
type scriptedKeyStore struct {
	coresecret.SubjectKeyStore
	get     func(ctx context.Context, subject string) ([]byte, bool, error)
	insert  func(ctx context.Context, subject string, wrapped []byte) (bool, error)
	replace func(ctx context.Context, subject string, current, next []byte) (bool, error)
	all     func(ctx context.Context) iter.Seq2[coresecret.SubjectKeyValue, error]
}

// Get answers with the script, or the wrapped store.
func (s scriptedKeyStore) Get(ctx context.Context, subject string) ([]byte, bool, error) {
	if s.get != nil {
		return s.get(ctx, subject)
	}
	return s.SubjectKeyStore.Get(ctx, subject)
}

// Insert answers with the script, or the wrapped store.
func (s scriptedKeyStore) Insert(ctx context.Context, subject string, wrapped []byte) (bool, error) {
	if s.insert != nil {
		return s.insert(ctx, subject, wrapped)
	}
	return s.SubjectKeyStore.Insert(ctx, subject, wrapped)
}

// Replace answers with the script, or the wrapped store.
func (s scriptedKeyStore) Replace(ctx context.Context, subject string, current, next []byte) (bool, error) {
	if s.replace != nil {
		return s.replace(ctx, subject, current, next)
	}
	return s.SubjectKeyStore.Replace(ctx, subject, current, next)
}

// All answers with the script, or the wrapped store.
func (s scriptedKeyStore) All(ctx context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
	if s.all != nil {
		return s.all(ctx)
	}
	return s.SubjectKeyStore.All(ctx)
}

// engineOver builds an engine over the fixture's root and store, with no cache.
func engineOver(t *testing.T, root *svcsecret.Keyring, store coresecret.SubjectKeyStore) *svcsecret.SubjectKeys {
	t.Helper()
	keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: root, Store: store})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	return keys
}

// TestSubjectKeysARootOutageIsRetryableNotUnreadable pins the distinction the
// keyring already makes, one level up: a root store that could not be READ is
// StoreUnavailable — retry — and never SubjectKeyUnreadable, which would send
// an operator looking for a lost key during an outage.
func TestSubjectKeysARootOutageIsRetryableNotUnreadable(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	box := seal(t, f.process(t, 0), "user:1", "value")
	downRoot, err := svcsecret.NewKeyring(outageStore{}, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	keys := engineOver(t, downRoot, f.store)
	_, openErr := keys.Open(t.Context(), box)
	if !errs.HasCode(openErr, coresecret.CodeStoreUnavailable) || errs.HasCode(openErr, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Open during a root outage = %v, want StoreUnavailable only", openErr)
	}
}

// TestSubjectKeysABoxTheRootSealedIsNeverAKey pins the purpose separation:
// a value the root keyring sealed with Seal — even one key long, even under
// the very associated data a wrap uses — does not unwrap as a data key,
// because a data key is wrapped under a key of its own purpose. A caller that
// seals other values directly under its root cannot be made to plant a key.
func TestSubjectKeysABoxTheRootSealedIsNeverAKey(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	planted, err := f.root.Seal(t.Context(), bytes.Repeat([]byte{0x42}, 32), []byte(wrapDomain+"user:1"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if inserted, insertErr := f.store.Insert(t.Context(), "user:1", planted); !inserted || insertErr != nil {
		t.Fatalf("Insert = (%v, %v)", inserted, insertErr)
	}
	if _, sealErr := f.process(t, 0).Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Seal over a planted key = %v, want SubjectKeyUnreadable", sealErr)
	}
}

// TestSubjectKeysAStoreThatNeverKeepsAKeyEndsTheRetry pins the bound on the
// create race: a store that answers "taken" to every Insert and "absent" to
// every Get is reported as failing after a few turns, never looped on.
func TestSubjectKeysAStoreThatNeverKeepsAKeyEndsTheRetry(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	inserts := 0
	store := scriptedKeyStore{
		SubjectKeyStore: f.store,
		insert: func(context.Context, string, []byte) (bool, error) {
			inserts++
			return false, nil
		},
	}
	_, err := engineOver(t, f.root, store).Seal(t.Context(), "user:1", []byte("x"))
	if !errs.HasCode(err, coresecret.CodeStoreUnavailable) || inserts != 3 {
		t.Fatalf("Seal = %v after %d inserts, want StoreUnavailable after 3", err, inserts)
	}
}

// TestSubjectKeysAFailedInsertFilesNothing pins that a store failing the
// Insert of a first key reports StoreUnavailable, with its own error kept.
func TestSubjectKeysAFailedInsertFilesNothing(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	store := scriptedKeyStore{
		SubjectKeyStore: f.store,
		insert:          func(context.Context, string, []byte) (bool, error) { return false, errBackendDown },
	}
	_, err := engineOver(t, f.root, store).Seal(t.Context(), "user:1", []byte("x"))
	if !errs.HasCode(err, coresecret.CodeStoreUnavailable) || !errors.Is(err, errBackendDown) {
		t.Fatalf("Seal = %v, want StoreUnavailable carrying the store's error", err)
	}
	if entries := filed(t, f.store); len(entries) != 0 {
		t.Fatalf("store holds %d keys after a failed insert", len(entries))
	}
}

// TestSubjectKeysAKeyGoneDuringARereadIsDestroyed pins the second read Open
// makes for a cached key another process replaced: when the store holds no
// key by then either, the box is erased.
func TestSubjectKeysAKeyGoneDuringARereadIsDestroyed(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	reader, writer := f.process(t, 16), f.process(t, 0)
	mustOpen(t, reader, seal(t, reader, "user:1", "cached"), "cached")
	if _, err := writer.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	fresh := seal(t, writer, "user:1", "fresh")
	if _, err := writer.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	openFails(t, reader, fresh, coresecret.CodeKeyDestroyed)
}

// TestSubjectKeysRewrapReportsWhatItDidBeforeAFailure pins that a pass the
// store interrupts returns the report so far with StoreUnavailable, and that
// a store yielding a subject nobody could have filed costs that entry only.
func TestSubjectKeysRewrapReportsWhatItDidBeforeAFailure(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	seal(t, keys, "user:1", "value")
	seal(t, keys, "user:2", "value")
	putRandomVersion(t, f.roots, rootName)
	replaces := 0
	failing := scriptedKeyStore{
		SubjectKeyStore: f.store,
		replace: func(ctx context.Context, subject string, current, next []byte) (bool, error) {
			replaces++
			if replaces > 1 {
				return false, errBackendDown
			}
			return f.store.Replace(ctx, subject, current, next)
		},
	}
	report, err := engineOver(t, f.root, failing).Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 2, Rewrapped: 1}) || !errors.Is(err, errBackendDown) || !errs.HasCode(err, coresecret.CodeStoreUnavailable) {
		t.Fatalf("Rewrap = (%+v, %v), want one key moved, then StoreUnavailable", report, err)
	}
	stray := scriptedKeyStore{
		SubjectKeyStore: f.store,
		all: func(ctx context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
			return func(yield func(coresecret.SubjectKeyValue, error) bool) {
				if !yield(coresecret.SubjectKeyValue{Subject: "Not A Subject", Wrapped: []byte{0x01}}, nil) {
					return
				}
				for entry, entryErr := range f.store.All(ctx) {
					if !yield(entry, entryErr) {
						return
					}
				}
			}
		},
	}
	report, err = engineOver(t, f.root, stray).Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 2, Current: 1, Rewrapped: 1, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap over a stray entry = (%+v, %v), want it counted unreadable and the rest moved", report, err)
	}
}

// TestSubjectKeysRewrapCountsAnAlteredKey pins that a wrapped key whose
// ciphertext was altered in the store does not unwrap during a pass: counted,
// left as it is, and loud.
func TestSubjectKeysRewrapCountsAnAlteredKey(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	seal(t, keys, "user:1", "value")
	entry := filed(t, f.store)[0]
	altered := slices.Clone(entry.Wrapped)
	altered[len(altered)-1] ^= 0x01
	if replaced, err := f.store.Replace(t.Context(), "user:1", entry.Wrapped, altered); !replaced || err != nil {
		t.Fatalf("Replace = (%v, %v)", replaced, err)
	}
	//: under the newest version already: a header alone would call it current.
	report, err := keys.Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 1, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap before a rotation = (%+v, %v), want the altered key counted unreadable, not current", report, err)
	}
	putRandomVersion(t, f.roots, rootName)
	report, err = keys.Rewrap(t.Context())
	if report != (svcsecret.RewrapValue{Root: 2, Unreadable: 1}) || !errs.HasCode(err, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Rewrap = (%+v, %v), want the altered key counted unreadable", report, err)
	}
}

// TestRotatorInUseAnswersOutsideTheVersionsKeepThePolicy pins the two answers
// that constrain nothing: 0 — no key at all — and a version the store never
// numbered. Both prune to Keep, exactly as a rotator without InUse would.
func TestRotatorInUseAnswersOutsideTheVersionsKeepThePolicy(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		answer int
	}
	tests := []tc{
		{"no key at all", 0},
		{"a version the store never numbered", 1000},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		f := newSubjectFixture(t)
		rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
			Store:  f.roots,
			Name:   rootName,
			Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 2, Generate: svcsecret.Random(32)},
			Clock:  f.clk,
			InUse:  func(context.Context) (int, error) { return c.answer, nil },
		})
		if err != nil {
			t.Fatalf("NewRotator: %v", err)
		}
		for range 3 {
			if _, rotateErr := rotator.Rotate(t.Context()); rotateErr != nil {
				t.Fatalf("Rotate: %v", rotateErr)
			}
		}
		if got := f.rootVersions(t); !slices.Equal(got, []int{4, 3}) {
			t.Fatalf("%s: root keeps %v, want [4 3]", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSubjectKeysAKeyMadeAcrossTwoRotationsStillOpens pins the creation race:
// a Seal wraps a new subject's key under version 1, and before its insert
// lands two rotations run — the first keeps version 1 as the one it replaced,
// the second scans a store where the key is not filed yet and prunes version
// 1. The key must still end up under a version the root keeps: the Seal reads
// the newest version after its insert and moves its own key there.
func TestSubjectKeysAKeyMadeAcrossTwoRotationsStillOpens(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	var rotator *svcsecret.Rotator
	inserts := 0
	stalling := scriptedKeyStore{
		SubjectKeyStore: f.store,
		insert: func(ctx context.Context, subject string, wrapped []byte) (bool, error) {
			inserts++
			//: the first insert stalls across two whole rotations.
			if inserts == 1 {
				for range 2 {
					if _, err := rotator.Rotate(ctx); err != nil {
						return false, err
					}
				}
			}
			return f.store.Insert(ctx, subject, wrapped)
		},
	}
	keys := engineOver(t, f.root, stalling)
	built, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store:  f.roots,
		Name:   rootName,
		Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 2, Generate: svcsecret.Random(32)},
		Clock:  f.clk,
		InUse:  keys.OldestRoot,
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	rotator = built
	box := seal(t, keys, "user:1", "made while the root rotated twice")
	if got := f.rootVersions(t); !slices.Equal(got, []int{3, 2}) {
		t.Fatalf("root keeps %v, want [3 2]: the second rotation retired version 1", got)
	}
	if got := wrappedVersions(t, f.store); !slices.Equal(got, []int{3}) {
		t.Fatalf("the new key is wrapped under %v, want [3], a version the root keeps", got)
	}
	mustOpen(t, f.process(t, 0), box, "made while the root rotated twice")
}

// TestOldestRootCountsOnlyKeysThatOpen pins that a key nothing can open pins
// no version: one wrapped under a version already pruned, one altered, one
// that is no keyring box. Counting them would keep every later version for
// ever — the root would never prune again, and a backed-up wrapped key would
// keep opening long after its subject's erasure.
func TestOldestRootCountsOnlyKeysThatOpen(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	seal(t, keys, "user:1", "lost with version 1")
	putRandomVersion(t, f.roots, rootName)
	if err := f.roots.Prune(t.Context(), rootName, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	box := seal(t, keys, "user:2", "alive under version 2")
	alive := filed(t, f.store)[1]
	altered := slices.Clone(alive.Wrapped)
	altered[len(altered)-1] ^= 0x01
	for subject, wrapped := range map[string][]byte{"user:3": altered, "user:4": []byte("no keyring box")} {
		if inserted, err := f.store.Insert(t.Context(), subject, wrapped); !inserted || err != nil {
			t.Fatalf("Insert(%s) = (%v, %v)", subject, inserted, err)
		}
	}
	if oldest, err := keys.OldestRoot(t.Context()); oldest != 2 || err != nil {
		t.Fatalf("OldestRoot = (%d, %v), want (2, nil): the lost keys pin nothing", oldest, err)
	}
	rotator, err := svcsecret.NewRotator(svcsecret.RotatorConfig{
		Store:  f.roots,
		Name:   rootName,
		Policy: svcsecret.PolicySpec{Every: rotationEvery, Keep: 2, Generate: svcsecret.Random(32)},
		Clock:  f.clk,
		InUse:  keys.OldestRoot,
		OnRotate: func(coresecret.VersionValue) {
			//: the live key moves on; the lost ones are reported, as they must be.
			report, rewrapErr := keys.Rewrap(context.Background())
			if report.Rewrapped != 1 || !errs.HasCode(rewrapErr, coresecret.CodeSubjectKeyUnreadable) {
				t.Errorf("Rewrap = (%+v, %v), want the live key moved and the lost ones reported", report, rewrapErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	for range 3 {
		if _, rotateErr := rotator.Rotate(t.Context()); rotateErr != nil {
			t.Fatalf("Rotate: %v", rotateErr)
		}
	}
	if got := f.rootVersions(t); !slices.Equal(got, []int{5, 4}) {
		t.Fatalf("root keeps %v, want [5 4]: versions piled up behind keys nothing can open", got)
	}
	mustOpen(t, f.process(t, 0), box, "alive under version 2")
}

// TestOldestRootStopsAtTheNextKeyWhenCancelled pins that a cancelled scan
// ends at the next key rather than after the whole store — a rotation given a
// deadline must not read a million keys past it.
func TestOldestRootStopsAtTheNextKeyWhenCancelled(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	for _, subject := range []string{"user:1", "user:2", "user:3"} {
		seal(t, f.process(t, 0), subject, "value")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	yielded := 0
	counting := scriptedKeyStore{
		SubjectKeyStore: f.store,
		all: func(c context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
			return func(yield func(coresecret.SubjectKeyValue, error) bool) {
				for entry, err := range f.store.All(c) {
					yielded++
					if !yield(entry, err) {
						return
					}
					//: the caller gives up after the first key.
					cancel()
				}
			}
		},
	}
	_, err := engineOver(t, f.root, counting).OldestRoot(ctx)
	if !errors.Is(err, context.Canceled) || yielded != 2 {
		t.Fatalf("OldestRoot = %v after %d keys, want context.Canceled at the second of three", err, yielded)
	}
}

// TestSubjectKeysRewrapLeavesANewerKeyToTheNextPass pins what a pass does with
// a key filed under a version NEWER than the one it read — a rotation and a
// first Seal landed after the pass began: current, not unreadable, and left
// alone; it is the next pass's to open.
func TestSubjectKeysRewrapLeavesANewerKeyToTheNextPass(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	seal(t, f.process(t, 0), "user:1", "under version 1")
	var box []byte
	late := scriptedKeyStore{
		SubjectKeyStore: f.store,
		all: func(ctx context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
			//: after the pass read the root: a rotation, and a new subject under it.
			putRandomVersion(t, f.roots, rootName)
			box = seal(t, f.process(t, 0), "user:2", "under version 2")
			return f.store.All(ctx)
		},
	}
	report, err := engineOver(t, f.root, late).Rewrap(t.Context())
	if err != nil || report != (svcsecret.RewrapValue{Root: 1, Current: 2}) {
		t.Fatalf("Rewrap = (%+v, %v), want both keys current and no unreadable", report, err)
	}
	if got := wrappedVersions(t, f.store); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("keys wrapped under %v, want [1 2]: the pass moved nothing down", got)
	}
	mustOpen(t, f.process(t, 0), box, "under version 2")
}
