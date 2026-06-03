package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// staticCreds is a CredentialProvider returning fixed login material for the
// happy-path white-box tests.
type staticCreds struct{}

func (staticCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: fixed user/password drives the DSN-building path deterministically.
	return writer.NewCredentialValue("logger", "s3cr3t", ""), nil
}

// failCreds is a CredentialProvider that always fails, exercising the
// credential-resolution error branch.
type failCreds struct{}

func (failCreds) Credentials(context.Context) (writer.CredentialValue, error) {
	//: a fixed failure drives the credential-error branch.
	return writer.CredentialValue{}, errs.Wrap(nil, errs.WrapParams{
		Code: CodeMySQLClientInitFailed, Reason: "CLIENT_INIT_FAILED",
		Public: "no creds", Private: "test failCreds",
	})
}

func Test_newClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.MySQLConfig
		wantErr bool
	}{
		{"nil credentials rejected", writer.MySQLConfig{Table: "logs"}, true},
		{"invalid table rejected", writer.MySQLConfig{Table: "bad table;", Credentials: staticCreds{}}, true},
		{"credential failure rejected", writer.MySQLConfig{Table: "logs", Credentials: failCreds{}}, true},
		{"valid config opens a lazy handle", writer.MySQLConfig{SocketPath: "/tmp/m.sock", Database: "d", Table: "logs", Credentials: staticCreds{}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, exec, err := newClient(tc.cfg)
			//: error arm — client-init sentinel, nil handle + closure.
			if tc.wantErr {
				if db != nil || exec != nil || !errs.HasCode(err, CodeMySQLClientInitFailed) {
					t.Fatalf("%s: db=%v execNil=%v err=%v want nil+client-init", tc.name, db, exec == nil, err)
				}
				return
			}
			//: happy arm — a lazy handle + a non-nil deliver closure.
			if err != nil || db == nil || exec == nil {
				t.Fatalf("%s: db=%v execNil=%v err=%v want handle+closure", tc.name, db, exec == nil, err)
			}
			//: release the lazily-opened pool (no connection was made).
			if cerr := db.Close(); cerr != nil {
				t.Errorf("%s: db.Close: %v", tc.name, cerr)
			}
		})
	}
}

func Test_execClosure_emptyBatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		batch []corelogger.RecordEvent
	}{
		{"nil batch is a no-op", nil},
		{"empty slice is a no-op", []corelogger.RecordEvent{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a valid offline config yields the real deliver closure.
			db, exec, err := newClient(writer.MySQLConfig{SocketPath: "/tmp/m.sock", Database: "d", Table: "logs", Credentials: staticCreds{}})
			if err != nil {
				t.Fatalf("%s: newClient: %v", tc.name, err)
			}
			t.Cleanup(func() {
				//: release the lazily-opened pool (no connection was made).
				if cerr := db.Close(); cerr != nil {
					t.Errorf("%s: db.Close: %v", tc.name, cerr)
				}
			})
			//: the empty-batch guard returns nil before touching the database.
			if gerr := exec(t.Context(), tc.batch); gerr != nil {
				t.Errorf("%s: exec(empty)=%v want nil", tc.name, gerr)
			}
		})
	}
}

func Test_execClosure_insertSuccess(t *testing.T) {
	//: mutates the package-global fakeShared state — must not run in parallel.
	tests := []struct {
		name     string
		batch    []corelogger.RecordEvent
		wantArgs int
		wantCols []driver.Value
	}{
		{
			"one record binds ts/level/message in column order",
			[]corelogger.RecordEvent{{Time: time.Unix(0, 0).UTC(), Level: level.Info, Message: "hello"}},
			colsPerRow,
			[]driver.Value{time.Unix(0, 0).UTC(), "INFO", "hello"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			//: shares the package-global fake driver state — not parallel-safe.
			fakeShared.failExec = false
			db := openFakeDB(t)
			t.Cleanup(func() {
				//: release the fake pool after the case.
				if cerr := db.Close(); cerr != nil {
					t.Errorf("%s: db.Close: %v", tc.name, cerr)
				}
			})
			//: the fake-driver round-trip must report a clean INSERT.
			if gerr := fakeExec(t.Context(), db, "app_logs", tc.batch); gerr != nil {
				t.Fatalf("%s: fakeExec=%v want nil", tc.name, gerr)
			}
			//: the captured args prove the flattened (ts, level, message) order.
			if len(fakeShared.lastArgs) != tc.wantArgs {
				t.Fatalf("%s: len(args)=%d want %d", tc.name, len(fakeShared.lastArgs), tc.wantArgs)
			}
			for i, want := range tc.wantCols {
				//: each bound column must match in position.
				if fakeShared.lastArgs[i] != want {
					t.Errorf("%s: arg[%d]=%v want %v", tc.name, i, fakeShared.lastArgs[i], want)
				}
			}
		})
	}
}

