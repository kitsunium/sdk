package sqlite_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
)

// SQLite's busy timeout against kit's call bound (ADR 0178 C2): kit gives
// the file B = T − max(250ms, T/10), T being <name>-timeout, refuses a T
// under 500ms, and refuses a URL whose own busy timeout reaches T — it never
// rewrites one.

// A <name>-timeout under 500ms refuses the start, naming the setting and the
// floor.
func TestASQLiteTimeoutUnderItsFloorRefusesTheStart(t *testing.T) {
	needsFileStore(t)
	t.Setenv("LEDGER_ARCHIVE_TIMEOUT", "400ms")
	err := ledger(t, t.TempDir()).Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "archive-timeout") || !strings.Contains(err.Error(), "500ms") {
		t.Fatalf("a SQLite timeout of 400ms started: %v", err)
	}
}

// A URL whose busy timeout — under either spelling the driver reads —
// reaches <name>-timeout refuses the start, naming the value it gives and
// the bound; one under it starts, as written.
func TestAURLsBusyTimeoutStaysUnderTheCallsBound(t *testing.T) {
	needsFileStore(t)
	for query, refused := range map[string]bool{
		"_busy_timeout=5000":               true,
		"_busy_timeout=4000&_timeout=6000": true,
		"_timeout=9000":                    true,
		"_busy_timeout=4999":               false,
		"_busy_timeout=9000&_timeout=4000": false,
	} {
		t.Run(query, func(t *testing.T) {
			t.Setenv("LEDGER_ARCHIVE_URL", "file:"+filepath.Join(t.TempDir(), "kept.sqlite")+"?"+query)
			a := ledger(t, t.TempDir())
			err := a.Start(t.Context())
			if !refused {
				if err != nil {
					t.Fatalf("a busy timeout under the bound: %v", err)
				}
				if err := a.Stop(context.Background()); err != nil {
					t.Logf("stop: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "busy timeout") || !strings.Contains(err.Error(), "archive-timeout") {
				t.Errorf("a busy timeout at the bound started: %v", err)
			}
		})
	}
}

// A write that finds the file's write lock taken waits kit's busy timeout,
// then fails as SQLite's refusal — before the call's own deadline: with a
// <name>-timeout of 2s, the wait is 1.75s.
func TestALostLockComesBackBeforeTheCallsDeadline(t *testing.T) {
	needsFileStore(t)
	t.Setenv("LEDGER_ARCHIVE_TIMEOUT", "2s")
	t.Setenv("KIT_SECRETS", "memory")
	dir := t.TempDir()
	audit := app.NewService("audit", "The audit trail.")
	store := audit.Store("trail", func(e entry) string { return e.ID })
	a := app.NewApp("ledger", audit).With(
		app.Database("archive", sqlite.Engine()),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard),
	)
	run(t, a)
	trail := a.Graph().Node("audit/store/trail")
	if trail == nil || trail.Store.Backend != "sqlite" {
		t.Fatalf("the store is not on the file: %+v", trail)
	}
	// Another writer holds the file's lock: the engine's transactions take
	// it when they begin.
	db := open(t, filepath.Join(dir, "archive.sqlite"))
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Logf("rollback: %v", err)
		}
	}()
	began := time.Now()
	err = store.Insert(t.Context(), entry{ID: "late"})
	waited := time.Since(began)
	if err == nil {
		t.Fatal("a write went through a lock another writer holds")
	}
	if errors.Is(err, context.DeadlineExceeded) || waited < 1600*time.Millisecond || waited >= 2*time.Second {
		t.Errorf("the write gave up after %s (%v): want SQLite's refusal after 1.75s, before the 2s deadline", waited, err)
	}
}
