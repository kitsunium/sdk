// Package session — the file store's failure paths around publication: the
// directory flush that makes a rename or an unlink survive a power cut
// (observed and made to fail through fileStore.syncDir), the rotation that
// withdraws what it published when it cannot finish, and the orphan cleanup
// behind a rename that fails.
package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// errFlushRefused stands in for an fsync the device refused.
var errFlushRefused = errors.New("fsync: input/output error") //nolint:err113 // a test double standing in for a failing fsync, not SDK code

// flushRecorder replaces fileStore.syncDir. At every call it records which
// record files the directory holds AT THAT MOMENT — that is what proves a
// flush ran after the rename or unlink it exists to make durable, rather than
// merely somewhere inside the operation — and it answers with the next queued
// result, nil once the queue is empty.
//
// The store calls it under its own lock on the test's goroutine, so it needs
// no lock of its own.
type flushRecorder struct {
	seen    [][]string
	results []error
}

// flush is the syncDir replacement.
func (r *flushRecorder) flush(dir string) error {
	entries, listErr := os.ReadDir(dir)
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), recordSuffix) {
			names = append(names, entry.Name())
		}
	}
	r.seen = append(r.seen, names)
	if listErr != nil {
		return listErr
	}
	if len(r.results) == 0 {
		return nil
	}
	next := r.results[0]
	r.results = r.results[1:]
	return next
}

// take returns what every flush since the last take saw, and forgets it.
func (r *flushRecorder) take() [][]string {
	seen := r.seen
	r.seen = nil
	return seen
}

// reset forgets what every flush so far saw, for a test that only cares about
// the flushes still to come.
func (r *flushRecorder) reset() {
	r.seen = nil
}

// flushObservedStore builds a file store whose directory flushes go through
// rec, skipping where the file store honestly refuses to exist.
func flushObservedStore(t *testing.T, clk clock.Clock, rec *flushRecorder) *fileStore {
	t.Helper()
	store, err := NewFileStore(FileConfig{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 100 * time.Minute, Clock: clk,
		Dir: filepath.Join(t.TempDir(), "sessions"), Key: internalKey(t),
	})
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		t.Skip("file store has no native mechanic on this platform")
	}
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	concrete, ok := store.(*fileStore)
	if !ok {
		t.Fatalf("NewFileStore returned %T, want *fileStore", store)
	}
	concrete.syncDir = rec.flush
	return concrete
}

// fileOf names the record file of a session.
func fileOf(session coresession.SessionValue) string {
	return session.ID().Digest() + recordSuffix
}

// assertFlushes fails unless the flushes one operation caused saw exactly want,
// in order — one entry per flush, each the sorted record files present then.
func assertFlushes(t *testing.T, op string, got, want [][]string) {
	t.Helper()
	if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Errorf("%s: the directory flushes saw %q, want %q", op, got, want)
	}
}

// TestEveryRenameAndUnlinkIsFlushedAfterItHappens pins the first half of the
// durability rule: each operation that renames a record into place or unlinks
// one is followed by an fsync of the directory, and the fsync comes AFTER the
// change — a flush that ran before the rename would make nothing durable.
//
// POSIX does not require a rename or an unlink to survive a crash until the
// directory is flushed. Before this, publish synced the record's bytes and
// never the directory, and removal never flushed at all, so a power cut after
// Destroy could legally bring the destroyed session back: a revocation undone.
// Sweep unlinks every dead record first and then flushes ONCE, which is why
// its expectation is a single flush over two removals.
//
// Mutations: dropping the flush from writeLocked failed with `New: the
// directory flushes saw [], want [["<digest>.session"]]`; dropping it from
// removeLocked failed with `Destroy: the directory flushes saw [], want [[]]`.
func TestEveryRenameAndUnlinkIsFlushedAfterItHappens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manual := clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC))
	rec := &flushRecorder{}
	store := flushObservedStore(t, manual, rec)

	fresh, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	//: the rename happened before the flush: the flush already sees the file.
	assertFlushes(t, "New", rec.take(), [][]string{{fileOf(fresh)}})

	if saveErr := store.Save(ctx, fresh.Set("state", "saved")); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	assertFlushes(t, "Save", rec.take(), [][]string{{fileOf(fresh)}})

	if _, loadErr := store.Load(ctx, fresh.ID()); loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	//: Load writes too — it slides the idle window — so it flushes too.
	assertFlushes(t, "Load", rec.take(), [][]string{{fileOf(fresh)}})

	bound, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
	if regenErr != nil {
		t.Fatalf("Regenerate: %v", regenErr)
	}
	//: one flush after the new record is in place (both exist), one after the
	//: old one is gone.
	both := []string{fileOf(fresh), fileOf(bound)}
	slices.Sort(both)
	assertFlushes(t, "Regenerate", rec.take(), [][]string{both, {fileOf(bound)}})

	if destroyErr := store.Destroy(ctx, bound.ID()); destroyErr != nil {
		t.Fatalf("Destroy: %v", destroyErr)
	}
	//: the unlink happened before the flush: the flush sees nothing left.
	assertFlushes(t, "Destroy", rec.take(), [][]string{{}})

	if destroyErr := store.Destroy(ctx, bound.ID()); destroyErr != nil {
		t.Fatalf("second Destroy: %v", destroyErr)
	}
	//: a retried revocation flushes again — the first one's flush may be the
	//: one that failed.
	assertFlushes(t, "second Destroy", rec.take(), [][]string{{}})

	first, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	manual.Advance(31 * time.Minute)
	survivor, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec.reset()
	removed, sweepErr := store.Sweep(ctx)
	if sweepErr != nil || removed != 2 {
		t.Fatalf("Sweep = (%d, %v), want (2, nil)", removed, sweepErr)
	}
	//: two unlinks, ONE flush, after both.
	assertFlushes(t, "Sweep", rec.take(), [][]string{{fileOf(survivor)}})
	//: and the two it removed really are gone.
	for _, dead := range []coresession.SessionValue{first, second} {
		if _, statErr := os.Stat(filepath.Join(store.dir, fileOf(dead))); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("a swept record is still on disk: %v", statErr)
		}
	}
}

