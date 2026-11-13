// Package docstore_test — the executable form of "two engines that keep one
// contract" (#1087, ADR 0110, ADR 0139, ADR 0143): one table of rows, each run
// on every store everyStore returns — memory, a filesystem, and the SQL store
// on each dialect's statements over the fake engine — the queue domain's
// shape (everyBroker). A row says what every engine does; what only one
// engine does — the file engine's overlay, the SQL row numbering — stays in
// that engine's own tests.
package docstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// storeConfig is what a row asks of the store it opens, in terms every engine
// takes: how many former versions, the clock they are stamped on, the hold —
// asked with the key, the SQL engine's context dropped — and, on the file
// engine alone, how many overlay entries fold.
type storeConfig struct {
	clock    clock.Clock
	held     func(key string) bool
	versions int
	foldAt   int
	// changes is the change feed, with the documents (#831); nil writes none.
	changes corestream.Stream
	// ordered adds the ordered index "since" over an account's Name to the
	// accounts indexes, for the paged reads' rows (#836).
	ordered bool
}

// indexesFor are the indexes a row's store opens with: the accounts
// indexes, and the ordered one when the row asks for it.
func indexesFor(cfg storeConfig) []coredocstore.IndexSpec[account] {
	indexes := accountIndexes()
	//: the ordered index the range rows read.
	if cfg.ordered {
		indexes = append(indexes, coredocstore.Ordered("since", func(a account) string { return a.Name }))
	}
	return indexes
}

// storeFactory opens one engine's accounts store, and reports the refusal of
// a configuration no store of that engine opens.
type storeFactory struct {
	open    func(t *testing.T, cfg storeConfig) storeEngine
	refuse  func(t *testing.T, cfg storeConfig) error
	name    string
	sqlOnly bool
}

// keeping opens f's store keeping versions former versions on a manual clock
// starting at epoch, and returns both.
func (f storeFactory) keeping(t *testing.T, versions int) (storeEngine, *clock.ManualClock) {
	t.Helper()
	clk := clock.NewManualClock(epoch)
	return f.open(t, storeConfig{versions: versions, clock: clk}), clk
}

// fileStoreConfig is the file engine's configuration of cfg over fsys.
func fileStoreConfig(fsys func() docstore.Config[account], cfg storeConfig) docstore.Config[account] {
	c := fsys()
	c.Versions, c.Clock, c.Held = cfg.versions, cfg.clock, cfg.held
	c.Changes, c.ChangesWithDocument = cfg.changes, cfg.changes != nil
	//: a fold threshold only where a row asks for one.
	if cfg.foldAt != 0 {
		c.FoldAt = cfg.foldAt
	}
	return c
}

// sqlStoreConfig configures a SQL store as cfg says.
func sqlStoreConfig(cfg storeConfig) func(*docstore.SQLConfig[account]) {
	return func(c *docstore.SQLConfig[account]) {
		c.Versions, c.Clock = cfg.versions, cfg.clock
		c.Changes, c.ChangesWithDocument = cfg.changes, cfg.changes != nil
		//: the hold asked with the key; its context is the SQL engine's own.
		if cfg.held != nil {
			c.Held = func(_ context.Context, key string) bool { return cfg.held(key) }
		}
	}
}

// everyStore returns a factory per engine: memory, a filesystem in memory,
// and the SQL store on each dialect over the fake engine.
func everyStore() []storeFactory {
	out := []storeFactory{}
	for name, backing := range map[string]func() docstore.Config[account]{
		"memory":     func() docstore.Config[account] { return accountConfig(nil) },
		"filesystem": func() docstore.Config[account] { return accountConfig(memFS()) },
	} {
		out = append(out, storeFactory{
			name: name,
			open: func(t *testing.T, cfg storeConfig) storeEngine {
				t.Helper()
				return fileStoreEngine(t, openIndexed(t, fileStoreConfig(backing, cfg), indexesFor(cfg)))
			},
			refuse: func(_ *testing.T, cfg storeConfig) error {
				_, err := openWith(fileStoreConfig(backing, cfg))
				return err
			},
		})
	}
	for _, dialect := range sqlDialects {
		out = append(out, storeFactory{
			name:    "sql-" + dialect.String(),
			sqlOnly: true,
			open: func(t *testing.T, cfg storeConfig) storeEngine {
				t.Helper()
				engine := newSQLEngine(dialect, accountsTable)
				c := docstore.SQLConfig[account]{
					Key: accountKey, Transactor: newSQLTransactor(t, engine, dialect), Dialect: dialect, Table: accountsTable,
				}
				sqlStoreConfig(cfg)(&c)
				store, err := docstore.OpenSQL(c, indexesFor(cfg)...)
				if err != nil {
					t.Fatalf("OpenSQL() = %v", err)
				}
				return sqlStoreEngine(t.Context(), store)
			},
			refuse: func(t *testing.T, cfg storeConfig) error {
				engine := newSQLEngine(dialect, accountsTable)
				c := docstore.SQLConfig[account]{
					Key: accountKey, Transactor: newSQLTransactor(t, engine, dialect), Dialect: dialect, Table: accountsTable,
				}
				sqlStoreConfig(cfg)(&c)
				_, err := docstore.OpenSQL(c, accountIndexes()...)
				return err
			},
		})
	}
	slices.SortFunc(out, func(a, b storeFactory) int { return strings.Compare(a.name, b.name) })
	return out
}

