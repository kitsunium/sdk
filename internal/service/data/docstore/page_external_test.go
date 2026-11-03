// Package docstore_test — the paged reads (#836), rows of the conformance
// table run on every engine: Documents and FindAfter (Pager, PagerContext),
// and Range over an index declared Ordered (Ranged, RangedContext); then
// what only one engine can show — the statements a page sends on SQL, the
// allocations a page costs on the file engine.
package docstore_test

import (
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// ordered adapts a paged-read row: the store keeps three former versions
// — so it has a trash — and the ordered index "since" over Name.
func ordered(fn func(t *testing.T, e storeEngine)) func(t *testing.T, f storeFactory) {
	return func(t *testing.T, f storeFactory) {
		fn(t, f.open(t, storeConfig{versions: 3, clock: clock.NewManualClock(epoch), ordered: true}))
	}
}

// drain ranges over seq to its end, failing the test on a yielded error.
func drain[V any](t *testing.T, seq iter.Seq2[V, error]) []V {
	t.Helper()
	var out []V
	for v, err := range seq {
		if err != nil {
			t.Fatalf("a paged read yielded %v", err)
		}
		out = append(out, v)
	}
	return out
}

// failureOf ranges over seq and returns its only yield, which must be a
// failure with the zero value.
func failureOf[V any](t *testing.T, seq iter.Seq2[V, error]) error {
	t.Helper()
	var failures []error
	for v, err := range seq {
		if err == nil || !reflect.ValueOf(&v).Elem().IsZero() {
			t.Fatalf("a refused read yielded (%v, %v), want only its failure", v, err)
		}
		failures = append(failures, err)
	}
	if len(failures) != 1 {
		t.Fatalf("a refused read yielded %d failures, want 1", len(failures))
	}
	return failures[0]
}

// keyed is the key of n accounts, acc_000 on, in key order.
func keyed(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("acc_%03d", i)
	}
	return out
}

// putAll stores an account under each key, in the order given.
func putAll(t *testing.T, e storeEngine, keys []string) {
	t.Helper()
	for _, key := range keys {
		must(t, e.put(account{ID: key, Name: "n"}))
	}
}

// entryKeys is the keys of entries, in order.
func entryKeys(entries []coredocstore.EntryValue) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Key
	}
	return out
}

// documentsVisitsEachDocumentOnce pins Documents' paging: 600 documents —
// more than two of the engine's own pages — written in no order, paged by
// 100 from the last key read, are each visited once, in key order, and
// read whole with no limit.
func documentsVisitsEachDocumentOnce(t *testing.T, e storeEngine) {
	want := keyed(600)
	shuffled := slices.Clone(want)
	slices.Reverse(shuffled[:300])
	putAll(t, e, shuffled)
	var seen []string
	pages := 0
	for after := ""; ; pages++ {
		page := drain(t, e.documents(after, 100))
		seen = append(seen, entryKeys(page)...)
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].Key
	}
	if !slices.Equal(seen, want) || pages != 6 {
		t.Fatalf("paging by 100 visited %d keys in %d full pages, want the 600 keys once each, in order, in 6", len(seen), pages)
	}
	if all := entryKeys(drain(t, e.documents("", 0))); !slices.Equal(all, want) {
		t.Fatalf("Documents(\"\", 0) = %d keys, want the 600 in order", len(all))
	}
	for _, entry := range drain(t, e.documents("acc_041", 1)) {
		var got account
		must(t, json.Unmarshal(entry.JSON, &got))
		if entry.Key != "acc_042" || got.ID != "acc_042" {
			t.Fatalf("Documents(acc_041, 1) = %s %s, want acc_042 with its JSON", entry.Key, entry.JSON)
		}
	}
}

