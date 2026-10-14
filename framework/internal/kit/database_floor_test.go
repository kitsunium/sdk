package kit_test

import (
	"context"
	stdsql "database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The floors kit holds a database to at its start (ADR 0178 C2): a MySQL
// server leases with SKIP LOCKED — MySQL 8.0.1, MariaDB 10.6 —, and a SQLite
// engine waits for a lock under <name>-timeout.

// A MySQL server below the floor refuses the start, naming the server and
// the version it read, never the rest of its answer; one at or above it is
// not refused for its version.
func TestAMySQLServerBelowTheFloorRefusesTheStart(t *testing.T) {
	for answer, refused := range map[string]string{
		"5.7.44-log":     "MySQL 5.7.44",
		"8.0.0-dmr":      "MySQL 8.0.0",
		"10.5.8-MariaDB": "MariaDB 10.5.8",
		"5.5.5-10.4.32-MariaDB-1:10.4.32+maria~ubu2004": "MariaDB 10.4.32",
		"banana": "does not say a MySQL version",
		"8.0.1":  "",
		"8.4.3":  "",
		"10.6.12-MariaDB-1:10.6.12+maria~ubu2004": "",
		"11.4.2-MariaDB": "",
	} {
		t.Run(answer, func(t *testing.T) {
			db := kit.NewFakeDB(sql.DialectMySQL)
			db.SetVersion(answer)
			t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
			var logs syncBuffer
			app := floorApp(t, &logs, kit.Database("database", db.Engine()))
			err := app.Start(t.Context())
			if err == nil {
				stopped, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if serr := app.Stop(stopped); serr != nil {
					t.Logf("stop: %v", serr)
				}
			}
			switch {
			case refused == "" && errs.HasCode(err, kit.CodeDatabaseVersion):
				t.Errorf("a server at the floor was refused: %v", err)
			case refused != "" && (!errs.HasCode(err, kit.CodeDatabaseVersion) || !strings.Contains(err.Error(), refused)):
				t.Errorf("a server below the floor: %v, want %q", err, refused)
			case refused != "" && strings.Contains(answer, "ubu2004") && strings.Contains(err.Error(), "ubu2004"):
				t.Errorf("the refusal quotes the server's answer: %v", err)
			}
		})
	}
}

// floorApp is the ledger in production with no data directory, its secrets
// in memory: the server's version is checked when its database opens, which
// needs no file store — on Windows and Plan 9 the SDK refuses one (vfs,
// ADR 0018), and a data directory would refuse the start before the floor
// is reached.
func floorApp(t *testing.T, logs *syncBuffer, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	l := newLedger()
	return kit.NewApp("ledger", l.books, l.audit).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(logs),
	}, opts...)...)
}

// waiting is an engine whose connections wait for a lock: what kit gave it.
type waiting struct {
	kit.Engine
	mu    sync.Mutex
	waits []time.Duration
}

// OpenWaiting records the wait and opens as the fake does.
func (w *waiting) OpenWaiting(url secret.Value, current func(context.Context) (secret.Value, error), wait time.Duration) (*stdsql.DB, error) {
	w.mu.Lock()
	w.waits = append(w.waits, wait)
	w.mu.Unlock()
	return w.Open(url, current)
}

// An engine whose connections wait for a lock is opened with a wait under
// <name>-timeout: T − max(250ms, T/10).
func TestALockWaiterWaitsUnderTheCallsBound(t *testing.T) {
	needsFileStore(t)
	for timeout, want := range map[string]time.Duration{
		"":      4500 * time.Millisecond,
		"30s":   27 * time.Second,
		"500ms": 250 * time.Millisecond,
		"2s":    1750 * time.Millisecond,
	} {
		t.Run(timeout, func(t *testing.T) {
			if timeout != "" {
				t.Setenv("LEDGER_ARCHIVE_TIMEOUT", timeout)
			}
			engine := &waiting{Engine: kit.NewFakeDB(sql.DialectSQLite).Engine()}
			var logs syncBuffer
			run(t, ledgerApp(t, newLedger(), &logs, kit.Database("archive", engine)))
			engine.mu.Lock()
			defer engine.mu.Unlock()
			if len(engine.waits) != 1 || engine.waits[0] != want {
				t.Errorf("the engine was opened waiting %v, want %s", engine.waits, want)
			}
		})
	}
}