// conformanceRow is one behaviour every engine keeps.
type conformanceRow struct {
	run  func(t *testing.T, f storeFactory)
	name string
}

// withVersions adapts a row written over an opened engine: the store keeps
// versions former versions on a clock starting at epoch.
func withVersions(versions int, fn func(t *testing.T, e storeEngine, clk *clock.ManualClock)) func(t *testing.T, f storeFactory) {
	return func(t *testing.T, f storeFactory) {
		e, clk := f.keeping(t, versions)
		fn(t, e, clk)
	}
}

// conformanceRows is the table: the versions (ADR 0143), then the trash (ADR
// 0143, amended for ADR 0178).
func conformanceRows() []conformanceRow {
	return []conformanceRow{
		{name: "VersionsAreNumberedStampedAndPruned", run: withVersions(2, versionsAreNumberedStampedAndPruned)},
		{name: "AWriteThatChangesNothingMakesNoVersion", run: withVersions(5, aWriteThatChangesNothingMakesNoVersion)},
		{name: "VersionsAreNeverIndexed", run: withVersions(3, versionsAreNeverIndexed)},
		{name: "ADeletionTakesTheVersions", run: withVersions(3, aDeletionTakesTheVersions)},
		{name: "AHeldDocumentKeepsItsVersions", run: aHeldDocumentKeepsItsVersions},
		{name: "RewriteVersions", run: withVersions(5, rewriteVersions)},
		{name: "AStoreWithoutVersions", run: withVersions(0, aStoreWithoutVersions)},
		{name: "VersionsConfigurationRefusals", run: versionsConfigurationRefusals},
		{name: "VersionsUnderContention", run: versionsUnderContention},
		{name: "ATrashedDocumentLeavesEveryReadAndKeepsItsVersions", run: withVersions(3, aTrashedDocumentLeavesEveryReadAndKeepsItsVersions)},
		{name: "RestoreBringsATrashedDocumentBackAsItWas", run: withVersions(3, restoreBringsATrashedDocumentBackAsItWas)},
		{name: "PurgeEmptiesTheTrash", run: withVersions(3, purgeEmptiesTheTrash)},
		{name: "ATrashedDocumentFreesItsUniqueKeys", run: withVersions(3, aTrashedDocumentFreesItsUniqueKeys)},
		{name: "AWriteUnderATrashedKeyContinuesItsVersions", run: withVersions(3, aWriteUnderATrashedKeyContinuesItsVersions)},
		{name: "DeleteNeverReachesTheTrash", run: withVersions(3, deleteNeverReachesTheTrash)},
		{name: "AStoreWithoutVersionsHasNoTrash", run: withVersions(0, aStoreWithoutVersionsHasNoTrash)},
		{name: "TombstonesAreInKeyOrder", run: withVersions(1, tombstonesAreInKeyOrder)},
		{name: "DocumentsVisitsEachDocumentOnce", run: ordered(documentsVisitsEachDocumentOnce)},
		{name: "DocumentsResumesAndStops", run: ordered(documentsResumesAndStops)},
		{name: "FindAfterPagesFind", run: ordered(findAfterPagesFind)},
		{name: "RangeReadsAnOrderedIndexBetweenTwoKeys", run: ordered(rangeReadsAnOrderedIndexBetweenTwoKeys)},
		{name: "RangeFollowsEveryWrite", run: ordered(rangeFollowsEveryWrite)},
		{name: "PagedReadsRefusals", run: ordered(pagedReadsRefusals)},
		{name: "EveryWriteAppendsOneChangeRecord", run: everyWriteAppendsOneChangeRecord},
	}
}