// documentsResumesAndStops pins what a listing does around its caller: a
// stop ends it, a resume point that is no stored key resumes above it, a
// document written during the listing is seen when it sorts after the last
// key the store was read up to — the end of a page —, not when it sorts
// before, and an edit of a yielded JSON never reaches the store.
func documentsResumesAndStops(t *testing.T, e storeEngine) {
	putAll(t, e, []string{"acc_1", "acc_3", "acc_5"})
	got := 0
	for range e.documents("", 0) {
		got++
		break
	}
	if got != 1 {
		t.Fatalf("a stopped listing yielded %d, want 1", got)
	}
	if keys := entryKeys(drain(t, e.documents("acc_2", 0))); !slices.Equal(keys, []string{"acc_3", "acc_5"}) {
		t.Fatalf("Documents(acc_2) = %v, want [acc_3 acc_5]", keys)
	}
	putAll(t, e, keyed(300))
	var keys []string
	for entry, err := range e.documents("", 0) {
		must(t, err)
		keys = append(keys, entry.Key)
		entry.JSON[0] = 'X'
		if entry.Key == "acc_000" {
			must(t, e.put(account{ID: "acc_0005", Name: "behind"}))
			must(t, e.put(account{ID: "acc_2995", Name: "ahead"}))
		}
	}
	if slices.Contains(keys, "acc_0005") || !slices.Contains(keys, "acc_2995") || len(keys) != 304 {
		t.Fatalf("a listing during two writes read %d keys, want acc_2995 seen and acc_0005 not", len(keys))
	}
	if _, err := e.get("acc_000"); err != nil {
		t.Fatalf("Get after an edit of a yielded JSON = %v, want the store untouched", err)
	}
}

// findAfterPagesFind pins FindAfter: the holders of one index key, after a
// store key, by pages, are what Find reads, in the same order; no limit
// reads them all; the empty key files nobody.
func findAfterPagesFind(t *testing.T, e storeEngine) {
	for i, key := range keyed(7) {
		teams := []string{"red"}
		if i%2 == 0 {
			teams = append(teams, "blue")
		}
		must(t, e.put(account{ID: key, Teams: teams}))
	}
	found, err := e.find("team", "red")
	must(t, err)
	var paged []string
	for after := ""; ; {
		page := drain(t, e.findAfter("team", "red", after, 3))
		paged = append(paged, ids(page)...)
		if len(page) < 3 {
			break
		}
		after = page[len(page)-1].ID
	}
	if !slices.Equal(paged, ids(found)) || len(paged) != 7 {
		t.Fatalf("FindAfter by 3 = %v, want Find's %v", paged, ids(found))
	}
	if blue := ids(drain(t, e.findAfter("team", "blue", "acc_002", 0))); !slices.Equal(blue, []string{"acc_004", "acc_006"}) {
		t.Fatalf("FindAfter(blue, acc_002) = %v, want [acc_004 acc_006]", blue)
	}
	if none := drain(t, e.findAfter("team", "", "", 0)); len(none) != 0 {
		t.Fatalf("FindAfter of the empty key = %v, want nobody", ids(none))
	}
}

// rangeReadsAnOrderedIndexBetweenTwoKeys pins Range: the documents under a
// key from lo, included, to hi, excluded — no bound when hi is empty —, in
// index-key order then store-key order, capped by limit; an empty key is
// not indexed; a run of one key longer than a page is read whole.
func rangeReadsAnOrderedIndexBetweenTwoKeys(t *testing.T, e storeEngine) {
	for _, a := range []account{
		{ID: "acc_e", Name: "2026-01-02"},
		{ID: "acc_a", Name: "2026-01-02"},
		{ID: "acc_b", Name: "2026-01-01"},
		{ID: "acc_c", Name: "2026-01-03"},
		{ID: "acc_d", Name: "2026-02-01"},
		{ID: "acc_f"},
	} {
		must(t, e.put(a))
	}
	for _, c := range []struct {
		lo, hi string
		limit  int
		want   []string
	}{
		{lo: "2026-01-02", hi: "2026-02-01", want: []string{"acc_a", "acc_e", "acc_c"}},
		{lo: "2026-01-02", want: []string{"acc_a", "acc_e", "acc_c", "acc_d"}},
		{lo: "2026-01-02", limit: 2, want: []string{"acc_a", "acc_e"}},
		{hi: "2026-01-02", want: []string{"acc_b"}},
		{want: []string{"acc_b", "acc_a", "acc_e", "acc_c", "acc_d"}},
		{lo: "2026-01-03", hi: "2026-01-03"},
		{lo: "2026-03", hi: "2026-01"},
	} {
		if got := ids(drain(t, e.rangeOf("since", c.lo, c.hi, c.limit))); !slices.Equal(got, c.want) {
			t.Fatalf("Range(since, %q, %q, %d) = %v, want %v", c.lo, c.hi, c.limit, got, c.want)
		}
	}
	tied := keyed(300)
	for _, key := range tied {
		must(t, e.put(account{ID: key, Name: "2027"}))
	}
	if got := ids(drain(t, e.rangeOf("since", "2027", "", 0))); !slices.Equal(got, tied) {
		t.Fatalf("Range over one key held by 300 = %d documents, want the 300 in store-key order", len(got))
	}
}

