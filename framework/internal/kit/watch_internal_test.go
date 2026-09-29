package kit

import (
	"context"
	"io"
	"testing"
)

// forumPost is content others see: moderated. forumDraft is not.
type (
	forumPost struct {
		ID   string `json:"id"`
		Body string `json:"body" kit:"moderated"`
	}
	forumDraft struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	}
)

// A store feeds a watch while the watch runs, and only then. In a product
// that mounts no watch every store's list stays empty — a write pays the
// one load that finds it so —; an unmarked store and the watch's own
// module's are never on it; a watch that stops takes itself off.
func TestAStoreFeedsAWatchOnlyWhileItRuns(t *testing.T) {
	forum := NewService("forum", "Posts, moderated, and drafts, which are not.")
	posts := forum.Store("posts", func(p forumPost) string { return p.ID })
	drafts := forum.Store("drafts", func(d forumDraft) string { return d.ID })
	screening := NewService("screening", "Screens the forum's posts.")
	flags := screening.Store("flags", func(p forumPost) string { return p.ID })
	watch := screening.Watch("content", Moderated, func(context.Context, WrittenEvent) error { return nil })
	module := NewModule("moderation", "Moderation, for the tests.", screening)
	run := func(app *App) {
		t.Helper()
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	stop := func(app *App) {
		t.Helper()
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	bare := NewApp("forum", forum).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	run(bare)
	if posts.feeds.Load() != nil || drafts.feeds.Load() != nil {
		t.Error("a store feeds a watch in a product that mounts none")
	}
	stop(bare)

	app := bare.With(module)
	run(app)
	if got := posts.feeds.Load(); got == nil || len(*got) != 1 || (*got)[0] != watch {
		t.Errorf("the marked store feeds %v, want the watch", got)
	}
	if drafts.feeds.Load() != nil || flags.feeds.Load() != nil {
		t.Error("an unmarked store, or the watch's own module's, feeds it")
	}
	stop(app)
	if posts.feeds.Load() != nil {
		t.Error("a stopped watch is still fed")
	}
}
