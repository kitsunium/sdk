package app

import (
	"testing"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// `<product> watch replay WATCH` (ADR 0178 C6), as Main runs it, on the file
// engine and on kit's fake database — the SQLite connector's suite runs it on
// a real file. The forum of records_keys_internal_test.go: the moderation
// module's watch hears the posts.

// replayByMain runs `watch replay` through Main on an app of w's place that
// is not running, as an operator would with the product stopped.
func replayByMain(t *testing.T, w *keysWorld, args ...string) int {
	t.Helper()
	return w.app(nil).Main(t.Context(), append([]string{"watch"}, args...))
}

// cliLists are the journal's list entries the command line wrote.
func cliLists(t *testing.T, app *App) int {
	t.Helper()
	_, j := app.privacyStores()
	if j == nil {
		t.Fatal("the forum keeps personal data, and has no journal")
	}
	entries, err := j.List(t.Context())
	must(t, err)
	n := 0
	for _, e := range entries {
		if e.Op == model.JournalList && e.By == actorCLI {
			n++
		}
	}
	return n
}

// Posts whose notices were lost while the product stopped cleanly — no mark,
// so no catch-up — are heard after `watch replay`, at the next start. Every
// replay queues one notice per record: two replays are heard twice, the
// watch's at-least-once contract, which its idempotent handler tolerates.
// Each replay journals one list entry per store it lists — one here.
func TestTheWatchCommandReplaysEveryRecordOfItsStores(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			first := w.start(t, func(b queue.Broker) queue.Broker { return lossyBroker{b} })
			for _, id := range []string{"p2", "p1"} {
				must(t, w.posts.Insert(t.Context(), keysPost{ID: id, Author: "ann", Body: "unheard"}))
			}
			stop(t, first)

			for range 2 {
				if code := replayByMain(t, w, "replay", w.watch.id); code != 0 {
					t.Fatalf("watch replay %s exited %d; want 0", w.watch.id, code)
				}
			}
			second := w.start(t, nil)
			defer stop(t, second)
			w.settle(t, "after")
			for _, id := range []string{"p1", "p2"} {
				got := w.heardOf(id)
				if len(got) != 2 || got[0].Deleted || got[0].Store != w.posts.id {
					t.Errorf("after two replays, the watch heard %+v of %s; want two notices of a write", got, id)
				}
			}
			if got := cliLists(t, second); got != 2 {
				t.Errorf("two replays of a watch one store feeds journaled %d list entries by the command line; want 2", got)
			}
		})
	}
}

// A replay names a watch the product declares: any other name is refused
// (status 1) and queues nothing; a command line that is not `replay WATCH`
// is a usage error (status 2).
func TestTheWatchCommandRefusesWhatIsNoWatch(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			first := w.start(t, func(b queue.Broker) queue.Broker { return lossyBroker{b} })
			must(t, w.posts.Insert(t.Context(), keysPost{ID: "p1", Author: "ann", Body: "unheard"}))
			stop(t, first)

			refused := map[string][]string{
				"a store":             {"replay", w.posts.id},
				"no watch at all":     {"replay", "screening/subscription/nothing"},
				"a service's name":    {"replay", "screening"},
				"a watch's bare name": {"replay", "content"},
			}
			for why, args := range refused {
				if code := replayByMain(t, w, args...); code != 1 {
					t.Errorf("watch %q (%s) exited %d; want 1", args, why, code)
				}
			}
			for _, args := range [][]string{nil, {"replay"}, {"list", w.watch.id}, {"replay", w.watch.id, "more"}, {"replay", "-h"}} {
				if code := replayByMain(t, w, args...); code != 2 {
					t.Errorf("watch %q exited %d; want the usage error 2", args, code)
				}
			}
			second := w.start(t, nil)
			defer stop(t, second)
			w.settle(t, "after")
			if got := w.heardOf("p1"); len(got) != 0 {
				t.Errorf("refused replays queued %d notices of p1; want none", len(got))
			}
			if got := cliLists(t, second); got != 0 {
				t.Errorf("refused replays journaled %d list entries; want none", got)
			}
		})
	}
}