// TestConformance runs every row on every store: one contract, proved once
// per engine rather than written twice by hand.
func TestConformance(t *testing.T) {
	t.Parallel()
	for _, row := range conformanceRows() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			for _, f := range everyStore() {
				t.Run(f.name, func(t *testing.T) {
					t.Parallel()
					row.run(t, f)
				})
			}
		})
	}
}

// versionsAreNumberedStampedAndPruned pins what a write leaves: a creation is
// version 1, every write that changes the document makes the next one — with
// the instant of the store's clock and the stamp's metadata — and the oldest
// beyond Versions are pruned, newest first, the document itself always the
// newest.
func versionsAreNumberedStampedAndPruned(t *testing.T, e storeEngine, clk *clock.ManualClock) {
	must(t, e.insertStamped(account{ID: "acc_1", Name: "v1"}, coredocstore.StampValue{Meta: map[string]string{"by": "ada"}}))
	clk.Advance(time.Minute)
	must(t, e.put(account{ID: "acc_1", Name: "v2"}))
	clk.Advance(time.Minute)
	_, err := e.updateStamped("acc_1", coredocstore.StampValue{Meta: map[string]string{"by": "grace", "command": "rename"}},
		func(a *account) error { a.Name = "v3"; return nil })
	must(t, err)
	clk.Advance(time.Minute)
	must(t, e.replaceStamped(account{ID: "acc_1", Name: "v4"}, coredocstore.StampValue{Meta: map[string]string{"by": "alan"}}))

	all := engineVersionsOf(t, e, "acc_1")
	if got := numbers(all); !slices.Equal(got, []uint64{4, 3, 2}) {
		t.Fatalf("numbers = %v, want the current 4 and the two newest former ones", got)
	}
	if got := namesIn(t, all); !slices.Equal(got, []string{"v4", "v3", "v2"}) {
		t.Fatalf("the versions hold %v", got)
	}
	for i, want := range []time.Time{epoch.Add(3 * time.Minute), epoch.Add(2 * time.Minute), epoch.Add(time.Minute)} {
		if !all[i].At.Equal(want) || all[i].At.Location() != time.UTC {
			t.Fatalf("version %d was made at %v, want %v in UTC", all[i].Number, all[i].At, want)
		}
	}
	if !maps.Equal(all[0].Meta, map[string]string{"by": "alan"}) ||
		!maps.Equal(all[1].Meta, map[string]string{"by": "grace", "command": "rename"}) || all[2].Meta != nil {
		t.Fatalf("the stamps are %v, %v, %v", all[0].Meta, all[1].Meta, all[2].Meta)
	}
	//: the current version is the document.
	if doc, getErr := e.get("acc_1"); getErr != nil || doc.Name != "v4" {
		t.Fatalf("Get() = %+v, %v", doc, getErr)
	}
	one, err := e.version("acc_1", 3)
	if err != nil || one.Number != 3 || !strings.Contains(string(one.JSON), `"v3"`) {
		t.Fatalf("Version(3) = %+v, %v", one, err)
	}
	_, err = e.version("acc_1", 1)
	requireCode(t, err, coredocstore.CodeVersionNotFound, "a pruned version")
	quotesNothing(t, err, "acc_1")
	_, err = e.version("acc_404", 1)
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "a version of no document")
	_, err = e.versions("acc_404")
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "the versions of no document")
	//: a read is a copy.
	all[0].JSON[0], all[0].Meta["by"] = '[', "mallory"
	if again := engineVersionsOf(t, e, "acc_1"); again[0].JSON[0] != '{' || again[0].Meta["by"] != "alan" {
		t.Fatalf("a read aliased the store: %s %v", again[0].JSON, again[0].Meta)
	}
}

