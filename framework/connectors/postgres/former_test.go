package postgres_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/postgres"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The names at rest on PostgreSQL (ADR 0180 step 5): a database a former
// release kept under kit_* — its version table, its registry, a consumer's
// queue, the privacy service's stores and their index rows, the indexes
// PostgreSQL named after them — is moved to the framework's names at the
// first start, every row as it was, and read back as it was; a second start
// moves nothing; a present name already taken refuses the start and nothing
// moves. The former layout is made by renaming back what this release wrote
// — the statements an operator going back to the former release runs, which
// the SQLite suite shows restore a former release's database byte for byte.

// The statements that rename the framework's tables back to the names a
// former release gave them: each table and index named after the framework,
// the version table, and the names the registry and the history give the
// tables.
const (
	renameTablesBack = `DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema()
    AND (tablename LIKE 'app\_privacy\_\_%' OR tablename LIKE 'app\_queue\_\_%' OR tablename LIKE 'app\_tables%' OR tablename = 'app_migrations') LOOP
    EXECUTE format('ALTER TABLE %I RENAME TO %I', r.tablename, 'kit' || substr(r.tablename, 4));
  END LOOP;
  FOR r IN SELECT indexname FROM pg_catalog.pg_indexes WHERE schemaname = current_schema()
    AND (indexname LIKE 'app\_privacy\_\_%' OR indexname LIKE 'app\_queue\_\_%' OR indexname LIKE 'app\_tables%' OR indexname LIKE 'app\_migrations%') LOOP
    EXECUTE format('ALTER INDEX %I RENAME TO %I', r.indexname, 'kit' || substr(r.indexname, 4));
  END LOOP;
END $$`
	renameRegistryBack = `UPDATE kit_tables SET
  doc_key = convert_to(replace(replace(convert_from(doc_key, 'UTF8'), 'app_privacy__', 'kit_privacy__'), 'app_queue__', 'kit_queue__'), 'UTF8'),
  doc = convert_to(replace(replace(convert_from(doc, 'UTF8'), 'app_privacy__', 'kit_privacy__'), 'app_queue__', 'kit_queue__'), 'UTF8')`
	renameHistoryBack = `UPDATE kit_migrations SET
  name = replace(replace(replace(name, 'app_privacy__', 'kit_privacy__'), 'app_queue__', 'kit_queue__'), 'app_tables', 'kit_tables')`
	takePresentName    = `CREATE TABLE app_privacy__holds (doc_key bytea NOT NULL PRIMARY KEY, rev bigint NOT NULL, doc bytea NOT NULL)`
	blockPresentName   = "CREATE VIEW app_privacy__journal AS SELECT 1 AS one"
	unblockPresentName = "DROP VIEW app_privacy__journal"
	listCatalogue      = `SELECT relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'i')
  UNION SELECT conname FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_namespace n ON n.oid = c.connamespace
  WHERE n.nspname = current_schema() ORDER BY 1`
)

// formerTables are the tables the product keeps, under this release's
// names: the dump reads each, and the catalogue holds no other.
var formerTables = []string{
	"app_migrations", "app_privacy__holds", "app_privacy__holds___ix", "app_privacy__journal", "app_privacy__journal___ix",
	"app_privacy__keys", "app_privacy__keys___ix", "app_queue__atelier__post", "app_tables", "app_tables___ix",
	"atelier__cases", "atelier__cases___ix", "atelier__cases___vs", "atelier__cases__history", "atelier__cases__history___ix",
	"atelier__tally", "atelier__tally___ix",
}

// atelierCase is a person's case, sealed where it rests, with its note's
// former values, its versions and its trash.
type atelierCase struct {
	ID    string `json:"id"`
	Email string `json:"email" app:"subject"`
	Note  string `json:"note" app:"personal,history=3"`
	Stage int    `json:"stage"`
}

// atelierLetter is a letter about a person, sealed in its queue.
type atelierLetter struct {
	To   string `json:"to" app:"subject"`
	Body string `json:"body" app:"personal"`
}

// atelierWho names the person a privacy command is about.
type atelierWho struct {
	ID string `json:"id"`
}

// atelier is the product, its data and app.Privacy's on PostgreSQL.
type atelier struct {
	app     *app.App
	cases   *app.Store[atelierCase]
	letters *app.Topic[atelierLetter]
	export  *app.Command[atelierWho, app.PersonalData]
	forget  *app.Command[atelierWho, app.Erasure]
	posted  chan atelierLetter
}

