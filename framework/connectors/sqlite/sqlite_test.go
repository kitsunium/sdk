package sqlite_test

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/app/storetest"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// countTables counts the store's table in the file.
const countTables = "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'audit__trail'"

// errTakeBack rolls a transaction back.
var errTakeBack = errors.New("the transaction is taken back")

// A database in a SQLite file needs no server: these tests always run.

// open opens path through the engine.
func open(t *testing.T, path string) *stdsql.DB {
	t.Helper()
	v := secret.FromString(path)
	db, err := sqlite.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the pool: %v", err)
		}
	})
	return db
}

func TestDescribeNamesTheFile(t *testing.T) {
	for raw, want := range map[string]string{
		"/data/archive.sqlite":                         "/data/archive.sqlite",
		"file:/data/archive.sqlite?_busy_timeout=9000": "/data/archive.sqlite",
		"relative/archive.sqlite":                      "relative/archive.sqlite",
	} {
		got, err := sqlite.Engine().Describe(secret.FromString(raw))
		if err != nil || got != (app.DatabaseURL{Driver: "modernc.org/sqlite", Database: want}) {
			t.Errorf("%q: %+v %v", raw, got, err)
		}
	}
	if _, err := sqlite.Engine().Describe(secret.FromString("file:?x=1")); err == nil {
		t.Error("a URL without a file")
	}
	if sqlite.Engine().Dialect() != app.DialectSQLite {
		t.Error(sqlite.Engine().Dialect())
	}
}

// The engine opens its file in WAL mode with a busy timeout, and its
// transactions take the write lock when they begin: two writers queue
// rather than fail.
func TestTheFileIsOpenedForWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	db := open(t, path)
	var mode string
	var busy int
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q: %v", mode, err)
	}
	if err := db.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("busy timeout %d: %v", busy, err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE entries (id TEXT PRIMARY KEY, n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	// Two pools, as two processes would have: each transaction reads, then
	// writes — the upgrade a deferred transaction fails on.
	other := open(t, path)
	var wg sync.WaitGroup
	errsOf := make([]error, 2)
	keys := []string{"a", "b"}
	for i, pool := range []*stdsql.DB{db, other} {
		wg.Go(func() {
			tx, err := pool.BeginTx(t.Context(), nil)
			if err != nil {
				errsOf[i] = err
				return
			}
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM entries").Scan(&n); err != nil {
				errsOf[i] = errors.Join(err, tx.Rollback())
				return
			}
			time.Sleep(50 * time.Millisecond)
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO entries VALUES (?, ?)", keys[i], n); err != nil {
				errsOf[i] = errors.Join(err, tx.Rollback())
				return
			}
			errsOf[i] = tx.Commit()
		})
	}
	wg.Wait()
	if err := errors.Join(errsOf...); err != nil {
		t.Fatalf("two writers: %v", err)
	}
}

type entry struct {
	ID string `json:"id"`
}

// ledger is a product with a SQLite database, in production, its data in
// dir.
func ledger(t *testing.T, dir string, opts ...app.DatabaseOption) *app.App {
	t.Helper()
	t.Setenv("LEDGER_SECRETS", "memory")
	audit := app.NewService("audit", "The audit trail.")
	audit.Store("trail", func(e entry) string { return e.ID })
	return app.NewApp("ledger", audit).With(
		app.Database("archive", sqlite.Engine(), opts...),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard),
	)
}

// Without a URL, the database is <data>/<name>.sqlite: it opens, answers
// its check and is ready.
func TestTheFileLandsBesideTheData(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	a := ledger(t, dir)
	run(t, a)
	if _, err := os.Stat(filepath.Join(dir, "archive.sqlite")); err != nil {
		t.Fatalf("the file beside the data: %v", err)
	}
	g := a.Graph()
	d := g.Runtime.Databases[0]
	type seen struct {
		state, engine, from, tls string
		ready                    bool
	}
	if got := (seen{d.State, d.Engine, d.URLFrom, d.TLS, d.Ready}); got != (seen{model.DatabaseOpen, "sqlite", model.SettingDefault, "", true}) {
		t.Errorf("runtime %+v", d)
	}
	if c := containerOf(g, "container:database:archive"); c == nil || c.Technology != "SQLite · modernc.org/sqlite" {
		t.Errorf("container %+v", c)
	}
	if s := status(t, a.URL(), "/_app/health/ready"); s != http.StatusOK {
		t.Errorf("ready: %d", s)
	}
}

// containerOf is the architecture's container id, or nil.
func containerOf(g *model.Graph, id string) *model.Container {
	for i := range g.Architecture.Containers {
		if c := &g.Architecture.Containers[i]; c.ID == id {
			return c
		}
	}
	return nil
}

// With a URL, the database is the URL's file.
func TestTheURLNamesTheFile(t *testing.T) {
	needsFileStore(t)
	elsewhere := filepath.Join(t.TempDir(), "kept.sqlite")
	t.Setenv("LEDGER_ARCHIVE_URL", elsewhere)
	a := ledger(t, t.TempDir())
	run(t, a)
	if _, err := os.Stat(elsewhere); err != nil {
		t.Fatalf("the URL's file: %v", err)
	}
	if d := a.Graph().Runtime.Databases[0]; d.URLFrom != model.SettingEnv {
		t.Errorf("runtime %+v", d)
	}
}

