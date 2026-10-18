package sqlite_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
)

// Records.Keys and the watches' catch-up (ADR 0178 C6) on a real SQLite
// file: a watch lists the keys of the store it hears, through the SDK's
// document store over SQL, and after a stop that was not clean its next start
// hears every memo — the one whose notice was lost among them —; after a
// clean stop it hears nothing again.

// watcher is a bureau whose watch lists the memos' keys when it hears the
// memo "list", and keeps what it heard.
type watcher struct {
	app   *app.App
	memos *app.Store[memo]

	mu      sync.Mutex
	noticed []string
	listed  []string
	listErr error
}

// newWatcher declares the bureau with its watch, or without it — a run that
// writes memos no watch hears —, its data in dir, on a SQLite file.
func newWatcher(t *testing.T, dir string, watching bool) *watcher {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	d := &watcher{}
	svc := app.NewService("bureau", "Memos, screened.")
	d.memos = svc.Store("memos", func(m memo) string { return m.ID })
	if watching {
		svc.Watch("screen", app.Moderated, d.screen, app.OwnStores())
	}
	d.app = app.NewApp("bureau", svc).With(append(bureauOptions(dir), onSQLite())...)
	return d
}

// screen keeps the key a notice names; told of "list", it lists the store's
// keys through the records port first, and keeps the key once the listing is
// done: settle returns when the key is kept, and the app stops after it, so a
// key kept before its listing ended let the stop cancel the listing.
func (d *watcher) screen(ctx context.Context, w app.Written) error {
	if w.Key == "list" {
		defer d.notice(w.Key)
		return d.list(ctx, w)
	}
	d.notice(w.Key)
	return nil
}

// notice keeps the key a notice named.
func (d *watcher) notice(key string) {
	d.mu.Lock()
	d.noticed = append(d.noticed, key)
	d.mu.Unlock()
}

// list lists the store's keys through the records port, and keeps them or
// the error that ended the listing.
func (d *watcher) list(ctx context.Context, w app.Written) error {
	recs, err := app.RecordsOf(ctx, w.Store)
	if err != nil {
		return err
	}
	var keys []string
	for key, err := range recs.Keys(ctx) {
		if err != nil {
			d.mu.Lock()
			d.listErr = err
			d.mu.Unlock()
			return nil
		}
		keys = append(keys, key)
	}
	d.mu.Lock()
	d.listed = keys
	d.mu.Unlock()
	return nil
}

// heard is what the watch heard so far.
func (d *watcher) heard() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.noticed)
}

// settle writes the memo id and waits for its notice: one consumer hears its
// notices in the order they were queued, so whatever was queued before it —
// a catch-up's notices among them — was heard.
func (d *watcher) settle(t *testing.T, id string) {
	t.Helper()
	if err := d.memos.Insert(t.Context(), memo{ID: id, Text: "settle"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(d.heard(), id) {
		if time.Now().After(deadline) {
			t.Fatalf("%s not heard: %v", id, d.heard())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startStop starts app, runs fn, and stops it.
func startStop(t *testing.T, a *app.App, fn func()) {
	t.Helper()
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	fn()
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// runningMark is where the watch's mark rests in the data directory.
func runningMark(dir string) string { return filepath.Join(dir, "bureau", "watches", "screen.running") }

// A watch lists the keys of the store it hears, in key order, on SQLite.
func TestAWatchListsTheKeysOfItsStoreOnSQLite(t *testing.T) {
	needsFileStore(t)
	d := newWatcher(t, t.TempDir(), true)
	startStop(t, d.app, func() {
		for _, id := range []string{"m2", "m1"} {
			if err := d.memos.Insert(t.Context(), memo{ID: id, Text: "hello"}); err != nil {
				t.Fatal(err)
			}
		}
		d.settle(t, "list")
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listErr != nil || !slices.Equal(d.listed, []string{"list", "m1", "m2"}) {
		t.Errorf("Keys from the watch = %q, %v; want [list m1 m2]", d.listed, d.listErr)
	}
}

// A memo written while no notice could reach the watch — a run that died
// between the write and its notice, leaving the watch's running mark — is
// heard at the next start. One process cannot die and start again: the
// first run mounts no watch, so its write is noticed by nobody, and the mark
// is put where a dying process leaves it.
func TestAWatchCatchesUpOnSQLiteAfterAStopThatWasNotClean(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	blind := newWatcher(t, dir, false)
	startStop(t, blind.app, func() {
		if err := blind.memos.Insert(t.Context(), memo{ID: "lost", Text: "unheard"}); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.MkdirAll(filepath.Dir(runningMark(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runningMark(dir), []byte("bureau/subscription/screen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newWatcher(t, dir, true)
	startStop(t, d.app, func() { d.settle(t, "after") })
	if !slices.Contains(d.heard(), "lost") {
		t.Errorf("after a stop that was not clean, the watch heard %v; want the lost memo", d.heard())
	}
}

// A clean stop takes the mark off: the next start hears nothing again.
func TestACleanStopOnSQLiteCatchesNothingUp(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	first := newWatcher(t, dir, true)
	startStop(t, first.app, func() { first.settle(t, "heard") })
	if _, err := os.Stat(runningMark(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after a clean stop, the running mark: %v; want it gone", err)
	}
	second := newWatcher(t, dir, true)
	startStop(t, second.app, func() { second.settle(t, "after") })
	if slices.Contains(second.heard(), "heard") {
		t.Errorf("a memo heard before a clean stop was heard again: %v", second.heard())
	}
}