// newAtelier declares the product on raw, its data directory dir; with
// wait, its subscriber holds every letter until its context ends.
func newAtelier(t *testing.T, raw, dir string, wait bool) *atelier {
	t.Helper()
	t.Setenv("ATELIER_SECRETS", "memory")
	t.Setenv("ATELIER_DATA_KEY", "an atelier data key, 32 bytes...")
	t.Setenv("ATELIER_INDEX_KEY", "an atelier index key of more than 16 bytes")
	t.Setenv("ATELIER_MAIN_URL", raw)
	svc := app.NewService("atelier", "Cases and letters about people, sealed where they rest.")
	a := &atelier{posted: make(chan atelierLetter, 8)}
	a.cases = svc.Store("cases", func(c atelierCase) string { return c.ID }, app.Purpose("Handle the cases"), app.Revisions(3))
	svc.Store("tally", func(e entry) string { return e.ID })
	a.letters = svc.Topic[atelierLetter]("letters")
	svc.Subscribe("post", a.letters, func(ctx context.Context, l atelierLetter) error {
		if wait {
			<-ctx.Done()
			return ctx.Err()
		}
		a.posted <- l
		return nil
	})
	a.export = svc.Command("export", func(ctx context.Context, w atelierWho) (app.PersonalData, error) {
		return app.Export(ctx, w.ID)
	})
	a.forget = svc.Command("forget", func(ctx context.Context, w atelierWho) (app.Erasure, error) {
		return app.Erase(ctx, "the person asked", w.ID)
	})
	a.app = app.NewApp("atelier", svc).With(app.Database("main", postgres.Engine(), app.Keeps(app.Privacy, svc)),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard))
	return a
}

// writeAtelier writes what the product keeps — versions, trash, an erased
// person, a held case, a letter delivered and one kept in the queue — and
// returns what it reads of it.
func writeAtelier(t *testing.T, raw, dir string) []byte {
	t.Helper()
	ctx := t.Context()
	a := newAtelier(t, raw, dir, false)
	if err := a.app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("ann", a.cases.Insert(ctx, atelierCase{ID: "c-ann", Email: "ann@atelier.test", Note: "a first note about ann", Stage: 1}))
	must("ann 2", a.cases.Put(ctx, atelierCase{ID: "c-ann", Email: "ann@atelier.test", Note: "a second note about ann", Stage: 2}))
	must("bob", a.cases.Insert(ctx, atelierCase{ID: "c-bob", Email: "bob@atelier.test", Note: "a note about bob", Stage: 1}))
	must("bob trashed", a.cases.Delete(ctx, "c-bob"))
	must("cy", a.cases.Insert(ctx, atelierCase{ID: "c-cy", Email: "cy@atelier.test", Note: "a note about cy", Stage: 1}))
	_, err := a.forget.Dispatch(ctx, atelierWho{ID: "cy@atelier.test"})
	must("cy forgotten", err)
	must("dan", a.cases.Insert(ctx, atelierCase{ID: "c-dan", Email: "dan@atelier.test", Note: "a note about dan", Stage: 4}))
	must("dan held", a.cases.Hold(ctx, "c-dan", "a court's request"))
	must("letter", a.letters.Publish(ctx, atelierLetter{To: "ann@atelier.test", Body: "a letter to ann"}))
	if got := received(t, a.posted); got.Body != "a letter to ann" {
		t.Fatalf("letter %+v", got)
	}
	dump := dumpAtelier(t, a)
	must("stop", a.app.Stop(ctx))
	w := newAtelier(t, raw, dir, true)
	must("start waiting", w.app.Start(ctx))
	must("letter kept", w.letters.Publish(ctx, atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}))
	must("stop waiting", w.app.Stop(ctx))
	return dump
}

// received is the next letter, within a few seconds.
func received(t *testing.T, ch <-chan atelierLetter) atelierLetter {
	t.Helper()
	select {
	case l := <-ch:
		return l
	case <-time.After(15 * time.Second):
		t.Fatal("no letter delivered")
	}
	return atelierLetter{}
}

