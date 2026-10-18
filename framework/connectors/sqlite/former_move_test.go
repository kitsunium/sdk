package sqlite_test

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What a former release kept (testdata/former, written before ADR 0180 step
// 5) is moved to the framework's names the first time this release meets
// it, and read back as it was: every row of every table and every file of
// the data directory byte for byte, the privacy journal's chain, the holds,
// the cases with their versions, former values and trash, a person's data
// opened from where it is sealed, an erased person still erased, and the
// letter still queued delivered. A second start moves nothing.
func TestAFormerReleasesDataIsMovedAndReadAsItWas(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former", kind)
			dir := formerCopy(t, fixture)
			files, tables := treeOf(t, dir), tablesOf(t, dir)
			if _, ok := files["kit.privacy/keys.json"]; !ok {
				t.Fatalf("the fixture keeps no kit.privacy/keys.json: %v", slices.Sorted(maps.Keys(files)))
			}
			if _, ok := tables["kit_privacy__journal"]; onSQLite && (!ok || len(tables["kit_queue__atelier__post"]) != 1) {
				t.Fatalf("the fixture's tables: %v", slices.Sorted(maps.Keys(tables)))
			}

			// The move, alone: the data directory's at resolve, the tables'
			// by migrate up.
			if status, out := mainOf(t, newAtelier(t, dir, onSQLite, false), "migrate", "up"); onSQLite && status != 0 {
				t.Fatalf("migrate up: %d %s", status, out)
			}
			movedFiles := map[string][]byte{}
			for name, b := range files {
				movedFiles[strings.Replace(name, "kit.privacy/", "app.privacy/", 1)] = b
			}
			if got := treeOf(t, dir); !maps.EqualFunc(got, movedFiles, bytes.Equal) {
				t.Errorf("the data directory once moved:\n got %v\nwant %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(movedFiles)))
			}
			if onSQLite {
				moved := tablesOf(t, dir)
				if len(moved["app_privacy__journal"]) == 0 || len(moved["app_queue__atelier__post"]) != 1 {
					t.Fatalf("the moved tables: %v", slices.Sorted(maps.Keys(moved)))
				}
				sameTablesMoved(t, tables, moved)
				for _, name := range catalogueOf(t, dir) {
					if strings.Contains(name, "kit") {
						t.Errorf("the database still names %s", name)
					}
				}
			}

			for name, args := range map[string][]string{
				"journal.txt": {"privacy", "journal", "-verify"},
				"holds.txt":   {"privacy", "holds"},
			} {
				status, got := mainOf(t, newAtelier(t, dir, onSQLite, false), args...)
				want, err := os.ReadFile(filepath.Join(fixture, name))
				if err != nil {
					t.Fatal(err)
				}
				if status != 0 || !bytes.Equal(got, want) {
					t.Errorf("%v: %d\n got %s\nwant %s", args, status, got, want)
				}
			}

			for round := range 2 {
				a := newAtelier(t, dir, onSQLite, false)
				run(t, a.app)
				want, err := os.ReadFile(filepath.Join(fixture, "dump.json"))
				if err != nil {
					t.Fatal(err)
				}
				if got := dumpAtelier(t, a); !bytes.Equal(got, want) {
					t.Errorf("round %d, what the product reads:\n got %s\nwant %s", round, got, want)
				}
				if round == 0 {
					if got := received(t, "the letter kept in the queue", a.posted); got != (atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}) {
						t.Errorf("the letter kept: %+v", got)
					}
				}
				if err := a.app.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(dir, "kit.privacy")); !os.IsNotExist(err) {
					t.Errorf("round %d: kit.privacy is back: %v", round, err)
				}
			}
			if onSQLite {
				for _, name := range catalogueOf(t, dir) {
					if strings.Contains(name, "kit") {
						t.Errorf("after two starts, the database names %s", name)
					}
				}
				if status, out := mainOf(t, newAtelier(t, dir, onSQLite, false), "migrate", "status"); status != 0 || strings.Contains(string(out), "former") {
					t.Errorf("migrate status after the move: %d %s", status, out)
				}
			}
		})
	}
}

// A move never replaces: a present name already taken refuses the start
// with CodeRenameTarget, both named, and what the former release kept stays
// where it was, byte for byte, its tables unrenamed and its version table
// recording no move.
func TestAMoveNeverReplacesWhatIsThere(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			dir := formerCopy(t, filepath.Join("testdata", "former", kind))
			if onSQLite {
				execOn(t, dir, `CREATE TABLE "app_privacy__holds" (doc_key BLOB NOT NULL PRIMARY KEY, rev INTEGER NOT NULL, doc BLOB NOT NULL)`)
			} else {
				if err := os.Mkdir(filepath.Join(dir, "app.privacy"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			files, tables := treeOf(t, dir), tablesOf(t, dir)
			err := newAtelier(t, dir, onSQLite, false).app.Start(t.Context())
			if !errs.HasCode(err, app.CodeRenameTarget) {
				t.Fatalf("the start with a present name taken: %v", err)
			}
			if !onSQLite {
				// The directory is moved before the database is opened: the
				// first refusal is the directory's.
				if got := treeOf(t, dir); !maps.EqualFunc(got, files, bytes.Equal) {
					t.Errorf("the data directory changed: %v", slices.Sorted(maps.Keys(got)))
				}
				return
			}
			after := tablesOf(t, dir)
			delete(after, "app_migrations") // made by the SDK's migrator before the move is judged
			if !maps.EqualFunc(after, tables, slices.Equal) {
				t.Errorf("the tables changed:\n got %v\nwant %v", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(tables)))
			}
		})
	}
}

