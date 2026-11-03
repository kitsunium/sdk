// Package docstore_test — a replacement made on the revision the caller read,
// one contract run on every engine: memory, file, and the SQL store on each
// dialect's statements over the fake engine (#1055, ADR 0209).
package docstore_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// revisionEngine is one engine's accounts store seen through the calls the
// revision contract reads, the SQL store's context supplied.
type revisionEngine struct {
	revision  func(key string) (account, coredocstore.RevisionValue, error)
	replaceIf func(v account, rev coredocstore.RevisionValue) error
	get       func(key string) (account, error)
	insert    func(v account) error
	put       func(v account) error
	del       func(key string) error
	trash     func(key string) error
	lookup    func(index, key string) (account, error)
	versions  func(key string) ([]coredocstore.VersionValue, error)
}

// fileRevisionEngine is the file engine's store seen as a revisionEngine.
func fileRevisionEngine(store *docstore.Store[account]) revisionEngine {
	var _ coredocstore.Revisioned[account] = store
	return revisionEngine{
		revision: store.Revision, replaceIf: store.ReplaceIf, get: store.Get,
		insert: store.Insert, put: store.Put, del: store.Delete, trash: store.Trash,
		lookup: store.Lookup, versions: store.Versions,
	}
}

// sqlRevisionEngine is the SQL engine's store, every call under ctx.
func sqlRevisionEngine(ctx context.Context, store *docstore.SQLStore[account]) revisionEngine {
	var _ coredocstore.RevisionedContext[account] = store
	return revisionEngine{
		revision: func(key string) (account, coredocstore.RevisionValue, error) { return store.Revision(ctx, key) },
		replaceIf: func(v account, rev coredocstore.RevisionValue) error {
			return store.ReplaceIf(ctx, v, rev)
		},
		get:      func(key string) (account, error) { return store.Get(ctx, key) },
		insert:   func(v account) error { return store.Insert(ctx, v) },
		put:      func(v account) error { return store.Put(ctx, v) },
		del:      func(key string) error { return store.Delete(ctx, key) },
		trash:    func(key string) error { return store.Trash(ctx, key) },
		lookup:   func(index, key string) (account, error) { return store.Lookup(ctx, index, key) },
		versions: func(key string) ([]coredocstore.VersionValue, error) { return store.Versions(ctx, key) },
	}
}

// eachRevisionEngine runs fn over every engine — memory, file, and the SQL
// store on each dialect — with and without versions, each in a subtest.
func eachRevisionEngine(t *testing.T, fn func(t *testing.T, e revisionEngine)) {
	t.Helper()
	for _, versions := range []int{0, 2} {
		t.Run("versions="+strconv.Itoa(versions), func(t *testing.T) {
			t.Parallel()
			eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
				fn(t, fileRevisionEngine(openVersioned(t, versionedConfig(fsys, versions, clock.NewManualClock(epoch)))))
			})
			eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
				fn(t, sqlRevisionEngine(t.Context(),
					openSQLVersioned(t, dialect, keeping(versions, clock.NewManualClock(epoch))).store))
			})
		})
	}
}

// TestOneRevisionReplacesOnce pins the contract: two ReplaceIf with one token
// — exactly one stores, the other is REVISION_MISMATCH and changes nothing —
// and the winner's write moved the revision.
func TestOneRevisionReplacesOnce(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		must(t, e.insert(account{ID: "acc_1", Email: "ada@example.com"}))
		read, rev, err := e.revision("acc_1")
		if err != nil || read.Email != "ada@example.com" || rev == "" {
			t.Fatalf("Revision = (%+v, %q, %v), want the document and a token", read, rev, err)
		}
		must(t, e.replaceIf(account{ID: "acc_1", Email: "first@example.com"}, rev))
		lost := e.replaceIf(account{ID: "acc_1", Email: "second@example.com"}, rev)
		requireCode(t, lost, coredocstore.CodeRevisionMismatch, "the second ReplaceIf on one token")
		quotesNothing(t, lost, "acc_1", string(rev))
		if got, _ := e.get("acc_1"); got.Email != "first@example.com" {
			t.Fatalf("Get = %+v, want the first replacement", got)
		}
		//: the unique index follows the replacement, as Replace's does.
		if owner, lookupErr := e.lookup("email", "first@example.com"); lookupErr != nil || owner.ID != "acc_1" {
			t.Fatalf("Lookup(first) = (%+v, %v), want acc_1", owner, lookupErr)
		}
		_, now, _ := e.revision("acc_1")
		if now == rev {
			t.Fatalf("the revision after a replacement is still %q", rev)
		}
		must(t, e.replaceIf(account{ID: "acc_1", Email: "third@example.com"}, now))
	})
}

// TestAnyWriteBetweenTheReadAndTheReplacementRefusesIt pins what a revision
// guards against: a Put between Revision and ReplaceIf is not overwritten.
func TestAnyWriteBetweenTheReadAndTheReplacementRefusesIt(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		must(t, e.insert(account{ID: "acc_1", Name: "Ada"}))
		_, rev, err := e.revision("acc_1")
		must(t, err)
		must(t, e.put(account{ID: "acc_1", Name: "Grace"}))
		requireCode(t, e.replaceIf(account{ID: "acc_1", Name: "Lost"}, rev), coredocstore.CodeRevisionMismatch, "ReplaceIf after a Put")
		if got, _ := e.get("acc_1"); got.Name != "Grace" {
			t.Fatalf("Get = %+v, want the Put's document", got)
		}
		//: a token no engine ever gave matches nothing.
		requireCode(t, e.replaceIf(account{ID: "acc_1", Name: "Lost"}, "not-a-revision"), coredocstore.CodeRevisionMismatch, "ReplaceIf on a forged token")
	})
}