// TestAFailedFlushIsReportedAndNotRolledBack pins the second half: when the
// directory flush fails, the caller hears StoreUnavailable — the change may
// not survive a power cut, and saying nothing would promise that it does — and
// the change is NOT undone, because it already happened: every reader sees it,
// and reverting a rename to repair a durability problem is a second write that
// can fail the same way (ADR 0056 D7). The retry is what makes it durable.
//
// Mutation: making flushLocked return nil whatever syncDir said failed with
// `Save with a refused flush = <nil>, want CodeStoreUnavailable`.
func TestAFailedFlushIsReportedAndNotRolledBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := &flushRecorder{}
	store := flushObservedStore(t, clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)), rec)
	fresh, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec.results = []error{errFlushRefused}
	saveErr := store.Save(ctx, fresh.Set("state", "published"))
	if !errs.HasCode(saveErr, coresession.CodeStoreUnavailable) {
		t.Fatalf("Save with a refused flush = %v, want CodeStoreUnavailable", saveErr)
	}
	if op := fieldOf(saveErr, "op"); op != "sync-dir-publish" {
		t.Errorf("op = %q, want %q — the refusal must name the flush, not an earlier step", op, "sync-dir-publish")
	}
	//: the rename was not undone: the next read sees the new content.
	loaded, loadErr := store.Load(ctx, fresh.ID())
	if loadErr != nil {
		t.Fatalf("Load after a refused flush: %v", loadErr)
	}
	if value, _ := loaded.Get("state"); value != "published" {
		t.Errorf("state = %q, want %q — the published record was rolled back", value, "published")
	}
	//: and the orphan cleanup had nothing to clean: the temporary was renamed.
	leftovers, globErr := filepath.Glob(filepath.Join(store.dir, ".tmp-*"))
	if globErr != nil {
		t.Fatalf("Glob: %v", globErr)
	}
	if len(leftovers) != 0 {
		t.Errorf("a refused flush left temporary files behind: %q", leftovers)
	}

	rec.results = []error{errFlushRefused}
	destroyErr := store.Destroy(ctx, fresh.ID())
	if !errs.HasCode(destroyErr, coresession.CodeStoreUnavailable) || fieldOf(destroyErr, "op") != "sync-dir-remove" {
		t.Fatalf("Destroy with a refused flush = %v, want CodeStoreUnavailable from sync-dir-remove", destroyErr)
	}
	//: the unlink was not undone either: the session is already unusable.
	if _, loadErr := store.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeNotFound) {
		t.Errorf("Load after a Destroy whose flush failed = %v, want CodeNotFound", loadErr)
	}
	rec.reset()
	//: the retry is what makes the revocation durable, so it flushes again.
	if retryErr := store.Destroy(ctx, fresh.ID()); retryErr != nil {
		t.Fatalf("retried Destroy: %v", retryErr)
	}
	assertFlushes(t, "retried Destroy", rec.take(), [][]string{{}})
}

