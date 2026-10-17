package kittest_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/framework/kit/kittest"
	_ "github.com/kitsunium/sdk/framework/kit/server"
	_ "github.com/kitsunium/sdk/framework/kit/store"
)

// The product of these tests: a desk whose notes are about someone, their
// author sealed at rest, their topic in clear.
var Desk = kit.NewService("desk", "Notes about people, for kittest's tests.")

// Note is about its author.
type Note struct {
	ID     string `json:"id"`
	Author string `json:"author" kit:"subject"`
	Body   string `json:"body" kit:"personal"`
	Topic  string `json:"topic" kit:"public"`
}

var Notes = Desk.Store("notes", func(n Note) string { return n.ID },
	kit.RetentionByProduct("the tests remove what they write"), kit.Purpose("Keep the notes of kittest's tests"))

// start runs the desk as a suite's own helper does: in memory, in dev, on a
// free port — unless the test's options say otherwise.
func start(t *testing.T, opts ...kit.AppOption) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	app := kit.NewApp("desk", Desk).With(append([]kit.AppOption{
		kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stop(t, app) })
	return app
}

// stop stops app; an app stopped already stays so.
func stop(t *testing.T, app *kit.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("stop: %v", err)
	}
}

// A Disk given after kit.InMemory runs the product on its data directory:
// what it keeps personal is sealed there, what is public is not, and the
// product started again on the same Disk reads it all back.
func TestADiskRunsTheProductAsProductionDoes(t *testing.T) {
	disk := kittest.NewDisk(t)
	app := start(t, disk)
	ctx := t.Context()
	if err := Notes.Insert(ctx, Note{ID: "n1", Author: "ann@example.com", Body: "a note only ann may read", Topic: "kittest-topic"}); err != nil {
		t.Fatal(err)
	}
	if held := disk.Holding(t, "ann@example.com", "a note only ann may read"); len(held) > 0 {
		t.Errorf("personal data in clear on disk: %v", held)
	}
	if held := disk.Holding(t, "kittest-topic"); len(held) == 0 {
		t.Error("the public topic is nowhere on disk: the store is not on it")
	}
	stop(t, app)

	start(t, disk)
	n, err := Notes.Get(ctx, "n1")
	if err != nil || n.Body != "a note only ann may read" || n.Author != "ann@example.com" {
		t.Errorf("after a restart on the same disk: %+v %v", n, err)
	}
}

// Holding reads every file but kit's secrets, and says where a word is, in
// order.
func TestHoldingSaysWhereAWordRests(t *testing.T) {
	disk := kittest.NewDisk(t)
	app := start(t, disk)
	for _, id := range []string{"b", "a"} {
		if err := Notes.Insert(t.Context(), Note{ID: id, Author: "bob@example.com", Body: "x", Topic: "shared-topic"}); err != nil {
			t.Fatal(err)
		}
	}
	stop(t, app) // folded into the store's snapshot
	if got := disk.Holding(t, "shared-topic"); !slices.Equal(got, []string{"desk/notes.json"}) {
		t.Errorf("Holding = %v", got)
	}
	if got := disk.Holding(t, "no such word", ""); len(got) != 0 {
		t.Errorf("Holding of nothing = %v", got)
	}
}
