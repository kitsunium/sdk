package apptest_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/app/apptest"
	_ "github.com/kitsunium/sdk/framework/app/server"
	_ "github.com/kitsunium/sdk/framework/app/store"
)

// The product of these tests: a desk whose notes are about someone, their
// author sealed at rest, their topic in clear.
var Desk = app.NewService("desk", "Notes about people, for apptest's tests.")

// Note is about its author.
type Note struct {
	ID     string `json:"id"`
	Author string `json:"author" kit:"subject"`
	Body   string `json:"body" kit:"personal"`
	Topic  string `json:"topic" kit:"public"`
}

var Notes = Desk.Store("notes", func(n Note) string { return n.ID },
	app.RetentionByProduct("the tests remove what they write"), app.Purpose("Keep the notes of apptest's tests"))

// start runs the desk as a suite's own helper does: in memory, in dev, on a
// free port — unless the test's options say otherwise.
func start(t *testing.T, opts ...app.AppOption) *app.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	a := app.NewApp("desk", Desk).With(append([]app.AppOption{
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stop(t, a) })
	return a
}

// stop stops app; an app stopped already stays so.
func stop(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("stop: %v", err)
	}
}

// A Disk given after app.InMemory runs the product on its data directory:
// what it keeps personal is sealed there, what is public is not, and the
// product started again on the same Disk reads it all back.
func TestADiskRunsTheProductAsProductionDoes(t *testing.T) {
	disk := apptest.NewDisk(t)
	a := start(t, disk)
	ctx := t.Context()
	if err := Notes.Insert(ctx, Note{ID: "n1", Author: "ann@example.com", Body: "a note only ann may read", Topic: "apptest-topic"}); err != nil {
		t.Fatal(err)
	}
	if held := disk.Holding(t, "ann@example.com", "a note only ann may read"); len(held) > 0 {
		t.Errorf("personal data in clear on disk: %v", held)
	}
	if held := disk.Holding(t, "apptest-topic"); len(held) == 0 {
		t.Error("the public topic is nowhere on disk: the store is not on it")
	}
	stop(t, a)

	start(t, disk)
	n, err := Notes.Get(ctx, "n1")
	if err != nil || n.Body != "a note only ann may read" || n.Author != "ann@example.com" {
		t.Errorf("after a restart on the same disk: %+v %v", n, err)
	}
}

// Holding reads every file but kit's secrets, and says where a word is, in
// order.
func TestHoldingSaysWhereAWordRests(t *testing.T) {
	disk := apptest.NewDisk(t)
	a := start(t, disk)
	for _, id := range []string{"b", "a"} {
		if err := Notes.Insert(t.Context(), Note{ID: id, Author: "bob@example.com", Body: "x", Topic: "shared-topic"}); err != nil {
			t.Fatal(err)
		}
	}
	stop(t, a) // folded into the store's snapshot
	if got := disk.Holding(t, "shared-topic"); !slices.Equal(got, []string{"desk/notes.json"}) {
		t.Errorf("Holding = %v", got)
	}
	if got := disk.Holding(t, "no such word", ""); len(got) != 0 {
		t.Errorf("Holding of nothing = %v", got)
	}
}
