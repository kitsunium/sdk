package sqlite_test

import (
	"slices"
	"testing"
)

// `<product> watch replay WATCH` (ADR 0178 C6) on a real SQLite file: with
// the product stopped, the command queues in the watch's table one notice per
// memo of the store that feeds it, and the next start hears them — a memo no
// watch heard among them, since the run that wrote it mounted none and
// stopped cleanly, leaving no mark to catch up on.
func TestTheWatchCommandReplaysOnSQLite(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	blind := newWatcher(t, dir, false)
	startStop(t, blind.app, func() {
		for _, id := range []string{"m2", "m1"} {
			if err := blind.memos.Insert(t.Context(), memo{ID: id, Text: "unheard"}); err != nil {
				t.Fatal(err)
			}
		}
	})

	replaying := newWatcher(t, dir, true)
	if code := replaying.app.Main(t.Context(), []string{"watch", "replay", "bureau/subscription/screen"}); code != 0 {
		t.Fatalf("watch replay exited %d; want 0", code)
	}
	if code := replaying.app.Main(t.Context(), []string{"watch", "replay", "bureau/subscription/nothing"}); code != 1 {
		t.Errorf("watch replay of no watch exited %d; want 1", code)
	}

	d := newWatcher(t, dir, true)
	startStop(t, d.app, func() { d.settle(t, "after") })
	heard := d.heard()
	for _, id := range []string{"m1", "m2"} {
		if !slices.Contains(heard, id) {
			t.Errorf("after watch replay, the watch heard %v; want %s among them", heard, id)
		}
	}
}