func Test_execClosure_insertError(t *testing.T) {
	//: mutates the package-global fakeShared state — must not run in parallel.
	tests := []struct {
		name  string
		batch []corelogger.RecordEvent
	}{
		{"driver Exec failure surfaces as InsertFailed", []corelogger.RecordEvent{{Message: "x", Level: level.Error}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			//: shares the package-global fake driver state — not parallel-safe.
			fakeShared.failExec = true
			t.Cleanup(func() {
				//: leave the shared driver clean for any later case.
				fakeShared.failExec = false
			})
			db := openFakeDB(t)
			t.Cleanup(func() {
				//: release the fake pool after the case.
				if cerr := db.Close(); cerr != nil {
					t.Errorf("%s: db.Close: %v", tc.name, cerr)
				}
			})
			gerr := fakeExec(t.Context(), db, "app_logs", tc.batch)
			//: a driver failure must wrap into the insert sentinel.
			if !errs.HasCode(gerr, CodeMySQLInsertFailed) {
				t.Errorf("%s: err=%v want InsertFailed", tc.name, gerr)
			}
			//: the underlying driver cause must stay reachable via errors.Is.
			if !errors.Is(gerr, errFakeExec) {
				t.Errorf("%s: errors.Is lost the driver cause", tc.name)
			}
		})
	}
}

func Test_buildDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     writer.MySQLConfig
		needles []string
	}{
		{"unix socket dsn", writer.MySQLConfig{SocketPath: "/tmp/m.sock", Database: "logs"}, []string{"unix(", "/tmp/m.sock", "logs", "logger:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dsn := buildDSN(tc.cfg, "logger", "s3cr3t")
			//: the formatted DSN must select net=unix at the socket path.
			for _, needle := range tc.needles {
				if !strings.Contains(dsn, needle) {
					t.Errorf("%s: dsn %q missing %q", tc.name, dsn, needle)
				}
			}
		})
	}
}

func Test_buildInsert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		batch    []corelogger.RecordEvent
		wantArgs int
		needles  []string
	}{
		{"two rows", []corelogger.RecordEvent{{Message: "a", Level: level.Info}, {Message: "b", Level: level.Warn}}, 6, []string{"INSERT INTO logs (ts, level, message) VALUES ", "(?,?,?),(?,?,?)"}},
		{"one row", []corelogger.RecordEvent{{Message: "x", Level: level.Error, Time: time.Unix(0, 0)}}, 3, []string{"(?,?,?)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			query, args := buildInsert("logs", tc.batch)
			//: the arg list flattens to colsPerRow per record.
			if len(args) != tc.wantArgs {
				t.Errorf("%s: len(args)=%d want %d", tc.name, len(args), tc.wantArgs)
			}
			//: the rendered statement must contain the expected fragments.
			for _, needle := range tc.needles {
				if !strings.Contains(query, needle) {
					t.Errorf("%s: query %q missing %q", tc.name, query, needle)
				}
			}
		})
	}
}

func Test_isIdentRune(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"letter", 'a', true},
		{"digit", '7', true},
		{"underscore", '_', true},
		{"space rejected", ' ', false},
		{"semicolon rejected", ';', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: only the identifier alphabet is accepted.
			if got := isIdentRune(tc.r); got != tc.want {
				t.Errorf("%s: isIdentRune(%q)=%v want %v", tc.name, tc.r, got, tc.want)
			}
		})
	}
}

func Test_validIdent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain identifier", "app_logs", true},
		{"empty rejected", "", false},
		{"space rejected", "app logs", false},
		{"semicolon rejected", "logs;drop", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: only plain identifiers are safe to interpolate into SQL.
			if got := validIdent(tc.in); got != tc.want {
				t.Errorf("%s: validIdent(%q)=%v want %v", tc.name, tc.in, got, tc.want)
			}
		})
	}
}

// fakeExecResult is the driver.Result the fake statement returns on a clean
// Exec; the writer ignores the values, so fixed numbers are enough.
type fakeExecResult struct{}

func (fakeExecResult) LastInsertId() (int64, error) {
	//: the writer discards the insert id — a fixed value suffices.
	return 0, nil
}

func (fakeExecResult) RowsAffected() (int64, error) {
	//: the writer discards the affected count — a fixed value suffices.
	return 0, nil
}

