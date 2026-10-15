// Package docstore_test — a document written with its versions as they are
// given (ADR 0178), one contract run on every engine: the memory store, the
// file store, and the SQL store on each dialect's statements over the fake
// engine; and the file store's write read back after a reopen.
package docstore_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// writerEngine is one engine's accounts store seen through the calls the
// PutDocument contract reads, with the SQL store's context supplied.
type writerEngine struct {
	putDocument func(key string, versions []coredocstore.VersionValue, trashedAt time.Time) error
	versions    func(key string) ([]coredocstore.VersionValue, error)
	get         func(key string) (account, error)
	lookup      func(index, key string) (account, error)
	insert      func(v account) error
	put         func(v account) error
	trash       func(key string) error
	restore     func(key string) error
	tombstones  func() ([]coredocstore.TombstoneValue, error)
	onWrite     func(fn func(string)) func()
}

// fileWriter asserts the file engine's VersionWriter sibling, by type
// assertion as a caller holding the port reaches it.
func fileWriter(t *testing.T, store *docstore.Store[account]) coredocstore.VersionWriter {
	t.Helper()
	writer, ok := any(store).(coredocstore.VersionWriter)
	if !ok {
		t.Fatalf("%T does not implement docstore.VersionWriter", store)
	}
	return writer
}

// sqlWriter asserts the SQL engine's VersionWriterContext sibling.
func sqlWriter(t *testing.T, store *docstore.SQLStore[account]) coredocstore.VersionWriterContext {
	t.Helper()
	writer, ok := any(store).(coredocstore.VersionWriterContext)
	if !ok {
		t.Fatalf("%T does not implement docstore.VersionWriterContext", store)
	}
	return writer
}

// fileWriterEngine is the file engine's accounts store.
func fileWriterEngine(t *testing.T, store *docstore.Store[account]) writerEngine {
	t.Helper()
	writer := fileWriter(t, store)
	return writerEngine{
		putDocument: writer.PutDocument, versions: store.Versions, get: store.Get, lookup: store.Lookup,
		insert: store.Insert, put: store.Put, trash: store.Trash, restore: store.Restore,
		tombstones: store.Tombstones, onWrite: store.OnWrite,
	}
}

// sqlWriterEngine is the SQL engine's accounts store, every call under ctx.
func sqlWriterEngine(t *testing.T, store *docstore.SQLStore[account]) writerEngine {
	t.Helper()
	ctx, writer := t.Context(), sqlWriter(t, store)
	return writerEngine{
		putDocument: func(key string, versions []coredocstore.VersionValue, trashedAt time.Time) error {
			return writer.PutDocument(ctx, key, versions, trashedAt)
		},
		versions:   func(key string) ([]coredocstore.VersionValue, error) { return store.Versions(ctx, key) },
		get:        func(key string) (account, error) { return store.Get(ctx, key) },
		lookup:     func(index, key string) (account, error) { return store.Lookup(ctx, index, key) },
		insert:     func(v account) error { return store.Insert(ctx, v) },
		put:        func(v account) error { return store.Put(ctx, v) },
		trash:      func(key string) error { return store.Trash(ctx, key) },
		restore:    func(key string) error { return store.Restore(ctx, key) },
		tombstones: func() ([]coredocstore.TombstoneValue, error) { return store.Tombstones(ctx) },
		onWrite:    store.OnWrite,
	}
}

// eachWriterEngine runs fn over every engine, each keeping versions former
// versions on a clock of its own starting at epoch.
func eachWriterEngine(t *testing.T, versions int, fn func(t *testing.T, e writerEngine, clk *clock.ManualClock)) {
	t.Helper()
	eachBackend(t, func(t *testing.T, fsys corevfs.FullFS) {
		clk := clock.NewManualClock(epoch)
		fn(t, fileWriterEngine(t, openVersioned(t, versionedConfig(fsys, versions, clk))), clk)
	})
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		clk := clock.NewManualClock(epoch)
		fn(t, sqlWriterEngine(t, openSQLVersioned(t, dialect, keeping(versions, clk)).store), clk)
	})
}