// dumpAtelier is what the running product reads: its cases, their versions,
// former notes and trash, and a person's data opened.
func dumpAtelier(t *testing.T, a *atelier) []byte {
	t.Helper()
	ctx := t.Context()
	type revision struct {
		Number uint64
		At     time.Time
		By     string
		Value  atelierCase
	}
	d := struct {
		Cases     []atelierCase
		Revisions map[string][]revision
		Former    map[string][]app.Former
		Trashed   []app.Tombstone
		Export    app.PersonalData
	}{Revisions: map[string][]revision{}, Former: map[string][]app.Former{}}
	var err error
	if d.Cases, err = a.cases.List(ctx); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(d.Cases, func(x, y atelierCase) int { return strings.Compare(x.ID, y.ID) })
	for _, key := range []string{"c-ann", "c-bob", "c-cy"} {
		revs, err := a.cases.Revisions(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range revs {
			d.Revisions[key] = append(d.Revisions[key], revision{r.Number, r.At.UTC(), r.By, r.Value})
		}
		if former, err := a.cases.Former(ctx, key, "/note"); err == nil {
			d.Former[key] = former
		}
	}
	if d.Trashed, err = a.cases.Trashed(ctx); err != nil {
		t.Fatal(err)
	}
	if d.Export, err = a.export.Dispatch(ctx, atelierWho{ID: "ann@atelier.test"}); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rowsOf is every row of every table of formerTables, its columns in hex,
// sorted, under the names given: this release's, or the former ones.
func rowsOf(t *testing.T, raw string, former bool) map[string][]string {
	t.Helper()
	db := open(t, raw)
	out := map[string][]string{}
	for _, table := range formerTables {
		name := table
		if former && strings.HasPrefix(name, "app_") {
			name = "kit" + strings.TrimPrefix(name, "app")
		}
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+name+`"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			vals, ptrs := make([]any, len(cols)), make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				b, ok := v.([]byte)
				if !ok {
					b, _ = json.Marshal(v)
				}
				parts[i] = hex.EncodeToString(b)
			}
			out[table] = append(out[table], strings.Join(parts, "|"))
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[table])
	}
	return out
}

// catalogueOf is every table, index and constraint name of the database.
func catalogueOf(t *testing.T, raw string) []string {
	t.Helper()
	rows, err := open(t, raw).QueryContext(t.Context(), listCatalogue)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

// execAll runs stmts on raw, in order.
func execAll(t *testing.T, raw string, stmts ...string) {
	t.Helper()
	db := open(t, raw)
	for _, s := range stmts {
		if _, err := db.ExecContext(t.Context(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestAFormerReleasesTablesAreMovedAndReadAsTheyWere(t *testing.T) {
	raw := fresh(t)
	dir := t.TempDir()
	want := writeAtelier(t, raw, dir)
	rows := rowsOf(t, raw, false)
	execAll(t, raw, renameTablesBack, renameRegistryBack, renameHistoryBack)
	if cat := catalogueOf(t, raw); slices.ContainsFunc(cat, func(n string) bool { return strings.HasPrefix(n, "app_") }) {
		t.Fatalf("the former layout still names the framework: %v", cat)
	}
	if got := rowsOf(t, raw, true); len(got["app_privacy__journal"]) == 0 {
		t.Fatalf("the former layout holds no journal: %v", slices.Sorted(maps.Keys(got)))
	}

	// The move alone, as `migrate up` runs it: every row it left, byte for
	// byte, under the framework's names.
	if status := newAtelier(t, raw, dir, false).app.Main(t.Context(), []string{"migrate", "up"}); status != 0 {
		t.Fatalf("migrate up: %d", status)
	}
	if got := rowsOf(t, raw, false); !maps.EqualFunc(got, rows, slices.Equal) {
		for _, table := range formerTables {
			if !slices.Equal(got[table], rows[table]) {
				t.Errorf("%s once moved:\n got %q\nwant %q", table, got[table], rows[table])
			}
		}
	}
	for round := range 2 {
		a := newAtelier(t, raw, dir, false)
		run(t, a.app)
		if round == 0 {
			if got := received(t, a.posted); got != (atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}) {
				t.Errorf("the letter kept: %+v", got)
			}
		}
		if got := dumpAtelier(t, a); string(got) != string(want) {
			t.Errorf("round %d, what the product reads:\n got %s\nwant %s", round, got, want)
		}
		if err := a.app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, name := range catalogueOf(t, raw) {
			if strings.Contains(name, "kit") {
				t.Errorf("round %d: the database still names %s", round, name)
			}
		}
	}
}

func TestAMoveOnPostgresNeverReplacesWhatIsThere(t *testing.T) {
	raw := fresh(t)
	dir := t.TempDir()
	writeAtelier(t, raw, dir)
	execAll(t, raw, renameTablesBack, renameRegistryBack, renameHistoryBack, takePresentName)
	before := catalogueOf(t, raw)
	err := newAtelier(t, raw, dir, false).app.Start(t.Context())
	if !errs.HasCode(err, app.CodeRenameTarget) {
		t.Fatalf("the start with a present name taken: %v", err)
	}
	if got := catalogueOf(t, raw); !slices.Equal(got, before) {
		t.Errorf("the catalogue changed:\n got %v\nwant %v", got, before)
	}
}

// A move the database refuses half-way — a view under one present name,
// which the catalogue the move judges does not list, so the renaming starts
// and the database refuses it — moves no table: what the former release kept
// is still under its former names. Once the cause is gone, the next start
// moves everything, and the product reads what it wrote.
func TestAMoveThatFailsHalfWayIsResumed(t *testing.T) {
	raw := fresh(t)
	dir := t.TempDir()
	want := writeAtelier(t, raw, dir)
	execAll(t, raw, renameTablesBack, renameRegistryBack, renameHistoryBack, blockPresentName)
	err := newAtelier(t, raw, dir, false).app.Start(t.Context())
	if !errs.HasCode(err, app.CodeRenameFailed) {
		t.Fatalf("the start whose move failed half-way: %v", err)
	}
	for _, name := range []string{"kit_privacy__holds", "kit_privacy__journal", "kit_queue__atelier__post", "kit_tables", "kit_migrations"} {
		if !slices.Contains(catalogueOf(t, raw), name) {
			t.Errorf("%s is gone after the failed move", name)
		}
	}
	execAll(t, raw, unblockPresentName)
	a := newAtelier(t, raw, dir, false)
	run(t, a.app)
	if got := dumpAtelier(t, a); string(got) != string(want) {
		t.Errorf("once moved, what the product reads:\n got %s\nwant %s", got, want)
	}
	for _, name := range catalogueOf(t, raw) {
		if strings.Contains(name, "kit") {
			t.Errorf("the database still names %s", name)
		}
	}
}