// A move that fails half-way leaves the former release's data as it was:
// on SQLite a renaming the database refuses after others ran rolls the whole
// move back; once the cause is gone, the next start moves everything and
// reads it as it was.
func TestAMoveThatFailsHalfWayLeavesTheFormerDataReadable(t *testing.T) {
	needsFileStore(t)
	fixture := filepath.Join("testdata", "former", "sqlite")
	dir := formerCopy(t, fixture)
	// A view under one present name: the catalogue the move judges lists
	// tables, so the move starts, renames the tables before it, and the
	// database refuses this one.
	execOn(t, dir, `CREATE VIEW "app_privacy__journal" AS SELECT 1 AS one`)
	files, tables := treeOf(t, dir), tablesOf(t, dir)
	err := newAtelier(t, dir, true, false).app.Start(t.Context())
	if !errs.HasCode(err, app.CodeRenameFailed) {
		t.Fatalf("the start whose move failed half-way: %v", err)
	}
	after := tablesOf(t, dir)
	delete(after, "app_migrations")
	if !maps.EqualFunc(after, tables, slices.Equal) {
		t.Errorf("the tables after the failed move:\n got %v\nwant %v", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(tables)))
	}
	moved := map[string][]byte{}
	for name, b := range files {
		moved[strings.Replace(name, "kit.privacy/", "app.privacy/", 1)] = b
	}
	if got := treeOf(t, dir); !maps.EqualFunc(got, moved, bytes.Equal) {
		t.Errorf("the data directory: %v", slices.Sorted(maps.Keys(got)))
	}
	execOn(t, dir, `DROP VIEW "app_privacy__journal"`)
	a := newAtelier(t, dir, true, false)
	run(t, a.app)
	want, err := os.ReadFile(filepath.Join(fixture, "dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := dumpAtelier(t, a); !bytes.Equal(got, want) {
		t.Errorf("what the product reads once moved:\n got %s\nwant %s", got, want)
	}
}

// The move is undone by renaming back (ADR 0180 step 5, §Breaking changes):
// the tables and the version table to their former names, the registry's
// and the history's names of the tables with them, the privacy service's
// directory to kit.privacy. Nothing it moved was rewritten, so what is left
// is the former release's data, byte for byte — the fixture itself —, and
// the next start of this release moves it again.
func TestTheMoveIsUndoneByRenamingBack(t *testing.T) {
	needsFileStore(t)
	fixture := filepath.Join("testdata", "former", "sqlite")
	dir := formerCopy(t, fixture)
	files, tables := treeOf(t, dir), tablesOf(t, dir)
	if status, out := mainOf(t, newAtelier(t, dir, true, false), "migrate", "up"); status != 0 {
		t.Fatalf("migrate up: %d %s", status, out)
	}
	db := openMain(t, dir)
	renameBack(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "app.privacy"), filepath.Join(dir, "kit.privacy")); err != nil {
		t.Fatal(err)
	}
	if got := tablesOf(t, dir); !maps.EqualFunc(got, tables, slices.Equal) {
		for _, table := range slices.Sorted(maps.Keys(tables)) {
			if !slices.Equal(got[table], tables[table]) {
				t.Errorf("%s once renamed back:\n got %q\nwant %q", table, got[table], tables[table])
			}
		}
	}
	if got := treeOf(t, dir); !maps.EqualFunc(got, files, bytes.Equal) {
		t.Errorf("the data directory once renamed back: %v", slices.Sorted(maps.Keys(got)))
	}
	a := newAtelier(t, dir, true, false)
	run(t, a.app)
	want, err := os.ReadFile(filepath.Join(fixture, "dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := dumpAtelier(t, a); !bytes.Equal(got, want) {
		t.Errorf("moved again, what the product reads:\n got %s\nwant %s", got, want)
	}
}

// presentToFormer are the framework's names a renaming back replaces.
var presentToFormer = []struct{ present, former string }{
	{"app_privacy__", "kit_privacy__"}, {"app_queue__", "kit_queue__"}, {"app_tables", "kit_tables"},
}