// accountJSON is a as json.Marshal writes it.
func accountJSON(t *testing.T, a account) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(a)
	must(t, err)
	return raw
}

// copiedHistory is a document's versions as another store kept them: numbers
// with gaps a pruning and a rewrite left, instants and metadata of their own,
// and a version 1 made before that store kept versions, at no instant.
func copiedHistory(t *testing.T) []coredocstore.VersionValue {
	t.Helper()
	return []coredocstore.VersionValue{
		{
			Number: 9, At: epoch.Add(-time.Hour), Meta: map[string]string{"by": "ada", "command": "rename"},
			JSON: accountJSON(t, account{ID: "acc_1", Name: "v9", Email: "ada@x.dev", Teams: []string{"red"}}),
		},
		{Number: 7, At: epoch.Add(-2*time.Hour + 123), JSON: accountJSON(t, account{ID: "acc_1", Name: "v7 <&>"})},
		{
			Number: 4, At: epoch.Add(-3 * time.Hour), Meta: map[string]string{"by": "bob"},
			JSON: json.RawMessage(`{ "id": "acc_1",  "name": "v4", "legacy": [1, 2] }`),
		},
		{Number: 1, JSON: accountJSON(t, account{ID: "acc_1", Name: "v1"})},
	}
}

// sameAsGiven fails the test unless got holds want's versions as a store
// keeps them: the same numbers, instants, metadata, and JSON compacted.
func sameAsGiven(t *testing.T, what string, got, want []coredocstore.VersionValue) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d versions, want %d: %+v", what, len(got), len(want), got)
	}
	for i := range want {
		compact := compacted(t, want[i].JSON)
		if got[i].Number != want[i].Number || !got[i].At.Equal(want[i].At) || !maps.Equal(got[i].Meta, want[i].Meta) ||
			string(got[i].JSON) != string(compact) {
			t.Fatalf("%s: version %d is\n%+v\nwant\n%+v (JSON %s)", what, i, got[i], want[i], compact)
		}
	}
}

// compacted is raw as json.Marshal writes a document: compact, HTML-escaped.
func compacted(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var compact, escaped bytes.Buffer
	must(t, json.Compact(&compact, raw))
	json.HTMLEscape(&escaped, compact.Bytes())
	return escaped.String()
}

// TestPutDocumentWritesTheVersionsAsTheyAreGiven pins PutDocument on every
// engine: the versions read back are those given — numbers with their gaps,
// instants to the nanosecond, the unknown instant of a version 1, metadata,
// JSON compacted —, the document is stored and filed, OnWrite is told, and
// the next write continues the numbering and prunes as every write does.
func TestPutDocumentWritesTheVersionsAsTheyAreGiven(t *testing.T) {
	t.Parallel()
	eachWriterEngine(t, 2, func(t *testing.T, e writerEngine, clk *clock.ManualClock) {
		var mu sync.Mutex
		var written []string
		e.onWrite(func(key string) { mu.Lock(); written = append(written, key); mu.Unlock() })
		given := copiedHistory(t)
		must(t, e.putDocument("acc_1", given, time.Time{}))

		sameAsGiven(t, "Versions after PutDocument", mustVersions(t, e, "acc_1"), given)
		if a, err := e.get("acc_1"); err != nil || a.Name != "v9" {
			t.Fatalf("Get() = %+v, %v; want the current version", a, err)
		}
		if a, err := e.lookup("email", "ada@x.dev"); err != nil || a.ID != "acc_1" {
			t.Fatalf("Lookup(email) = %+v, %v; want the document filed", a, err)
		}
		mu.Lock()
		if !slices.Equal(written, []string{"acc_1"}) {
			t.Errorf("OnWrite was told %v", written)
		}
		mu.Unlock()

		clk.Advance(time.Minute)
		must(t, e.put(account{ID: "acc_1", Name: "v10"}))
		if got := numbers(mustVersions(t, e, "acc_1")); !slices.Equal(got, []uint64{10, 9, 7}) {
			t.Fatalf("the next write left versions %v, want [10 9 7]", got)
		}
	})
}

