package kit

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Records.Keys and the watches' catch-up (ADR 0178 C6), on the file engine
// and on a database — kit's fake one; the SQLite connector's suite runs the
// catch-up on a real file. A forum keeps posts: an author, personal, and a
// body, moderated. The moderation module watches what is moderated; the
// archiving module watches nothing.

// keysPost is a forum post: its author is personal, its body moderated.
type keysPost struct {
	ID     string `json:"id"`
	Author string `json:"author" kit:"personal"`
	Body   string `json:"body" kit:"moderated"`
}

// keysWorld is the forum, the two modules, and what the watch heard.
type keysWorld struct {
	posts *StoreService[keysPost]
	shelf *StoreService[forumDraft]
	watch *Watch
	// app declares the forum's app on its place, every queue behind wrap
	// when it is set, and does not start it; start starts one.
	app   func(wrap func(queue.Broker) queue.Broker) *App
	start func(t *testing.T, wrap func(queue.Broker) queue.Broker) *App

	mu    sync.Mutex
	heard []WrittenEvent
}

// keysPlace is where the forum keeps its data: a data directory, and the
// options that place the stores, the same on every start of one test.
type keysPlace struct {
	dir  string
	opts []AppConfigurer
}

// keysEngines are the two places the forum keeps its data: the data
// directory alone, and a database beside it.
func keysEngines(t *testing.T) map[string]keysPlace {
	t.Helper()
	file, db := t.TempDir(), t.TempDir()
	return map[string]keysPlace{
		"file": {dir: file, opts: []AppConfigurer{DataDir(file)}},
		"sql":  {dir: db, opts: []AppConfigurer{DataDir(db), Database("database", NewFakeDB(sql.DialectPostgres).Engine())}},
	}
}

// markOf is where the watch's running mark rests in the place's data
// directory.
func (w *keysWorld) markOf(p keysPlace) string {
	return filepath.Join(p.dir, filepath.FromSlash(w.watch.runningMark()))
}

// newKeysWorld declares the forum and its modules, and returns a world whose
// start runs them with place, every queue behind wrap when it is set.
func newKeysWorld(t *testing.T, place keysPlace) *keysWorld {
	t.Helper()
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("FORUM_DATABASE_URL", "fake://forum:pw@db.internal:5432/forum?tls=verify-full")
	w := &keysWorld{}
	forum := NewService("forum", "Posts: an author, personal, and a body, moderated.")
	w.posts = forum.Store("posts", func(p keysPost) string { return p.ID })
	screening := NewService("screening", "Screens the forum's posts.")
	w.watch = screening.Watch("content", Moderated, func(_ context.Context, e WrittenEvent) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.heard = append(w.heard, e)
		return nil
	})
	moderation := NewModule("moderation", "Moderation, for the tests.", screening)
	archive := NewService("archive", "Keeps drafts, and watches nothing.")
	w.shelf = archive.Store("shelf", func(d forumDraft) string { return d.ID })
	archiving := NewModule("archiving", "A module that watches nothing.", archive)
	w.app = func(wrap func(queue.Broker) queue.Broker) *App {
		opts := append([]AppConfigurer{moderation, archiving, Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard)}, place.opts...)
		app := NewApp("forum", forum).With(opts...)
		app.wrapQueue = wrap
		return app
	}
	w.start = func(t *testing.T, wrap func(queue.Broker) queue.Broker) *App {
		t.Helper()
		app := w.app(wrap)
		must(t, app.Start(t.Context()))
		return app
	}
	return w
}

// heardOf are the notices the watch heard of key.
func (w *keysWorld) heardOf(key string) []WrittenEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(w.heard), func(e WrittenEvent) bool { return e.Key != key })
}

// settle writes a last post and waits for its notice: one consumer hears its
// notices in the order they were queued, so every notice queued before it —
// a catch-up's included — has been heard.
func (w *keysWorld) settle(t *testing.T, key string) {
	t.Helper()
	must(t, w.posts.Insert(t.Context(), keysPost{ID: key, Author: "ann", Body: "last"}))
	eventuallyInternal(t, "the notice of "+key, func() bool { return len(w.heardOf(key)) > 0 })
}

// stop stops app, failing the test on an error.
func stop(t *testing.T, app *App) {
	t.Helper()
	must(t, app.Stop(context.Background()))
}

