package kit

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// draft is what the revisions command's product keeps: a secret member the
// restore keeps as it is.
type draft struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Token string `json:"token,omitempty" kit:"secret"`
}

var cmdRevisionsSvc = NewService("cmd-revisions", "Drafts, for the revisions command's tests.")

var cmdDrafts = cmdRevisionsSvc.Store("drafts", func(d draft) string { return d.ID }, Revisions(5))

// revisionsCmd runs the revisions command of an app named "drafts" on the
// data in dir, as an operator would, the product stopped.
func revisionsCmd(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	app := NewApp("drafts", cmdRevisionsSvc).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:1"), Logs(io.Discard))
	var out, errOut bytes.Buffer
	code = app.revisionsCommand(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// draftsIn runs the drafts' product on dir while fn writes, and stops it.
func draftsIn(t *testing.T, dir string, fn func(ctx context.Context)) {
	t.Helper()
	app := NewApp("drafts", cmdRevisionsSvc).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:0"), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	fn(t.Context())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, app.Stop(ctx))
}

// `revisions restore` writes a version back as Store.Restore does — a new
// version by nobody, the secret kept —, a member alone with -field, and is
// what the Studio shows instead of restoring (ADR 0010, D13).
func TestTheRevisionsCommandRestoresAVersion(t *testing.T) {
	dir := t.TempDir()
	draftsIn(t, dir, func(ctx context.Context) {
		must(t, cmdDrafts.Insert(ctx, draft{ID: "d1", Title: "one", Body: "first", Token: "t1"}))
		_, err := cmdDrafts.Update(ctx, "d1", func(d *draft) error {
			d.Title, d.Body, d.Token = "two", "second", "t2"
			return nil
		})
		must(t, err)
	})

	code, out, errOut := revisionsCmd(t, dir, "restore", "-store", "cmd-revisions/store/drafts", "-key", "d1", "-rev", "1", "-field", "/title")
	if code != 0 || out != "cmd-revisions/store/drafts d1: version 1 restored, as a new version\n" {
		t.Fatalf("restore -field: %d %q %q", code, out, errOut)
	}
	draftsIn(t, dir, func(ctx context.Context) {
		got, err := cmdDrafts.Get(ctx, "d1")
		must(t, err)
		if got.Title != "one" || got.Body != "second" || got.Token != "t2" {
			t.Errorf("after restoring the title: %+v", got)
		}
		revs, err := cmdDrafts.Revisions(ctx, "d1")
		must(t, err)
		if len(revs) != 3 || revs[0].Number != 3 || revs[0].By != "" {
			t.Errorf("the command's restore is a new version by nobody: %+v", revs)
		}
	})

	if code, out, errOut := revisionsCmd(t, dir, "restore", "--store", "cmd-revisions/store/drafts", "--key", "d1", "--rev", "1"); code != 0 {
		t.Fatalf("restore: %d %q %q", code, out, errOut)
	}
	draftsIn(t, dir, func(ctx context.Context) {
		got, err := cmdDrafts.Get(ctx, "d1")
		must(t, err)
		if got.Title != "one" || got.Body != "first" || got.Token != "t2" {
			t.Errorf("after restoring version 1: %+v", got)
		}
	})
}

// The command refuses what it cannot restore, and says how it is used.
func TestTheRevisionsCommandRefuses(t *testing.T) {
	dir := t.TempDir()
	draftsIn(t, dir, func(ctx context.Context) {
		must(t, cmdDrafts.Insert(ctx, draft{ID: "d1", Title: "one"}))
	})
	for _, c := range []struct {
		args   []string
		code   int
		errHas string
	}{
		{nil, 2, "revisions restore -store ID"},
		{[]string{"list"}, 2, "revisions restore -store ID"},
		{[]string{"restore", "-store", "cmd-revisions/store/drafts", "-key", "d1"}, 2, "revisions restore -store ID"},
		{[]string{"restore", "-store", "cmd-revisions/store/drafts", "-key", "d1", "-rev", "1", "extra"}, 2, "revisions restore -store ID"},
		{[]string{"restore", "-store", "cmd-revisions/store/nothing", "-key", "d1", "-rev", "1"}, 1, "no such store"},
		{[]string{"restore", "-store", "cmd-revisions/store/drafts", "-key", "d1", "-rev", "9"}, 1, ""},
		{[]string{"restore", "-store", "cmd-revisions/store/drafts", "-key", "nobody", "-rev", "1"}, 1, ""},
	} {
		code, out, errOut := revisionsCmd(t, dir, c.args...)
		if code != c.code || !strings.Contains(errOut, c.errHas) {
			t.Errorf("revisions %v: %d %q %q, want %d and %q", c.args, code, out, errOut, c.code, c.errHas)
		}
	}
}

// A restore kit makes for the command line is one root span on the store,
// which says it came from there.
func TestARestoreFromTheCommandLineIsTraced(t *testing.T) {
	app := NewApp("drafts", cmdRevisionsSvc).With(InMemory(), Env(EnvDev), Listen("127.0.0.1:0"), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	ctx := t.Context()
	must(t, cmdDrafts.Insert(ctx, draft{ID: "d2", Title: "one"}))
	_, err := cmdDrafts.Update(ctx, "d2", func(d *draft) error { d.Title = "two"; return nil })
	must(t, err)

	events, stop := app.hub.subscribe()
	defer stop()
	must(t, cmdDrafts.restoreVia(withActor(ctx, actorCLI), app, actorCLI, "d2", 1, nil))
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == model.EventSpan && e.Span.Node == "cmd-revisions/store/drafts" && e.Span.Attrs["via"] == actorCLI {
				return
			}
		case <-deadline:
			t.Fatal("no span via cli for the restore")
		}
	}
}