// rangeFollowsEveryWrite pins that the ordered index moves with every
// write: a replacement refiles a document, a deletion and the trash take
// it out, a restoration puts it back.
func rangeFollowsEveryWrite(t *testing.T, e storeEngine) {
	must(t, e.put(account{ID: "acc_1", Name: "b"}))
	must(t, e.put(account{ID: "acc_2", Name: "c"}))
	must(t, e.put(account{ID: "acc_3", Name: "d"}))
	must(t, e.replace(account{ID: "acc_1", Name: "e"}))
	must(t, e.update("acc_2", func(a *account) error { a.Name = "a"; return nil }))
	if got := ids(drain(t, e.rangeOf("since", "", "", 0))); !slices.Equal(got, []string{"acc_2", "acc_3", "acc_1"}) {
		t.Fatalf("Range after two rewrites = %v, want [acc_2 acc_3 acc_1]", got)
	}
	must(t, e.del("acc_3"))
	must(t, e.trash("acc_1"))
	if got := ids(drain(t, e.rangeOf("since", "", "", 0))); !slices.Equal(got, []string{"acc_2"}) {
		t.Fatalf("Range after a deletion and a trash = %v, want [acc_2]", got)
	}
	must(t, e.restore("acc_1"))
	if got := ids(drain(t, e.rangeOf("since", "", "", 0))); !slices.Equal(got, []string{"acc_2", "acc_1"}) {
		t.Fatalf("Range after a restoration = %v, want [acc_2 acc_1]", got)
	}
}

// pagedReadsRefusals pins the refusals, each yielded once with the zero
// value: Range of an index declared without Ordered, Range and FindAfter
// of an undeclared one.
func pagedReadsRefusals(t *testing.T, e storeEngine) {
	must(t, e.put(account{ID: "acc_1", Name: "a", Teams: []string{"red"}}))
	requireCode(t, failureOf(t, e.rangeOf("team", "", "", 0)), coredocstore.CodeIndexNotOrdered, "Range(team)")
	requireCode(t, failureOf(t, e.rangeOf("nope", "", "", 0)), coredocstore.CodeIndexUnknown, "Range(nope)")
	findErr := func() error {
		for _, err := range e.findAfter("nope", "red", "", 0) {
			return err
		}
		return nil
	}()
	requireCode(t, findErr, coredocstore.CodeIndexUnknown, "FindAfter(nope)")
}

// TestDocumentsAllocatesPerPageNotPerStore pins #836's cost: a page of 100
// documents allocates the same at 1 000 documents and at 10 000 — what a
// page costs grows with the page, never with the store. Not parallel:
// AllocsPerRun refuses a parallel test.
func TestDocumentsAllocatesPerPageNotPerStore(t *testing.T) {
	allocs := func(n int) float64 {
		store := openAccounts(t, nil)
		for _, key := range keyed(n) {
			must(t, store.Put(account{ID: key}))
		}
		return testing.AllocsPerRun(5, func() {
			for _, err := range store.Documents("acc_100", 100) {
				must(t, err)
			}
		})
	}
	small, large := allocs(1000), allocs(10000)
	if large > small+2 {
		t.Fatalf("a page of 100 allocates %v times over 10 000 documents and %v over 1 000, want the same", large, small)
	}
}

// counted is a document that counts its decodes, so a test sees which
// documents a read decoded.
type counted struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// decodes counts every counted decoded, across the suite's stores.
var decodes atomic.Int64

// UnmarshalJSON decodes c and counts it.
func (c *counted) UnmarshalJSON(raw []byte) error {
	decodes.Add(1)
	type plain counted
	return json.Unmarshal(raw, (*plain)(c))
}