// errFakeExec is the canned driver-side failure used to drive the INSERT error
// path; the test asserts it stays reachable via errors.Is through wrapInsert.
var errFakeExec = errors.New("fake driver exec boom")

// fakeStmt is the prepared statement the fake conn hands back. It captures the
// last Exec args so a test can assert the bound (ts, level, message) ordering,
// and returns either a result or the canned error per the shared fake state.
type fakeStmt struct {
	// shared points at the per-driver state so captured args + the error toggle
	// survive across the new statement the pool prepares for each Exec.
	shared *fakeState
}

func (s *fakeStmt) Close() error {
	//: nothing to release — the fake holds no real handle.
	return nil
}

func (s *fakeStmt) NumInput() int {
	//: -1 disables the driver's arg-count check so any batch width is accepted.
	return -1
}

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	//: record the flattened bound values so the test can assert column order.
	s.shared.lastArgs = args
	//: the error toggle drives the wrapInsert path when set.
	if s.shared.failExec {
		//: surface the canned cause so errors.Is can reach it through the wrap.
		return nil, errFakeExec
	}
	//: a clean Exec returns the no-op result.
	return fakeExecResult{}, nil
}

func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	//: the writer only ever Execs an INSERT — Query must never be reached.
	return nil, errors.New("fake driver: Query unsupported")
}

// fakeConn is the connection the fake driver opens; every Prepare yields a
// statement bound to the same shared state.
type fakeConn struct {
	// shared is threaded into each prepared statement.
	shared *fakeState
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	//: hand back a statement that writes through to the shared capture state.
	return &fakeStmt{shared: c.shared}, nil
}

func (c *fakeConn) Close() error {
	//: nothing to release.
	return nil
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	//: the writer never opens a transaction — fail loudly if it ever does.
	return nil, errors.New("fake driver: Begin unsupported")
}

// fakeState is the per-driver mutable state shared with every conn + stmt so a
// test can flip failExec before an Exec and read lastArgs after it.
type fakeState struct {
	// lastArgs is the flattened arg slice from the most recent Exec.
	lastArgs []driver.Value
	// failExec, when true, makes every Exec return errFakeExec.
	failExec bool
}

// fakeDriver opens connections wired to one shared state instance, so the test
// observes captures made on the pool's connection.
type fakeDriver struct {
	// shared is handed to every connection this driver opens.
	shared *fakeState
}

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	//: accept any DSN — the offline fake never parses it.
	return &fakeConn{shared: d.shared}, nil
}

// fakeShared is the single state the registered fake driver writes through; a
// test reads lastArgs / sets failExec on it.
var fakeShared = &fakeState{}

// registerFakeOnce guards the one-time sql.Register so tests sharing the driver
// name never double-register (which panics).
var registerFakeOnce sync.Once

// openFakeDB registers the fake driver once and opens a lazy *sql.DB against it.
// The handle drives buildInsert through a real database/sql path without a
// MySQL server, so the captured args prove the (ts, level, message) ordering.
func openFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	//: register exactly once — a second sql.Register("mysqlfake", …) panics.
	registerFakeOnce.Do(func() {
		sql.Register("mysqlfake", &fakeDriver{shared: fakeShared})
	})
	db, err := sql.Open("mysqlfake", "ignored-dsn")
	//: sql.Open is lazy — it must succeed offline against the fake driver.
	if err != nil {
		t.Fatalf("sql.Open(mysqlfake): %v", err)
	}
	return db
}

// ExecContexter is the minimal database surface fakeExec needs; depending on it
// (instead of *sql.DB) keeps the helper coupled to the one method it calls.
type ExecContexter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// fakeExec mirrors the production exec closure (client.go) against an injected
// database handle: buildInsert → ExecContext → wrapInsert on failure. The
// production closure hardcodes sql.Open("mysql", …) so the fake driver cannot
// reach it; this helper exercises the identical seam with the fake injected.
func fakeExec(ctx context.Context, db ExecContexter, table string, batch []corelogger.RecordEvent) error {
	//: an empty batch is a no-op, matching the closure's guard.
	if len(batch) == 0 {
		//: nothing to insert.
		return nil
	}
	query, args := buildInsert(table, batch)
	//: a failed INSERT surfaces under the insert sentinel with the row count.
	if _, eerr := db.ExecContext(ctx, query, args...); eerr != nil {
		//: wrapInsert never echoes the payload.
		return wrapInsert(eerr, len(batch))
	}
	//: the batch landed.
	return nil
}