// renameBack undoes the move on db, as an operator going back to the former
// release would, before its first start: each table and the version table
// to its former name, and the names the registry and the history give the
// tables.
func renameBack(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	formerOf := func(b []byte) []byte {
		for _, r := range presentToFormer {
			b = bytes.ReplaceAll(b, []byte(r.present), []byte(r.former))
		}
		return b
	}
	for _, table := range namesOn(t, db, "SELECT name FROM sqlite_master WHERE type = 'table'") {
		former := string(formerOf([]byte(table)))
		if table == "app_migrations" {
			former = "kit_migrations"
		}
		if former != table {
			if _, err := db.ExecContext(ctx, `ALTER TABLE "`+table+`" RENAME TO "`+former+`"`); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT doc_key, doc FROM "kit_tables"`)
	if err != nil {
		t.Fatal(err)
	}
	type record struct{ key, doc []byte }
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.key, &r.doc); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if _, err := db.ExecContext(ctx, `UPDATE "kit_tables" SET doc_key = ?, doc = ? WHERE doc_key = ?`, formerOf(r.key), formerOf(r.doc), r.key); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range namesOn(t, db, `SELECT name FROM "kit_migrations"`) {
		if _, err := db.ExecContext(ctx, `UPDATE "kit_migrations" SET name = ? WHERE name = ?`, string(formerOf([]byte(name))), name); err != nil {
			t.Fatal(err)
		}
	}
}

// sameTablesMoved checks that after holds before's tables under their
// present names, every row byte for byte — but the two the move rewrites:
// the registry's records, keyed and naming their tables by their present
// names, and the history, which names the tables' migrations so.
func sameTablesMoved(t *testing.T, before, after map[string][]string) {
	t.Helper()
	present := func(name string) string {
		for _, p := range []string{"kit_privacy__", "kit_queue__", "kit_tables", "kit_migrations"} {
			if strings.HasPrefix(name, p) {
				return "app" + strings.TrimPrefix(name, "kit")
			}
		}
		return name
	}
	want := map[string][]string{}
	for table, rows := range before {
		var out []string
		for _, r := range rows {
			switch table {
			case "kit_tables", "kit_migrations":
				out = append(out, renamedRow(t, r))
			default:
				out = append(out, r)
			}
		}
		slices.Sort(out)
		want[present(table)] = out
	}
	if !maps.EqualFunc(after, want, slices.Equal) {
		for _, table := range slices.Sorted(maps.Keys(want)) {
			if !slices.Equal(after[table], want[table]) {
				t.Errorf("%s:\n got %q\nwant %q", table, after[table], want[table])
			}
		}
		for table := range after {
			if _, ok := want[table]; !ok {
				t.Errorf("%s: a table the former release did not have", table)
			}
		}
	}
}

// renamedRow is a row of the registry or of the history with the former
// tables' names replaced by their present ones, in each column.
func renamedRow(t *testing.T, row string) string {
	t.Helper()
	cols := strings.Split(row, "|")
	for i, c := range cols {
		raw, err := hex.DecodeString(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"kit_privacy__", "kit_queue__", "kit_tables"} {
			raw = bytes.ReplaceAll(raw, []byte(p), []byte("app"+strings.TrimPrefix(p, "kit")))
		}
		cols[i] = hex.EncodeToString(raw)
	}
	return strings.Join(cols, "|")
}

// formerCopy copies the fixture's data directory into a directory of the
// test's, with the modes the data directory needs.
func formerCopy(t *testing.T, fixture string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	src := filepath.Join(fixture, "data")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// treeOf is every file under dir but the database's, by its path, with its
// bytes.
func treeOf(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), "main.sqlite") {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// openMain opens the data directory's SQLite file, nil when there is none.
func openMain(t *testing.T, dir string) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, "main.sqlite")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tablesOf is every table of the data directory's SQLite file, each row its
// columns in hex, sorted; none without a file.
func tablesOf(t *testing.T, dir string) map[string][]string {
	t.Helper()
	db := openMain(t, dir)
	if db == nil {
		return map[string][]string{}
	}
	defer db.Close()
	out := map[string][]string{}
	for _, table := range namesOn(t, db, "SELECT name FROM sqlite_master WHERE type = 'table'") {
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+table+`"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = hex.EncodeToString(asBytes(v))
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

// asBytes is a column's value as bytes: a blob as it is, anything else as
// JSON writes it.
func asBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	b, _ := json.Marshal(v)
	return b
}

// catalogueOf is every name the data directory's SQLite file gives a table
// or an index.
func catalogueOf(t *testing.T, dir string) []string {
	t.Helper()
	db := openMain(t, dir)
	if db == nil {
		return nil
	}
	defer db.Close()
	return namesOn(t, db, "SELECT name FROM sqlite_master")
}

// namesOn is the one column query lists.
func namesOn(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// execOn runs a statement on the data directory's SQLite file.
func execOn(t *testing.T, dir, stmt string) {
	t.Helper()
	db := openMain(t, dir)
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), stmt); err != nil {
		t.Fatal(err)
	}
}

// mainOf runs the product's binary with args, its standard output
// captured.
func mainOf(t *testing.T, a *atelier, args ...string) (int, []byte) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	status := a.app.Main(t.Context(), args)
	os.Stdout = saved
	_ = w.Close()
	return status, <-done
}