// TestPutDocumentWritesATrashedDocument pins a document written into the
// trash: gone from every read, its unique keys free, listed by Tombstones at
// the instant given, its versions those given, and Restore brings it back.
func TestPutDocumentWritesATrashedDocument(t *testing.T) {
	t.Parallel()
	eachWriterEngine(t, 5, func(t *testing.T, e writerEngine, _ *clock.ManualClock) {
		var mu sync.Mutex
		var written []string
		e.onWrite(func(key string) { mu.Lock(); written = append(written, key); mu.Unlock() })
		given := copiedHistory(t)
		trashedAt := epoch.Add(-30*time.Minute + 7)
		must(t, e.putDocument("acc_1", given, trashedAt))

		_, err := e.get("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Get of a document written into the trash")
		_, err = e.lookup("email", "ada@x.dev")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Lookup of a trashed document's e-mail")
		tombs, err := e.tombstones()
		must(t, err)
		if len(tombs) != 1 || tombs[0].Key != "acc_1" || !tombs[0].At.Equal(trashedAt) {
			t.Fatalf("Tombstones() = %+v, want acc_1 trashed at %v", tombs, trashedAt)
		}
		sameAsGiven(t, "Versions of a trashed document", mustVersions(t, e, "acc_1"), given)
		mu.Lock()
		if len(written) != 0 {
			t.Errorf("OnWrite was told %v for a trashed document", written)
		}
		mu.Unlock()

		must(t, e.insert(account{ID: "acc_2", Email: "ada@x.dev"}))
		requireCode(t, e.restore("acc_1"), coredocstore.CodeUniqueKeyTaken, "Restore while its e-mail is taken")
		must(t, e.put(account{ID: "acc_2"}))
		must(t, e.restore("acc_1"))
		if a, err := e.get("acc_1"); err != nil || a.Name != "v9" {
			t.Fatalf("Get() after Restore = %+v, %v", a, err)
		}
		sameAsGiven(t, "Versions after Restore", mustVersions(t, e, "acc_1"), given)
	})
}

// TestPutDocumentRefusesWhatCannotBeWrittenAsGiven pins every refusal on
// every engine, each naming its problem, and that a refusal writes nothing.
func TestPutDocumentRefusesWhatCannotBeWrittenAsGiven(t *testing.T) {
	t.Parallel()
	eachWriterEngine(t, 5, func(t *testing.T, e writerEngine, _ *clock.ManualClock) {
		valid := func() []coredocstore.VersionValue { return copiedHistory(t) }
		invalid := []struct {
			change  func([]coredocstore.VersionValue) []coredocstore.VersionValue
			problem string
		}{
			{problem: "no version", change: func([]coredocstore.VersionValue) []coredocstore.VersionValue { return nil }},
			{problem: "a version numbered 0", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue { v[3].Number = 0; return v }},
			{problem: "numbers that do not strictly decrease from the newest", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue { v[2].Number = 7; return v }},
			{problem: "numbers that do not strictly decrease from the newest", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue { v[1].Number = 10; return v }},
			{problem: "an unknown instant on a version other than 1", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue { v[1].At = time.Time{}; return v }},
			{problem: "a version that is not JSON", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue {
				v[2].JSON = json.RawMessage(`{"id":`)
				return v
			}},
			{problem: "a version that is not JSON", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue { v[0].JSON = nil; return v }},
			{problem: "a current document whose own key is another", change: func(v []coredocstore.VersionValue) []coredocstore.VersionValue {
				v[0].JSON = accountJSON(t, account{ID: "acc_9"})
				return v
			}},
		}
		for _, testCase := range invalid {
			err := e.putDocument("acc_1", testCase.change(valid()), time.Time{})
			requireCode(t, err, coredocstore.CodeVersionsInvalid, "PutDocument with "+testCase.problem)
			if got := problemOf(err); got != testCase.problem {
				t.Errorf("PutDocument with %s named the problem %q", testCase.problem, got)
			}
		}
		requireCode(t, e.putDocument("", valid(), time.Time{}), coredocstore.CodeDocumentKeyEmpty, "PutDocument with no key")
		undecodable := valid()
		undecodable[0].JSON = json.RawMessage(`{"id":5}`)
		requireCode(t, e.putDocument("acc_1", undecodable, time.Time{}), coredocstore.CodeDocumentUndecodable, "PutDocument of an undecodable document")
		_, err := e.versions("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions after refused writes")

		must(t, e.insert(account{ID: "stored"}))
		must(t, e.insert(account{ID: "trashed"}))
		must(t, e.trash("trashed"))
		must(t, e.insert(account{ID: "holder", Email: "ada@x.dev"}))
		for _, key := range []string{"stored", "trashed"} {
			taken := []coredocstore.VersionValue{{Number: 1, At: epoch, JSON: accountJSON(t, account{ID: key, Name: "copy"})}}
			requireCode(t, e.putDocument(key, taken, time.Time{}), coredocstore.CodeDocumentExists, "PutDocument over "+key)
		}
		requireCode(t, e.putDocument("acc_1", valid(), time.Time{}), coredocstore.CodeUniqueKeyTaken, "PutDocument of a taken e-mail")
		_, err = e.versions("acc_1")
		requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions after a refused unique key")
		must(t, e.putDocument("acc_1", valid(), epoch))
	})
}

