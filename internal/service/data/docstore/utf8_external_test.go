// Package docstore_test — a key is valid UTF-8 (#459): encoding/json writes
// an invalid byte as U+FFFD, so a store key or an index key that is not
// would be another key once the document is read back — after a reopen, a
// reindex — and two keys differing only there would become one, losing an
// acknowledged write. Every engine refuses it before anything is written.
package docstore_test

import (
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// invalidUTF8 are keys JSON cannot carry as they are: a lone continuation
// byte, a byte never valid, a truncated sequence, an encoded surrogate and an
// overlong encoding — each after a valid prefix, and one alone.
var invalidUTF8 = []string{"user\xff", "k\xfe", "\x80", "caf\xc3", "\xed\xa0\x80", "\xc0\xaf"}

// validUTF8 are keys outside ASCII that JSON carries unchanged, and that must
// keep being accepted: an accent, an emoji, a CJK ideograph, and the last
// code point.
var validUTF8 = []string{"é", "user-😀", "名前", "\U0010FFFF"}

// refusedAsUnencodable requires err to be DocumentUnencodable, naming index
// when it is not empty, and quoting none of the key's bytes.
func refusedAsUnencodable(t *testing.T, err error, index, key, what string) {
	t.Helper()
	requireCode(t, err, coredocstore.CodeDocumentUnencodable, what)
	if got := fieldValue(errs.FieldsOf(err), "index"); got != index {
		t.Fatalf("%s names index %q, want %q", what, got, index)
	}
	quotesNothing(t, err, key)
}

// TestAnInvalidUTF8KeyIsRefusedByTheFileEngine pins the refusal on the memory
// and file engines, for every write mode and Update, for the store key and
// for a unique and a multi-valued index key — and that a refused write leaves
// nothing behind, in memory or on disk.
func TestAnInvalidUTF8KeyIsRefusedByTheFileEngine(t *testing.T) {
	t.Parallel()
	for name, persisted := range map[string]bool{"memory": false, "file": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fsys := memFS()
			cfg := accountConfig(nil)
			if persisted {
				cfg = accountConfig(fsys)
			}
			store, err := openWith(cfg)
			must(t, err)
			t.Cleanup(func() { _ = store.Close() })
			must(t, store.Put(account{ID: "acc_1", Email: "ada@example.com"}))
			for _, key := range invalidUTF8 {
				refusedAsUnencodable(t, store.Put(account{ID: key}), "", key, "Put of a key "+key)
				refusedAsUnencodable(t, store.Insert(account{ID: key}), "", key, "Insert")
				refusedAsUnencodable(t, store.Replace(account{ID: key}), "", key, "Replace")
				refusedAsUnencodable(t, store.Put(account{ID: "acc_2", Email: key}), "email", key, "a unique index key")
				refusedAsUnencodable(t, store.Put(account{ID: "acc_2", Teams: []string{"ok", key}}), "team", key, "a multi-valued index key")
				_, updErr := store.Update("acc_1", func(a *account) error { a.Email = key; return nil })
				refusedAsUnencodable(t, updErr, "email", key, "an Update writing an index key")
			}
			all, listErr := store.List()
			must(t, listErr)
			if got := ids(all); len(got) != 1 || got[0] != "acc_1" {
				t.Fatalf("refused writes stored documents: %v", got)
			}
			if got, getErr := store.Get("acc_1"); getErr != nil || got.Email != "ada@example.com" {
				t.Fatalf("a refused Update changed the document: %+v, %v", got, getErr)
			}
			if persisted {
				if entries := overlayEntries(t, fsys); len(entries) != 1 {
					t.Fatalf("refused writes reached the disk: %v", entries)
				}
			}
		})
	}
}

// TestAValidUTF8KeySurvivesAReopen pins the other side: keys outside ASCII
// that are valid UTF-8 are stored, found by key and by index, and found again,
// unchanged and distinct, after the store is closed and reopened.
func TestAValidUTF8KeySurvivesAReopen(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	store, err := openWith(accountConfig(fsys))
	must(t, err)
	for _, key := range validUTF8 {
		must(t, store.Put(account{ID: key, Email: key + "@example.com", Teams: []string{key}}))
	}
	must(t, store.Close())
	reopened := openAccounts(t, fsys)
	all, err := reopened.List()
	must(t, err)
	if len(all) != len(validUTF8) {
		t.Fatalf("%d documents before the reopen, %d after", len(validUTF8), len(all))
	}
	for _, key := range validUTF8 {
		got, getErr := reopened.Get(key)
		if getErr != nil || got.ID != key {
			t.Fatalf("Get(%q) after a reopen = %+v, %v", key, got, getErr)
		}
		if found, lookupErr := reopened.Lookup("email", key+"@example.com"); lookupErr != nil || found.ID != key {
			t.Fatalf("Lookup(email, %q) after a reopen = %+v, %v", key, found, lookupErr)
		}
		if _, updErr := reopened.Update(key, func(*account) error { return nil }); updErr != nil {
			t.Fatalf("a no-op Update of %q = %v", key, updErr)
		}
	}
}

// TestAnInvalidUTF8KeyIsRefusedByTheSQLEngine pins the same refusal on every
// dialect, before any statement is sent: the SQL engine stores the document
// as JSON too, so the key read back would differ from the key row.
func TestAnInvalidUTF8KeyIsRefusedByTheSQLEngine(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx, fx := t.Context(), openSQL(t, dialect)
		for _, key := range invalidUTF8 {
			refusedAsUnencodable(t, fx.store.Put(ctx, account{ID: key}), "", key, "Put")
			refusedAsUnencodable(t, fx.store.Insert(ctx, account{ID: key}), "", key, "Insert")
			refusedAsUnencodable(t, fx.store.Put(ctx, account{ID: "acc_1", Email: key}), "email", key, "a unique index key")
			refusedAsUnencodable(t, fx.store.Put(ctx, account{ID: "acc_1", Teams: []string{key}}), "team", key, "a multi-valued index key")
		}
		if roles := fx.engine.roles(); len(roles) != 0 {
			t.Fatalf("a refused key reached the database: %v", roles)
		}
		for _, key := range validUTF8 {
			must(t, fx.store.Put(ctx, account{ID: key, Email: key + "@example.com"}))
			if got, err := fx.store.Get(ctx, key); err != nil || got.ID != key {
				t.Fatalf("Get(%q) = %+v, %v", key, got, err)
			}
		}
	})
}

// TestAStoreWithoutIndexesRefusesAnInvalidKey pins the refusal on a store
// with no index at all, where only the store key is checked.
func TestAStoreWithoutIndexesRefusesAnInvalidKey(t *testing.T) {
	t.Parallel()
	store, err := docstore.Open(docstore.Config[account]{Key: accountKey})
	must(t, err)
	t.Cleanup(func() { _ = store.Close() })
	refusedAsUnencodable(t, store.Put(account{ID: "k\xff", Email: "\xfe"}), "", "k\xff", "Put")
	must(t, store.Put(account{ID: "k", Email: "\xfe"}))
}