// aWriteThatChangesNothingMakesNoVersion pins the writes that keep the
// current version: one storing the JSON already stored, and one stamped
// InPlace — Replace's and Put's — which changes the document and keeps the
// version's number, instant and metadata. A creation is version 1 even
// stamped InPlace.
func aWriteThatChangesNothingMakesNoVersion(t *testing.T, e storeEngine, clk *clock.ManualClock) {
	must(t, e.insertStamped(account{ID: "acc_1", Name: "draft"}, coredocstore.StampValue{InPlace: true, Meta: map[string]string{"by": "ada"}}))
	clk.Advance(time.Hour)
	must(t, e.put(account{ID: "acc_1", Name: "draft"}))
	must(t, e.update("acc_1", func(*account) error { return nil }))
	must(t, e.replaceStamped(account{ID: "acc_1", Name: "published"}, coredocstore.StampValue{InPlace: true, Meta: map[string]string{"by": "workflow"}}))
	must(t, e.putStamped(account{ID: "acc_1", Name: "republished"}, coredocstore.StampValue{InPlace: true}))
	all := engineVersionsOf(t, e, "acc_1")
	if len(all) != 1 || all[0].Number != 1 || !all[0].At.Equal(epoch) || all[0].Meta["by"] != "ada" {
		t.Fatalf("versions = %+v, want version 1 as the creation made it", all)
	}
	if got := namesIn(t, all); !slices.Equal(got, []string{"republished"}) {
		t.Fatalf("the current version holds %v, want the document as the in-place writes left it", got)
	}
	must(t, e.put(account{ID: "acc_1", Name: "edited"}))
	if got := namesIn(t, engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []string{"edited", "republished"}) {
		t.Fatalf("after an edit the versions hold %v", got)
	}
}

// versionsAreNeverIndexed pins that Lookup and Find read the current version
// only: an e-mail a former version holds files nothing, and another document
// may take it.
func versionsAreNeverIndexed(t *testing.T, e storeEngine, _ *clock.ManualClock) {
	must(t, e.put(account{ID: "acc_1", Email: "old@x.dev", Teams: []string{"red"}}))
	must(t, e.put(account{ID: "acc_1", Email: "new@x.dev", Teams: []string{"blue"}}))
	_, err := e.lookup("email", "old@x.dev")
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "Lookup of a former version's e-mail")
	if red, findErr := e.find("team", "red"); findErr != nil || len(red) != 0 {
		t.Fatalf("Find of a former version's team = %v, %v", ids(red), findErr)
	}
	must(t, e.insert(account{ID: "acc_2", Email: "old@x.dev"}))
}

// aDeletionTakesTheVersions pins that a deleted document leaves no version,
// and that one inserted again under its key is a new document, numbered from
// 1.
func aDeletionTakesTheVersions(t *testing.T, e storeEngine, _ *clock.ManualClock) {
	must(t, e.put(account{ID: "acc_1", Name: "a"}))
	must(t, e.put(account{ID: "acc_1", Name: "b"}))
	must(t, e.del("acc_1"))
	_, err := e.versions("acc_1")
	requireCode(t, err, coredocstore.CodeDocumentNotFound, "Versions of a deleted document")
	must(t, e.insert(account{ID: "acc_1", Name: "again"}))
	if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("a document inserted again has versions %v, want 1", got)
	}
}

// aHeldDocumentKeepsItsVersions pins the legal hold: while Held says so, no
// write prunes, and the versions pile up beyond Versions; the first write
// after the release prunes them — even one that makes no version. Held is
// asked only by a write that would prune.
func aHeldDocumentKeepsItsVersions(t *testing.T, f storeFactory) {
	var mu sync.Mutex
	held, asked := map[string]bool{"acc_1": true}, 0
	e := f.open(t, storeConfig{versions: 1, held: func(key string) bool {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return held[key]
	}})
	must(t, e.put(account{ID: "acc_1", Name: "v1"}))
	must(t, e.put(account{ID: "acc_1", Name: "v2"}))
	mu.Lock()
	askedBefore := asked
	mu.Unlock()
	if askedBefore != 0 {
		t.Fatalf("Held was asked %d times with nothing to prune", askedBefore)
	}
	for _, name := range []string{"v3", "v4", "v5"} {
		must(t, e.put(account{ID: "acc_1", Name: name}))
	}
	if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{5, 4, 3, 2, 1}) {
		t.Fatalf("a held document keeps %v, want every version", got)
	}
	//: the hold is lifted; a write that makes no version prunes all the same.
	mu.Lock()
	held["acc_1"] = false
	mu.Unlock()
	must(t, e.put(account{ID: "acc_1", Name: "v5"}))
	if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{5, 4}) {
		t.Fatalf("after the release the document keeps %v, want 5 and 4", got)
	}
}