// TestRangeDecodesOnlyWhatItYields pins "without decoding others" (#836)
// on both engines: a Range of 3 documents among 200 decodes 3, and a
// FindAfter page as many as it yields.
func TestRangeDecodesOnlyWhatItYields(t *testing.T) {
	key := func(c counted) string { return c.ID }
	since := coredocstore.Ordered("since", func(c counted) string { return c.Name })
	team := coredocstore.Index("team", func(c counted) []string { return []string{"all"} })
	fill := func(put func(counted) error) {
		for i := range 200 {
			must(t, put(counted{ID: fmt.Sprintf("c_%03d", i), Name: fmt.Sprintf("%03d", i)}))
		}
	}
	file, err := docstore.Open(docstore.Config[counted]{Key: key}, since, team)
	must(t, err)
	fill(file.Put)
	engine := newSQLEngine(coresql.DialectSQLite, accountsTable)
	sqlStore, err := docstore.OpenSQL(docstore.SQLConfig[counted]{
		Key: key, Transactor: newSQLTransactor(t, engine, coresql.DialectSQLite), Dialect: coresql.DialectSQLite, Table: accountsTable,
	}, since, team)
	must(t, err)
	fill(func(c counted) error { return sqlStore.Put(t.Context(), c) })
	for name, reads := range map[string]struct{ ranged, found iter.Seq2[counted, error] }{
		"file": {file.Range("since", "050", "053", 0), file.FindAfter("team", "all", "c_100", 4)},
		"sql":  {sqlStore.Range(t.Context(), "since", "050", "053", 0), sqlStore.FindAfter(t.Context(), "team", "all", "c_100", 4)},
	} {
		before := decodes.Load()
		if got := drain(t, reads.ranged); len(got) != 3 || decodes.Load()-before != 3 {
			t.Fatalf("%s: Range of 3 among 200 decoded %d documents, want 3", name, decodes.Load()-before)
		}
		before = decodes.Load()
		if got := drain(t, reads.found); len(got) != 4 || decodes.Load()-before != 4 {
			t.Fatalf("%s: a FindAfter page of 4 decoded %d documents, want 4", name, decodes.Load()-before)
		}
	}
}

// TestAnOrderedIndexIsRebuiltAtOpen pins the file engine's sorted list
// across a reopen: Open files every loaded document, then sorts the list
// once, and Range reads the same as before.
func TestAnOrderedIndexIsRebuiltAtOpen(t *testing.T) {
	t.Parallel()
	fsys := memFS()
	indexes := append(accountIndexes(), coredocstore.Ordered("since", func(a account) string { return a.Name }))
	first, err := docstore.Open(accountConfig(fsys), indexes...)
	must(t, err)
	for i, key := range keyed(50) {
		must(t, first.Put(account{ID: key, Name: fmt.Sprintf("%02d", 49-i)}))
	}
	want := ids(drain(t, first.Range("since", "10", "20", 0)))
	must(t, first.Close())
	second, err := docstore.Open(accountConfig(fsys), indexes...)
	must(t, err)
	defer func() { must(t, second.Close()) }()
	if got := ids(drain(t, second.Range("since", "10", "20", 0))); !slices.Equal(got, want) || len(got) != 10 {
		t.Fatalf("Range after a reopen = %v, want %v", got, want)
	}
	must(t, second.Put(account{ID: "acc_x", Name: "15"}))
	if got := ids(drain(t, second.Range("since", "15", "16", 0))); !slices.Equal(got, []string{"acc_034", "acc_x"}) {
		t.Fatalf("Range after a write on a reopened store = %v, want [acc_034 acc_x]", got)
	}
}