// TestAFailedRotationWithdrawsTheRecordItPublished pins the rotation's
// rollback: when Regenerate cannot retire the old record after publishing the
// new one, the new one is removed again.
//
// Before, that error path returned at once and the new record — live, bound to
// the principal, and named by an identifier the caller was never given —
// stayed on disk until it expired, and every retry of the failed login
// published another. The failure is provoked at the directory flush after the
// old record's unlink, a rotation's second flush; the first follows the new
// record's rename, and the recorder shows the new record in place by then, so
// the withdrawal asserted below is not vacuous.
//
// Mutation: returning removeErr without withdrawing failed both cases with "a
// failed rotation left 1 record file(s) behind".
func TestAFailedRotationWithdrawsTheRecordItPublished(t *testing.T) {
	t.Parallel()
	errWithdrawRefused := errors.New("fsync: no space left on device") //nolint:err113 // a second test double, told apart from the first by its text
	tests := []struct {
		name           string
		results        []error
		withdrawFailed bool
	}{
		{"the withdrawal succeeds", []error{nil, errFlushRefused}, false},
		{"the withdrawal fails too, and the cause stays the answer", []error{nil, errFlushRefused, errWithdrawRefused}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			rec := &flushRecorder{}
			store := flushObservedStore(t, clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)), rec)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rec.reset()
			rec.results = tc.results
			rotated, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
			if !errs.HasCode(regenErr, coresession.CodeStoreUnavailable) || fieldOf(regenErr, "op") != "sync-dir-remove" {
				t.Fatalf("Regenerate = %v, want CodeStoreUnavailable from retiring the old record", regenErr)
			}
			if !rotated.ID().IsZero() {
				t.Error("a failed Regenerate returned a session")
			}
			//: the first flush ran with BOTH records present: the new one was
			//: published before anything failed.
			if seen := rec.take(); len(seen) == 0 || len(seen[0]) != 2 {
				t.Fatalf("the flushes saw %q; the new record was never published, so this proves nothing", seen)
			}
			leftovers, globErr := filepath.Glob(filepath.Join(store.dir, "*"+recordSuffix))
			if globErr != nil {
				t.Fatalf("Glob: %v", globErr)
			}
			//: nothing survives under an identifier nobody was given.
			if len(leftovers) != 0 {
				t.Errorf("a failed rotation left %d record file(s) behind: %q", len(leftovers), leftovers)
			}
			//: the reason the rotation failed is still the answer; a failed
			//: withdrawal is recorded beside it, never in its place.
			if cause := fieldOf(regenErr, "cause"); cause != errFlushRefused.Error() {
				t.Errorf("cause = %q, want the retirement's own failure %q", cause, errFlushRefused.Error())
			}
			if recorded := fieldOf(regenErr, "withdraw") != ""; recorded != tc.withdrawFailed {
				t.Errorf("withdraw field present = %v, want %v", recorded, tc.withdrawFailed)
			}
		})
	}
}

// TestAFailedRenameLeavesNoOrphan executes the cleanup publish promises on
// every failure AFTER its temporary exists — which, it turned out, no test in
// this package did. TestAFailedPublishLeavesThePreviousRecord fails before the
// temporary is created, and TestARenameOntoADirectoryLeavesNothingBehind fails
// at Save's READ of the directory ("is a directory", op=read), never reaching
// the rename it is named after; both assert "no orphan" over a publication
// that never made one. Here publish is called directly, so the temporary is
// created, narrowed, written, synced and closed, and only the switch fails.
//
// Mutation: dropping removeTemp from publish's deferred cleanup failed with
// `a failed rename left the temporary behind: [".../sessions/.tmp-…"]`.
func TestAFailedRenameLeavesNoOrphan(t *testing.T) {
	t.Parallel()
	store := flushObservedStore(t, clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)), &flushRecorder{})
	//: rename(2) refuses to replace a directory with a file, on every Unix.
	target := filepath.Join(store.dir, strings.Repeat("ab", 32)+recordSuffix)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "occupied"), []byte("prior"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	publishErr := store.publish(target, []byte("a sealed record"))
	if !errs.HasCode(publishErr, coresession.CodeStoreUnavailable) || fieldOf(publishErr, "op") != "rename" {
		t.Fatalf("publish onto a directory = %v (op %q), want CodeStoreUnavailable from the rename",
			publishErr, fieldOf(publishErr, "op"))
	}
	leftovers, globErr := filepath.Glob(filepath.Join(store.dir, ".tmp-*"))
	if globErr != nil {
		t.Fatalf("Glob: %v", globErr)
	}
	if len(leftovers) != 0 {
		t.Errorf("a failed rename left the temporary behind: %q", leftovers)
	}
	//: and what stood in the target's place is exactly as it was.
	if prior, readErr := os.ReadFile(filepath.Join(target, "occupied")); readErr != nil || string(prior) != "prior" {
		t.Errorf("the failed rename disturbed the target: %q (%v)", prior, readErr)
	}
}

// fieldOf returns the value of one field of an SDK error, or "".
func fieldOf(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}