// rewriteVersions pins the erasure's rewrite: the former versions, newest
// first, cleared or dropped by the caller's function in one durable write that
// calls no hook — the current version untouched, each kept version's instant
// kept — and every result a rewrite may not return refused, changing nothing.
func rewriteVersions(t *testing.T, e storeEngine, _ *clock.ManualClock) {
	for _, email := range []string{"a@x.dev", "b@x.dev", "c@x.dev", "d@x.dev"} {
		must(t, e.put(account{ID: "acc_1", Name: "Ada", Email: email}))
	}
	var mu sync.Mutex
	hooked := 0
	e.onWrite(func(string) { mu.Lock(); hooked++; mu.Unlock() })
	must(t, e.rewrite("acc_1", func(former []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) {
		if got := numbers(former); !slices.Equal(got, []uint64{3, 2, 1}) {
			t.Errorf("the rewrite was given %v, want the former versions newest first", got)
		}
		kept := []coredocstore.VersionValue{former[0], former[2]}
		for i := range kept {
			kept[i].JSON = json.RawMessage(`{"id": "acc_1", "name": "[erased]"}`)
			kept[i].Meta = map[string]string{"erased": "yes"}
		}
		return kept, nil
	}))
	all := engineVersionsOf(t, e, "acc_1")
	if got := numbers(all); !slices.Equal(got, []uint64{4, 3, 1}) {
		t.Fatalf("after the rewrite the versions are %v, want 4, 3 and 1", got)
	}
	if got := namesIn(t, all); !slices.Equal(got, []string{"Ada", "[erased]", "[erased]"}) {
		t.Fatalf("after the rewrite the versions hold %v", got)
	}
	if string(all[1].JSON) != `{"id":"acc_1","name":"[erased]"}` || all[1].Meta["erased"] != "yes" || !all[1].At.Equal(epoch) {
		t.Fatalf("a rewritten version = %s %v %v, want its JSON compact, its metadata as rewritten, its instant kept",
			all[1].JSON, all[1].Meta, all[1].At)
	}
	mu.Lock()
	if hooked != 0 {
		t.Fatalf("a rewrite called OnWrite %d times", hooked)
	}
	mu.Unlock()
	refusals := map[string]func([]coredocstore.VersionValue) []coredocstore.VersionValue{
		"a version it was not given": func(f []coredocstore.VersionValue) []coredocstore.VersionValue {
			f[0].Number = 7
			return f
		},
		"out of order": func(f []coredocstore.VersionValue) []coredocstore.VersionValue {
			return []coredocstore.VersionValue{f[1], f[0]}
		},
		"another instant": func(f []coredocstore.VersionValue) []coredocstore.VersionValue {
			f[1].At = f[1].At.Add(time.Second)
			return f
		},
		"not JSON": func(f []coredocstore.VersionValue) []coredocstore.VersionValue {
			f[0].JSON = json.RawMessage(`{"id": "s3cr3t`)
			return f
		},
	}
	for name, rewrite := range refusals {
		err := e.rewrite("acc_1", func(f []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return rewrite(f), nil })
		requireCode(t, err, coredocstore.CodeVersionsRewriteRefused, name)
		quotesNothing(t, err, "s3cr3t", "acc_1")
	}
	refusal := errors.New("the caller's own refusal")
	if err := e.rewrite("acc_1", func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return nil, refusal }); !errors.Is(err, refusal) {
		t.Fatalf("a failing rewrite = %v, want the function's own error", err)
	}
	if got := numbers(engineVersionsOf(t, e, "acc_1")); !slices.Equal(got, []uint64{4, 3, 1}) {
		t.Fatalf("a refused rewrite changed the versions: %v", got)
	}
	requireCode(t, e.rewrite("acc_404", func(f []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return f, nil }),
		coredocstore.CodeDocumentNotFound, "a rewrite of no document")
}

// aStoreWithoutVersions pins the zero value: a store that keeps no versions
// refuses every read and rewrite of them and accepts stamped writes as plain
// ones. What it writes — the file engine's entry, the SQL engine's statements
// — is each engine's own test.
func aStoreWithoutVersions(t *testing.T, e storeEngine, _ *clock.ManualClock) {
	must(t, e.putStamped(account{ID: "acc_1", Name: "Ada"}, coredocstore.StampValue{Meta: map[string]string{"by": "ada"}}))
	if got, err := e.get("acc_1"); err != nil || got.Name != "Ada" {
		t.Fatalf("Get() after a stamped write = %+v, %v", got, err)
	}
	_, err := e.versions("acc_1")
	requireCode(t, err, coredocstore.CodeVersionsNotKept, "Versions")
	_, err = e.version("acc_1", 1)
	requireCode(t, err, coredocstore.CodeVersionsNotKept, "Version")
	err = e.rewrite("acc_1", func(f []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return f, nil })
	requireCode(t, err, coredocstore.CodeVersionsNotKept, "RewriteVersions")
}