// collectKeys ranges over seq to its end: the keys, and the one failure.
func collectKeys(seq iter.Seq2[string, error]) ([]string, error) {
	var keys []string
	for key, err := range seq {
		if err != nil {
			return keys, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// journaledLists counts the journal's list entries.
func journaledLists(t *testing.T, app *App) int {
	t.Helper()
	_, j := app.privacyStores()
	if j == nil {
		t.Fatal("the forum keeps personal data, and has no journal")
	}
	entries, err := j.List(t.Context())
	must(t, err)
	n := 0
	for _, e := range entries {
		if e.Op == model.JournalList {
			n++
		}
	}
	return n
}

// Records.Keys lists a store's keys to a module that watches it, in key
// order, and to nobody else: a module that watches nothing, code that runs
// inside no node, and a watching module asking for a store it does not hear
// are refused, Forbidden, with CodeRecordsKeys. One listing is one journal
// entry, whatever number of keys it yields; a refusal journals nothing.
func TestRecordsKeysIsOfferedToAWatchingModuleOnly(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			app := w.start(t, nil)
			defer stop(t, app)
			if onDB := w.posts.onDatabase() != nil; onDB != (name == "sql") {
				t.Fatalf("the posts on a database: %v; want %v", onDB, name == "sql")
			}
			ctx := t.Context()
			for _, id := range []string{"p3", "p1", "p2"} {
				must(t, w.posts.Insert(ctx, keysPost{ID: id, Author: "ann", Body: "hello"}))
			}
			before := journaledLists(t, app)
			refusals := map[string]struct {
				ctx   context.Context
				store string
			}{
				"a module that watches nothing":   {withNode(ctx, w.shelf.id, app), w.posts.id},
				"code inside no node":             {withNode(ctx, "", app), w.posts.id},
				"a store the watch does not hear": {withNode(ctx, w.watch.id, app), w.shelf.id},
			}
			for why, r := range refusals {
				recs, err := RecordsOf(r.ctx, r.store)
				must(t, err)
				keys, err := collectKeys(recs.Keys(r.ctx))
				var ke *Error
				if len(keys) != 0 || !errs.HasCode(err, CodeRecordsKeys) || !errors.As(err, &ke) || ke.Code != WireForbidden {
					t.Errorf("%s: Keys = %q, %v; want nothing and a Forbidden CodeRecordsKeys", why, keys, err)
				}
			}
			if got := journaledLists(t, app); got != before {
				t.Errorf("the refusals journaled %d list entries, want none", got-before)
			}
			watching := withNode(ctx, w.watch.id, app)
			recs, err := RecordsOf(watching, w.posts.id)
			must(t, err)
			keys, err := collectKeys(recs.Keys(watching))
			if err != nil || !slices.Equal(keys, []string{"p1", "p2", "p3"}) {
				t.Fatalf("Keys for the watching module = %q, %v; want [p1 p2 p3]", keys, err)
			}
			if got := journaledLists(t, app); got != before+1 {
				t.Errorf("one listing of three keys journaled %d list entries, want exactly one", got-before)
			}
		})
	}
}

// lossyBroker refuses every publication: a process that died between a
// write and its notice, as the queue sees it.
type lossyBroker struct{ queue.Broker }

// Publish refuses.
func (lossyBroker) Publish(context.Context, []byte) (queue.Message, error) {
	return queue.Message{}, failure(CodeWatchQueue, "QUEUE_DOWN", "the queue is down", nil)
}

// A stop that was not clean between a write and its notice: the write
// stands, its notice never reached the queue, and the process died with the
// watch's running mark in place. The next start catches up: the watch hears
// the post whose notice was lost, and every other post of the store. One
// process cannot die and start again, so the first run is stopped and its
// mark put back, as a process that died leaves it.
func TestAWatchCatchesUpAfterAStopThatWasNotClean(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			first := w.start(t, func(b queue.Broker) queue.Broker { return lossyBroker{b} })
			must(t, w.posts.Insert(t.Context(), keysPost{ID: "lost", Author: "ann", Body: "unheard"}))
			stop(t, first)
			if _, err := os.Stat(w.markOf(place)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("after the first run's stop, the running mark: %v; want it gone", err)
			}
			must(t, os.WriteFile(w.markOf(place), []byte(w.watch.id+"\n"), 0o600))
			if len(w.heardOf("lost")) != 0 {
				t.Fatal("the lost notice was heard")
			}

			second := w.start(t, nil)
			defer stop(t, second)
			w.settle(t, "after")
			got := w.heardOf("lost")
			if len(got) != 1 || got[0].Deleted || got[0].Store != w.posts.id {
				t.Errorf("after a stop that was not clean, the watch heard %+v of the lost post; want one notice of a write", got)
			}
		})
	}
}

// A clean stop takes the mark off: the next start catches nothing up, and
// a post the watch heard is not heard again.
func TestACleanStopCatchesNothingUp(t *testing.T) {
	for name, place := range keysEngines(t) {
		t.Run(name, func(t *testing.T) {
			w := newKeysWorld(t, place)
			first := w.start(t, nil)
			w.settle(t, "heard")
			stop(t, first)
			if _, err := os.Stat(w.markOf(place)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("after a clean stop, the running mark: %v; want it gone", err)
			}

			second := w.start(t, nil)
			defer stop(t, second)
			w.settle(t, "after")
			if got := w.heardOf("heard"); len(got) != 1 {
				t.Errorf("a post heard before a clean stop was heard %d times; want once", len(got))
			}
		})
	}
}