// problemOf is err's "problem" field.
func problemOf(err error) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == "problem" {
			return field.StringValue()
		}
	}
	return ""
}

// mustVersions reads key's versions through e, failing the test on an error.
func mustVersions(t *testing.T, e writerEngine, key string) []coredocstore.VersionValue {
	t.Helper()
	all, err := e.versions(key)
	if err != nil {
		t.Fatalf("Versions(%s) = %v", key, err)
	}
	return all
}

// TestPutDocumentNeedsAStoreThatKeepsVersions pins VERSIONS_NOT_KEPT on both
// engines for a store keeping none.
func TestPutDocumentNeedsAStoreThatKeepsVersions(t *testing.T) {
	t.Parallel()
	given := copiedHistory(t)
	file := openVersioned(t, versionedConfig(nil, 0, nil))
	requireCode(t, fileWriter(t, file).PutDocument("acc_1", given, time.Time{}), coredocstore.CodeVersionsNotKept, "the file engine keeping none")
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		store := openSQLVersioned(t, dialect).store
		requireCode(t, sqlWriter(t, store).PutDocument(t.Context(), "acc_1", given, time.Time{}), coredocstore.CodeVersionsNotKept, "the SQL engine keeping none")
	})
}

// TestAWrittenDocumentSurvivesAReopenOfTheFileStore pins the file engine's
// durability: what PutDocument wrote — a stored document and a trashed one —
// reads back the same from the files, before and after a fold.
func TestAWrittenDocumentSurvivesAReopenOfTheFileStore(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	given := copiedHistory(t)
	trashed := []coredocstore.VersionValue{{Number: 3, At: epoch, JSON: accountJSON(t, account{ID: "acc_2", Name: "gone"})}}
	trashedAt := epoch.Add(time.Second)
	cfg := versionedConfig(fsys, 2, nil)
	cfg.FoldAt = -1
	store, err := openWith(cfg)
	must(t, err)
	must(t, fileWriter(t, store).PutDocument("acc_1", given, time.Time{}))
	must(t, fileWriter(t, store).PutDocument("acc_2", trashed, trashedAt))
	for _, step := range []string{"replayed from the overlay", "read from the folded files"} {
		if step == "read from the folded files" {
			must(t, store.Fold())
		}
		must(t, store.Close())
		store, err = openWith(cfg)
		must(t, err)
		sameAsGiven(t, step, versionsOf(t, store, "acc_1"), given)
		sameAsGiven(t, step, versionsOf(t, store, "acc_2"), trashed)
		tombs, tombErr := store.Tombstones()
		must(t, tombErr)
		if len(tombs) != 1 || tombs[0].Key != "acc_2" || !tombs[0].At.Equal(trashedAt) {
			t.Fatalf("%s: Tombstones() = %+v", step, tombs)
		}
	}
	must(t, store.Close())
}
