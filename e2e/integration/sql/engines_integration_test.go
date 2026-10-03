//go:build integration

// Package sql_test runs the SDK's SQL mechanisms on the three real engines,
// through real drivers: the document store over SQL (pkg/v1/data/docstore), the
// queue's SQL broker (pkg/v1/data/queue), and the transactor's and migration
// runner's contracts (pkg/v1/data/sql), SQLite's file lock among them.
//
// The SDK ships no driver and its workspace modules import none (ADR 0055
// §D2), so the default suite runs the same contracts over a fake engine. This
// package is where the SQL meets an engine that parses it. It lives in the
// auxiliary e2e module, outside go.work, so the drivers and testcontainers it
// needs reach no module a consumer requires (ADR 0157), and runs only under
// the integration tag:
//
//	cd e2e && GOWORK=off go test -tags integration -race ./integration/sql/...
//
// SQLite needs nothing (modernc.org/sqlite, no cgo). PostgreSQL 17 and MySQL
// 8.4 are started with testcontainers on free ports and removed at the end;
// without Docker their cases skip, so the lane stays green where Docker is
// absent.
package sql_test

import (
	"context"
	stdsql "database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	tc "github.com/testcontainers/testcontainers-go"
	mysqlc "github.com/testcontainers/testcontainers-go/modules/mysql"
	postgresc "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// engine is one database the suite runs on.
type engine struct {
	// db is the pool, opened by the engine's own driver.
	db *stdsql.DB
	// name labels the subtests.
	name string
	// dialect is the SQL the SDK speaks to it.
	dialect sql.Dialect
}

// containers are the servers the suite started, removed by TestMain.
var (
	containersMu sync.Mutex
	containers   []tc.Container
)

// The two servers, each started once for the whole run.
var (
	postgresOnce, mysqlOnce sync.Once
	postgresDSN, mysqlDSN   string
	postgresErr, mysqlErr   error
)

// nameSeq numbers the tables and version tables the cases create, so cases
// sharing a server never share a table.
var nameSeq atomic.Int64

// TestMain removes every container the run started, whatever the verdict.
func TestMain(m *testing.M) {
	code := m.Run()
	containersMu.Lock()
	for _, c := range containers {
		if err := tc.TerminateContainer(c); err != nil {
			fmt.Fprintf(os.Stderr, "terminate: %v\n", err)
		}
	}
	containersMu.Unlock()
	os.Exit(code)
}

// keep registers a started container for TestMain to remove.
func keep(c tc.Container) {
	containersMu.Lock()
	defer containersMu.Unlock()
	containers = append(containers, c)
}

// uniqueName returns a table name no other case uses.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, nameSeq.Add(1))
}

// sqliteEngine opens a fresh SQLite database file for one case. txlock names
// how its transactions begin: "" for SQLite's deferred default, "immediate"
// for the write lock at BEGIN, as a framework's engine may configure.
func sqliteEngine(tb testing.TB, txlock string) *engine {
	tb.Helper()
	dsn := "file:" + filepath.Join(tb.TempDir(), "store.sqlite") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	if txlock != "" {
		dsn += "&_txlock=" + txlock
	}
	db, err := stdsql.Open("sqlite", dsn)
	if err != nil {
		tb.Fatalf("open sqlite: %v", err)
	}
	tb.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			tb.Errorf("close sqlite: %v", cerr)
		}
	})
	name := "sqlite"
	if txlock != "" {
		name += "-" + txlock
	}
	return &engine{db: db, name: name, dialect: sql.DialectSQLite}
}

// postgresEngine opens a pool onto the run's PostgreSQL 17, starting it the
// first time; it skips the case without Docker.
func postgresEngine(tb testing.TB) *engine {
	tb.Helper()
	postgresOnce.Do(func() {
		ctx := context.Background()
		ctr, err := postgresc.Run(ctx, "postgres:17",
			postgresc.WithDatabase("sdk"), postgresc.WithUsername("sdk"), postgresc.WithPassword("sdk"),
			postgresc.BasicWaitStrategies())
		if ctr != nil {
			keep(ctr)
		}
		if err != nil {
			postgresErr = err
			return
		}
		postgresDSN, postgresErr = ctr.ConnectionString(ctx, "sslmode=disable")
	})
	if postgresErr != nil {
		tb.Skipf("PostgreSQL unavailable (Docker?): %v", postgresErr)
	}
	return openServer(tb, "pgx", postgresDSN, "postgres", sql.DialectPostgres)
}

// mysqlEngine opens a pool onto the run's MySQL 8.4, starting it the first
// time; it skips the case without Docker.
func mysqlEngine(tb testing.TB) *engine {
	tb.Helper()
	mysqlOnce.Do(func() {
		ctx := context.Background()
		ctr, err := mysqlc.Run(ctx, "mysql:8.4",
			mysqlc.WithDatabase("sdk"), mysqlc.WithUsername("sdk"), mysqlc.WithPassword("sdk"))
		if ctr != nil {
			keep(ctr)
		}
		if err != nil {
			mysqlErr = err
			return
		}
		mysqlDSN, mysqlErr = ctr.ConnectionString(ctx)
	})
	if mysqlErr != nil {
		tb.Skipf("MySQL unavailable (Docker?): %v", mysqlErr)
	}
	return openServer(tb, "mysql", mysqlDSN, "mysql", sql.DialectMySQL)
}

// openServer opens a pool onto a server for one case.
func openServer(tb testing.TB, driverName, dsn, name string, dialect sql.Dialect) *engine {
	tb.Helper()
	db, err := stdsql.Open(driverName, dsn)
	if err != nil {
		tb.Fatalf("open %s: %v", name, err)
	}
	tb.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			tb.Errorf("close %s: %v", name, cerr)
		}
	})
	return &engine{db: db, name: name, dialect: dialect}
}

// eachEngine runs fn on SQLite, PostgreSQL and MySQL, each in a subtest of its
// own that a missing Docker skips.
func eachEngine(t *testing.T, fn func(t *testing.T, e *engine)) {
	t.Helper()
	for name, open := range map[string]func(testing.TB) *engine{
		"sqlite":   func(tb testing.TB) *engine { return sqliteEngine(tb, "") },
		"postgres": postgresEngine,
		"mysql":    mysqlEngine,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, open(t))
		})
	}
}

// transactor returns the SDK's transactor over e's pool.
func transactor(tb testing.TB, e *engine) sql.Transactor {
	tb.Helper()
	tm, err := sql.NewTransactor(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}})
	if err != nil {
		tb.Fatalf("NewTransactor: %v", err)
	}
	return tm
}

// must fails the test on an error.
func must(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("unexpected error: %v", err)
	}
}