// TestSQLAPageIsOneStatement pins the SQL engine's paging on every dialect:
// a page of Documents, FindAfter or Range is one statement, its LIMIT
// bound to what the caller asked, and a listing with no limit reads pages
// of a few hundred.
func TestSQLAPageIsOneStatement(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		ctx := t.Context()
		fx := openSQL(t, dialect, append(accountIndexes(), coredocstore.Ordered("since", func(a account) string { return a.Name }))...)
		for _, key := range keyed(600) {
			must(t, fx.store.Put(ctx, account{ID: key, Name: key, Teams: []string{"red"}}))
		}
		for _, c := range []struct {
			read  func() int
			role  string
			pages int
		}{
			{role: "pageAfter", pages: 1, read: func() int { return len(drain(t, fx.store.Documents(ctx, "", 100))) }},
			{role: "pageAfter", pages: 3, read: func() int { return len(drain(t, fx.store.Documents(ctx, "", 0))) }},
			{role: "findAfter", pages: 1, read: func() int { return len(drain(t, fx.store.FindAfter(ctx, "team", "red", "", 10))) }},
			{role: "rangeBelow", pages: 1, read: func() int { return len(drain(t, fx.store.Range(ctx, "since", "acc_1", "acc_2", 10))) }},
			{role: "rangeFrom", pages: 3, read: func() int { return len(drain(t, fx.store.Range(ctx, "since", "", "", 0))) }},
		} {
			before := len(fx.engine.roles())
			n := c.read()
			sent := fx.engine.roles()[before:]
			if len(sent) != c.pages || strings.Count(strings.Join(sent, " "), c.role) != c.pages {
				t.Fatalf("a read of %d through %s sent %v, want %d %s", n, c.role, sent, c.pages, c.role)
			}
		}
	})
}

// TestSQLThePagedStatements pins, as text, the statements of FindAfter and
// Range on PostgreSQL and MySQL: each on the index table's primary key
// (index_name, index_key, doc_key), every placeholder once and in order —
// MySQL's are positional —, Range's cursor written without a row value and
// bounded below on the index key alone too, so its scan starts at the
// cursor. Documents sends Reindex's pageAfter, pinned with it.
func TestSQLThePagedStatements(t *testing.T) {
	t.Parallel()
	join := ` i JOIN "members__accounts" d ON d.doc_key = i.doc_key WHERE i.index_name = $1`
	cursor := ` AND i.index_key >= $2 AND (i.index_key > $3 OR (i.index_key = $4 AND i.doc_key > $5))`
	order := ` ORDER BY i.index_key, i.doc_key LIMIT `
	mysqlJoin := " i JOIN `members__accounts` d ON d.doc_key = i.doc_key WHERE i.index_name = ?"
	mysqlCursor := " AND i.index_key >= ? AND (i.index_key > ? OR (i.index_key = ? AND i.doc_key > ?))"
	for _, tc := range []struct {
		dialect coresql.Dialect
		want    map[string]string
	}{
		{coresql.DialectPostgres, map[string]string{
			"findAfter":  `SELECT i.doc_key, d.doc FROM "members__accounts___ix"` + join + ` AND i.index_key = $2 AND i.doc_key > $3 ORDER BY i.doc_key LIMIT $4`,
			"rangeFrom":  `SELECT i.index_key, i.doc_key, d.doc FROM "members__accounts___ix"` + join + cursor + order + `$6`,
			"rangeBelow": `SELECT i.index_key, i.doc_key, d.doc FROM "members__accounts___ix"` + join + cursor + ` AND i.index_key < $6` + order + `$7`,
			"pageAfter":  `SELECT doc_key, doc FROM "members__accounts" WHERE doc_key > $1 ORDER BY doc_key LIMIT $2`,
		}},
		{coresql.DialectMySQL, map[string]string{
			"findAfter":  "SELECT i.doc_key, d.doc FROM `members__accounts___ix`" + mysqlJoin + " AND i.index_key = ? AND i.doc_key > ? ORDER BY i.doc_key LIMIT ?",
			"rangeFrom":  "SELECT i.index_key, i.doc_key, d.doc FROM `members__accounts___ix`" + mysqlJoin + mysqlCursor + order + "?",
			"rangeBelow": "SELECT i.index_key, i.doc_key, d.doc FROM `members__accounts___ix`" + mysqlJoin + mysqlCursor + " AND i.index_key < ?" + order + "?",
			"pageAfter":  "SELECT doc_key, doc FROM `members__accounts` WHERE doc_key > ? ORDER BY doc_key LIMIT ?",
		}},
	} {
		rendered := docstore.RenderSQLForTest(tc.dialect, accountsTable)
		for role, want := range tc.want {
			if got := statementFor(t, rendered, role); got != want {
				t.Errorf("%s %s =\n%s\nwant\n%s", tc.dialect, role, got, want)
			}
		}
	}
}