// lifecycle is what run needs of an app.
type lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// run starts app and stops it with the test.
func run(t *testing.T, a lifecycle) {
	t.Helper()
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Stop(context.Background()); err != nil {
			t.Logf("stop: %v", err)
		}
	})
}

// status is the status of GET path on the app at base.
func status(t *testing.T, base, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Logf("closing the body: %v", err)
	}
	return resp.StatusCode
}

// A SQLite database runs its migrations — kit's own, which make its stores'
// tables, then the product's — under the file's own lock (the SDK's ADR
// 0140), and its stores live in it.
func TestMigrationsAndStoresRunOnTheFile(t *testing.T) {
	needsFileStore(t)
	m := app.Migration{Version: 1, Name: "create", Up: app.Statements("CREATE TABLE x (id TEXT)"), Down: app.Statements("DROP TABLE x")}
	dir := t.TempDir()
	a := ledger(t, dir, app.Migrations(m))
	run(t, a)
	g := a.Graph()
	d := g.Runtime.Databases[0]
	var sets []string
	for _, s := range d.Migrations {
		sets = append(sets, s.Name)
		if len(s.Pending) != 0 || s.Problem != "" {
			t.Errorf("set %+v", s)
		}
	}
	if !slices.Equal(sets, []string{"kit", "product"}) {
		t.Errorf("the sets: %v", sets)
	}
	if st := g.Node("audit/store/trail").Store; st.Backend != "sqlite" || st.Table != "audit__trail" {
		t.Errorf("the store: %+v", st)
	}
	db := open(t, filepath.Join(dir, "archive.sqlite"))
	var n int
	if err := db.QueryRowContext(t.Context(), countTables).Scan(&n); err != nil || n == 0 {
		t.Errorf("the store's table in the file: %d %v", n, err)
	}
}

// The stores' conformance suite, on SQLite: no server, so it always runs.
func TestStoresConform(t *testing.T) {
	storetest.Run(t, storetest.BackendConfig{Name: "sqlite", Options: func(t *testing.T, _ string) []app.AppOption {
		needsFileStore(t)
		return []app.AppOption{app.DataDir(t.TempDir()), app.Database("database", sqlite.Engine())}
	}})
}

// patient is a person's file: who they are, and their name, sealed where
// they rest.
type patient struct {
	ID    string `json:"id"`
	Email string `json:"email" app:"subject"`
	Name  string `json:"name" app:"personal"`
}

// A store that seals rests on SQLite as it rests in files: its row holds
// boxes where the members kit seals were, and reads back as it was written,
// after a restart too. An erasure in a transaction that rolls back destroys
// nothing; one that commits destroys the person's data key, and their row
// as a backup kept it then reads as empty, never as an error.
func TestASealedStoreRoundTripsAndErases(t *testing.T) {
	needsFileStore(t)
	t.Setenv("CLINIC_SECRETS", "memory")
	t.Setenv("CLINIC_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("CLINIC_INDEX_KEY", "an index key of more than 16 bytes")
	dir := t.TempDir()
	clinic := app.NewService("clinic", "Patients, sealed where they rest.")
	patients := clinic.Store("patients", func(p patient) string { return p.ID }, app.Purpose("Care for the patients"))
	a := app.NewApp("clinic", clinic).With(app.Database("archive", sqlite.Engine()),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.DataDir(dir), app.Logs(io.Discard))
	run(t, a)
	if st := a.Graph().Node("clinic/store/patients").Store; st.Backend != "sqlite" {
		t.Fatalf("the store: %+v", st)
	}
	ctx := t.Context()
	ann := patient{ID: "p1", Email: "ann@clinic.test", Name: "Annabel Quist"}
	bob := patient{ID: "p2", Email: "bob@clinic.test", Name: "Robert Vale"}
	for _, p := range []patient{ann, bob} {
		if err := patients.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	db := open(t, filepath.Join(dir, "archive.sqlite"))
	var row []byte
	if err := db.QueryRowContext(ctx, `SELECT doc FROM "clinic__patients" WHERE doc_key = ?`, []byte("p1")).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(row, []byte(`"sealed:v1:`)) || bytes.Contains(row, []byte("Annabel")) || bytes.Contains(row, []byte("ann@clinic.test")) {
		t.Fatalf("the row rests in clear: %s", row)
	}
	if got, err := patients.Get(ctx, "p1"); err != nil || got != ann {
		t.Fatalf("the record: %+v %v", got, err)
	}
	erase := func(ctx context.Context) error { return patients.Erase(ctx, "p1", "asked by the person") }
	if err := app.Transact(ctx, func(ctx context.Context) error { return errors.Join(erase(ctx), errTakeBack) }); !errors.Is(err, errTakeBack) {
		t.Fatalf("the erasure taken back: %v", err)
	}
	if got, err := patients.Get(ctx, "p1"); err != nil || got != ann {
		t.Fatalf("after the erasure taken back: %+v %v", got, err)
	}
	if err := app.Transact(ctx, erase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE "clinic__patients" SET doc = ? WHERE doc_key = ?`, row, []byte("p1")); err != nil {
		t.Fatal(err)
	}
	if got, err := patients.Get(ctx, "p1"); err != nil || got != (patient{ID: "p1"}) {
		t.Fatalf("the row a backup kept, once the key is destroyed: %+v %v", got, err)
	}
	stopped, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(stopped); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := patients.Get(ctx, "p2"); err != nil || got != bob {
		t.Fatalf("after a restart: %+v %v", got, err)
	}
}