// TestARevisionOfNothingIsNotFound pins the misses: Revision of a missing or
// trashed key, and ReplaceIf of a deleted one, which is never a creation.
func TestARevisionOfNothingIsNotFound(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		_, _, missing := e.revision("acc_missing")
		requireCode(t, missing, coredocstore.CodeDocumentNotFound, "Revision of a missing key")
		must(t, e.insert(account{ID: "acc_1"}))
		_, rev, err := e.revision("acc_1")
		must(t, err)
		must(t, e.del("acc_1"))
		requireCode(t, e.replaceIf(account{ID: "acc_1", Name: "Back"}, rev), coredocstore.CodeDocumentNotFound, "ReplaceIf of a deleted key")
		if _, getErr := e.get("acc_1"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
			t.Fatalf("Get after a refused ReplaceIf = %v, want DOCUMENT_NOT_FOUND: no creation", getErr)
		}
	})
}

// TestATrashedDocumentHasNoRevision pins the trash on a store that keeps
// versions: a trashed document is gone from every read, Revision included,
// and a ReplaceIf does not take it out of the trash.
func TestATrashedDocumentHasNoRevision(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		must(t, e.insert(account{ID: "acc_1"}))
		_, rev, err := e.revision("acc_1")
		must(t, err)
		trashErr := e.trash("acc_1")
		if errs.HasCode(trashErr, coredocstore.CodeVersionsNotKept) {
			return
		}
		must(t, trashErr)
		_, _, trashed := e.revision("acc_1")
		requireCode(t, trashed, coredocstore.CodeDocumentNotFound, "Revision of a trashed key")
		requireCode(t, e.replaceIf(account{ID: "acc_1"}, rev), coredocstore.CodeDocumentNotFound, "ReplaceIf of a trashed key")
	})
}

// TestAReplacementOnARevisionMakesAVersion pins that ReplaceIf writes as
// Replace does on a store that keeps versions: one version more.
func TestAReplacementOnARevisionMakesAVersion(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		must(t, e.insert(account{ID: "acc_1", Name: "Ada"}))
		_, rev, err := e.revision("acc_1")
		must(t, err)
		must(t, e.replaceIf(account{ID: "acc_1", Name: "Grace"}, rev))
		all, versionsErr := e.versions("acc_1")
		if errs.HasCode(versionsErr, coredocstore.CodeVersionsNotKept) {
			return
		}
		must(t, versionsErr)
		if got := numbers(all); len(got) != 2 || got[0] != 2 {
			t.Fatalf("versions after ReplaceIf = %v, want [2 1]", got)
		}
	})
}

// TestConcurrentReplacementsOnOneRevisionHaveOneWinner races eight ReplaceIf
// on one token: exactly one stores.
func TestConcurrentReplacementsOnOneRevisionHaveOneWinner(t *testing.T) {
	t.Parallel()
	eachRevisionEngine(t, func(t *testing.T, e revisionEngine) {
		must(t, e.insert(account{ID: "acc_1"}))
		_, rev, err := e.revision("acc_1")
		must(t, err)
		var wg sync.WaitGroup
		results := make([]error, 8)
		for i := range results {
			wg.Go(func() { results[i] = e.replaceIf(account{ID: "acc_1", Name: string(rune('a' + i))}, rev) })
		}
		wg.Wait()
		won := 0
		for _, result := range results {
			switch {
			case result == nil:
				won++
			case !errs.HasCode(result, coredocstore.CodeRevisionMismatch):
				t.Fatalf("a losing ReplaceIf = %v, want REVISION_MISMATCH", result)
			}
		}
		if won != 1 {
			t.Fatalf("%d ReplaceIf stored on one token, want exactly 1", won)
		}
	})
}

// TestARevisionSurvivesAFoldAndAReopenOfTheFileStore pins the file engine's
// token: the SHA-256 of the document as json.Marshal writes it, so a fold,
// which writes the snapshot indented, and a reopen keep it.
func TestARevisionSurvivesAFoldAndAReopenOfTheFileStore(t *testing.T) {
	t.Parallel()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		if fsys == nil {
			t.Skip("a memory store has no files to fold or reopen")
		}
		store, err := openWith(accountConfig(fsys))
		must(t, err)
		must(t, store.Insert(account{ID: "acc_1", Email: "ada&grace@example.com"}))
		_, rev, revErr := store.Revision("acc_1")
		must(t, revErr)
		must(t, store.Fold())
		_, folded, _ := store.Revision("acc_1")
		if folded != rev {
			t.Fatalf("Revision after a fold = %q, want %q", folded, rev)
		}
		must(t, store.Close())
		again, reopenErr := openWith(accountConfig(fsys))
		must(t, reopenErr)
		defer func() { must(t, again.Close()) }()
		_, reopened, _ := again.Revision("acc_1")
		if reopened != rev {
			t.Fatalf("Revision after a reopen = %q, want %q", reopened, rev)
		}
		must(t, again.ReplaceIf(account{ID: "acc_1", Email: "ada@example.com"}, rev))
	})
}