// versionsConfigurationRefusals pins the two settings refused before anything
// is read: a negative Versions, and a Held on a store that keeps no versions,
// which would hold nothing.
func versionsConfigurationRefusals(t *testing.T, f storeFactory) {
	err := f.refuse(t, storeConfig{versions: -1})
	requireCode(t, err, coredocstore.CodeStoreMisconfigured, "a negative Versions")
	err = f.refuse(t, storeConfig{held: func(string) bool { return true }})
	requireCode(t, err, coredocstore.CodeStoreMisconfigured, "Held without Versions")
	if setting := fieldValue(errs.FieldsOf(err), "setting"); setting != "Held" {
		t.Fatalf("the refusal names %q, want Held", setting)
	}
}

// versionsUnderContention pins the versions under the race detector: writers
// stamping, updating, rewriting and deleting shared documents while readers
// read their versions. Whatever the interleaving, every document ends with its
// current version first, numbers strictly decreasing, and no more former
// versions than the store keeps — the file engine folding every eight entries
// meanwhile.
func versionsUnderContention(t *testing.T, f storeFactory) {
	e := f.open(t, storeConfig{versions: 3, foldAt: 8})
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 40 {
				key := fmt.Sprintf("acc_%d", i%4)
				stamp := coredocstore.StampValue{Meta: map[string]string{"by": strconv.Itoa(worker)}}
				tolerate(t, e.putStamped(account{ID: key, Name: fmt.Sprintf("w%d-%d", worker, i)}, stamp))
				_, err := e.updateStamped(key, stamp, func(a *account) error { a.Teams = []string{strconv.Itoa(i)}; return nil })
				tolerate(t, err, coredocstore.CodeDocumentNotFound)
				_, err = e.versions(key)
				tolerate(t, err, coredocstore.CodeDocumentNotFound)
				err = e.rewrite(key, func(f []coredocstore.VersionValue) ([]coredocstore.VersionValue, error) { return f[:len(f)/2], nil })
				tolerate(t, err, coredocstore.CodeDocumentNotFound)
				if i%9 == 0 {
					tolerate(t, e.del(key), coredocstore.CodeDocumentNotFound)
				}
			}
		})
	}
	wg.Wait()
	all, err := e.list()
	must(t, err)
	for _, a := range all {
		versions := engineVersionsOf(t, e, a.ID)
		if len(versions) > 4 {
			t.Fatalf("%s keeps %d versions, more than the current one and three former ones", a.ID, len(versions))
		}
		for i := 1; i < len(versions); i++ {
			if versions[i].Number >= versions[i-1].Number {
				t.Fatalf("%s's versions are numbered %v, not strictly decreasing", a.ID, numbers(versions))
			}
		}
		var current account
		must(t, json.Unmarshal(versions[0].JSON, &current))
		if current.Name != a.Name || !slices.Equal(current.Teams, a.Teams) {
			t.Fatalf("%s's current version holds %+v, the document %+v", a.ID, current, a)
		}
	}
}

// everyWriteAppendsOneChangeRecord: each write that stands appends one
// change record — its operation, the version it left and, asked, the
// document — and a refused write none, on every engine alike (#831).
func everyWriteAppendsOneChangeRecord(t *testing.T, f storeFactory) {
	feed := changeStream(t)
	e := f.open(t, storeConfig{versions: 2, clock: clock.NewManualClock(epoch), changes: feed})
	must(t, e.put(account{ID: "acc", Email: "a@x"}))
	must(t, e.update("acc", func(a *account) error { a.Name = "Ada"; return nil }))
	if err := e.insert(account{ID: "acc"}); !errs.HasCode(err, coredocstore.CodeDocumentExists) {
		t.Fatalf("Insert over a taken key = %v, want DOCUMENT_EXISTS", err)
	}
	must(t, e.trash("acc"))
	must(t, e.restore("acc"))
	must(t, e.del("acc"))
	changes := readChanges(t, feed)
	got := opsOf(changes)["acc"]
	if want := []string{"put@1", "put@2", "trash@2", "restore@2", "delete@0"}; !slices.Equal(got, want) || len(changes) != len(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	if !bytes.Contains(changes[1].Document, []byte(`"Ada"`)) || changes[4].Document != nil {
		t.Fatalf("documents = %s / %s, want Ada's then none for the delete", changes[1].Document, changes[4].Document)
	}
}
