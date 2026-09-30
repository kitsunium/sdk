package kit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The revisions product (ADR 0007 §3): pages that keep their three former
// versions, as WordPress keeps a post's, a workflow that publishes them, a
// command that edits them — and a secret token, which no version gives back.

var Press = kit.NewService("press", "Pages that keep their revisions, for the revisions tests (ADR 0007 §3).")

// Page is a page of the press.
type Page struct {
	ID     string   `json:"id"`
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Blocks []string `json:"blocks,omitempty"`
	Token  string   `json:"token,omitempty" kit:"secret"`
	State  string   `json:"state,omitempty"`
	Views  int      `json:"views,omitempty"`
}

func (p Page) Key() string { return p.ID }

var Pages = Press.Store("pages", Page.Key, kit.Revisions(3),
	kit.Unique("slug", func(p Page) string { return p.Slug }))

// Publishing publishes a page; its review counts a view as it enters.
var Publishing = Press.Workflow("publishing", Pages, func(p *Page) *string { return &p.State }).
	Initial("draft").
	On("publish", "draft", "published").
	On("unpublish", "published", "draft").
	On("review", "draft", "reviewed").
	OnEnter("reviewed", func(_ context.Context, p *Page) error { p.Views++; return nil })

// PageEdit is what EditPage changes.
type PageEdit struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// EditPage retitles a page: its version names the command.
var EditPage = Press.Command("edit-page", func(ctx context.Context, in PageEdit) (kit.EmptyValue, error) {
	_, err := Pages.Update(ctx, in.ID, func(p *Page) error { p.Title = in.Title; return nil })
	return kit.EmptyValue{}, err
})

// Notes keep no revisions.
var Notes = Press.Store("notes", func(n Page) string { return n.ID })

// startPress runs the press in dev: in memory unless opts give a data
// directory.
func startPress(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	app := kit.NewApp("press", Press).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	run(t, app)
	return app
}

// pressBackends run a test on memory and in the data directory, where the
// page's secret is sealed at rest.
var pressBackends = []struct {
	name string
	opts func(t *testing.T) []kit.AppConfigurer
}{
	{"memory", func(*testing.T) []kit.AppConfigurer { return []kit.AppConfigurer{kit.InMemory()} }},
	{"files", func(t *testing.T) []kit.AppConfigurer {
		needsFileStore(t)
		return []kit.AppConfigurer{kit.DataDir(t.TempDir())}
	}},
}

// eachPress runs fn on each backend, on a manual clock at epoch.
func eachPress(t *testing.T, fn func(t *testing.T, clk *clock.ManualClock)) {
	for _, b := range pressBackends {
		t.Run(b.name, func(t *testing.T) {
			clk := clock.NewManualClock(epoch)
			startPress(t, append(b.opts(t), kit.Clock(clk))...)
			fn(t, clk)
		})
	}
}

// retitle changes a page's title.
func retitle(title string) func(*Page) error {
	return func(p *Page) error { p.Title = title; return nil }
}

// titles are the titles of a page's versions, newest first, with their
// numbers.
func titles(t *testing.T, key string) []string {
	t.Helper()
	revs, err := Pages.Revisions(t.Context(), key)
	if err != nil {
		t.Fatalf("revisions of %s: %v", key, err)
	}
	out := []string{}
	for _, r := range revs {
		out = append(out, fmt.Sprintf("%d %s", r.Number, r.Value.Title))
	}
	return out
}

// A record keeps its three previous versions, newest first: numbered from
// 1 and never renumbered, the oldest pruned, each with when it was made, by
// whom and which command.
func TestARecordKeepsItsRevisions(t *testing.T) {
	eachPress(t, func(t *testing.T, clk *clock.ManualClock) {
		ctx := kit.WithUser(t.Context(), "editor", Who{})
		must(t, Pages.Insert(ctx, Page{ID: "p1", Slug: "home", Title: "t1"}))
		for _, title := range []string{"t2", "t3", "t4"} {
			clk.Advance(time.Hour)
			_, err := Pages.Update(ctx, "p1", retitle(title))
			must(t, err)
		}
		clk.Advance(time.Hour)
		_, err := EditPage.Dispatch(ctx, PageEdit{ID: "p1", Title: "t5"})
		must(t, err)

		if got, want := titles(t, "p1"), []string{"5 t5", "4 t4", "3 t3", "2 t2"}; !slices.Equal(got, want) {
			t.Errorf("versions %q, want %q: three former ones, the oldest pruned", got, want)
		}
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		if r := revs[0]; r.By != "editor" || r.Command != "press/command/edit-page" || !r.At.Equal(epoch.Add(4*time.Hour)) {
			t.Errorf("the newest version says %q %q %v: the user, the command and the instant", r.By, r.Command, r.At)
		}
		if r := revs[1]; r.By != "editor" || r.Command != "" || !r.At.Equal(epoch.Add(3*time.Hour)) {
			t.Errorf("a version outside a command says %q %q %v", r.By, r.Command, r.At)
		}
		one, err := Pages.Revision(ctx, "p1", 3)
		must(t, err)
		if one.Value.Title != "t3" || one.Number != 3 {
			t.Errorf("version 3 is %+v", one)
		}
		if _, err := Pages.Revision(ctx, "p1", 1); !isNotFound(err) {
			t.Errorf("a pruned version answers %v, want a NotFound", err)
		}
		if _, err := Pages.Revisions(ctx, "nobody"); !isNotFound(err) {
			t.Errorf("a missing record answers %v, want a NotFound", err)
		}
	})
}

// A write that stores the same JSON makes no version, and neither does a
// transition that changes only the state; one whose hook changes more does.
func TestOnlyAChangeMakesAVersion(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := t.Context()
		p, err := Publishing.Start(ctx, Page{ID: "p1", Slug: "home", Title: "t1", Token: "tok-1"})
		must(t, err)
		must(t, Pages.Put(ctx, p))
		_, err = Pages.Update(ctx, "p1", func(*Page) error { return nil })
		must(t, err)
		if got := titles(t, "p1"); len(got) != 1 {
			t.Errorf("versions %q after writes of the same record: want one", got)
		}
		_, err = Publishing.Fire(ctx, "p1", "publish")
		must(t, err)
		_, err = Publishing.Fire(ctx, "p1", "unpublish")
		must(t, err)
		if got := titles(t, "p1"); len(got) != 1 {
			t.Errorf("versions %q after transitions of the state alone: want one", got)
		}
		cur, err := Pages.Get(ctx, "p1")
		must(t, err)
		if cur.State != "draft" {
			t.Errorf("the state is %q after publish and unpublish", cur.State)
		}
		_, err = Publishing.Fire(ctx, "p1", "review")
		must(t, err)
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		if len(revs) != 2 || revs[0].Value.Views != 1 || revs[0].Value.State != "reviewed" {
			t.Errorf("a transition whose hook counts a view makes a version: %+v", revs)
		}
	})
}

// A secret member is zeroed in what Revisions and Revision return; Diff says
// it changed, never what.
func TestASecretNeverLeavesAVersion(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := t.Context()
		must(t, Pages.Insert(ctx, Page{ID: "p1", Slug: "home", Title: "t1", Token: "tok-1", Blocks: []string{"a", "c"}}))
		_, err := Pages.Update(ctx, "p1", func(p *Page) error {
			p.Title, p.Token, p.Blocks = "t2", "tok-2", []string{"a", "b", "c"}
			return nil
		})
		must(t, err)
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		for _, r := range revs {
			if r.Value.Token != "" {
				t.Errorf("version %d gives its secret back: %q", r.Number, r.Value.Token)
			}
		}
		one, err := Pages.Revision(ctx, "p1", 1)
		must(t, err)
		if one.Value.Token != "" {
			t.Errorf("Revision gives a secret back: %q", one.Value.Token)
		}
		edits, err := Pages.Diff(ctx, "p1", 1, 2)
		must(t, err)
		got := []string{}
		for _, e := range edits {
			got = append(got, fmt.Sprintf("%s %s %s %s", e.Op, e.Path, string(e.From), string(e.To)))
		}
		want := []string{`add /blocks/1  "b"`, `replace /title "t1" "t2"`, `replace /token  `}
		if !slices.Equal(got, want) {
			t.Errorf("diff %q, want %q", got, want)
		}
		back, err := Pages.Diff(ctx, "p1", 2, 1)
		must(t, err)
		if len(back) != 3 || back[0].Op != "remove" || string(back[0].From) != `"b"` {
			t.Errorf("the diff back is %+v", back)
		}
		same, err := Pages.Diff(ctx, "p1", 2, 2)
		must(t, err)
		if len(same) != 0 {
			t.Errorf("a version against itself: %+v", same)
		}
		if _, err := Pages.Diff(ctx, "p1", 1, 9); !isNotFound(err) {
			t.Errorf("a diff with a version never made answers %v", err)
		}
	})
}

// Restore writes a version back as a new one — whole, or the fields named
// —, keeping the workflow's state and the secret; it names neither.
func TestRestoreWritesAVersionBack(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := kit.WithUser(t.Context(), "restorer", Who{})
		_, err := Publishing.Start(ctx, Page{ID: "p1", Slug: "home", Title: "t1", Body: "b1", Token: "tok-1"})
		must(t, err)
		_, err = Pages.Update(ctx, "p1", func(p *Page) error { p.Title, p.Body, p.Token = "t2", "b2", "tok-2"; return nil })
		must(t, err)
		_, err = Publishing.Fire(ctx, "p1", "publish")
		must(t, err)

		got, err := Pages.Restore(ctx, "p1", 1, "/title")
		must(t, err)
		if got.Title != "t1" || got.Body != "b2" || got.State != "published" || got.Token != "tok-2" {
			t.Errorf("the title restored gives %+v", got)
		}
		got, err = Pages.Restore(ctx, "p1", 1)
		must(t, err)
		if got.Title != "t1" || got.Body != "b1" || got.State != "published" || got.Token != "tok-2" {
			t.Errorf("the whole version restored gives %+v: the state and the secret stay", got)
		}
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		if len(revs) != 4 || revs[0].Number != 4 || revs[0].By != "restorer" {
			t.Errorf("a restore is a new version by its caller: %+v", revs)
		}
		stored, err := Pages.Get(ctx, "p1")
		must(t, err)
		if stored.Token != "tok-2" {
			t.Errorf("the stored secret is %q", stored.Token)
		}
		for _, fields := range [][]string{{"/token"}, {"/state"}, {"/nothing"}, {"title"}} {
			if _, err := Pages.Restore(ctx, "p1", 1, fields...); !isInvalid(err) {
				t.Errorf("a restore of %q answers %v, want an Invalid", fields, err)
			}
		}
		if _, err := Pages.Restore(ctx, "p1", 9); !isNotFound(err) {
			t.Errorf("a restore of a version never made answers %v", err)
		}
		if _, err := Notes.Revisions(ctx, "p1"); !isInvalid(err) {
			t.Errorf("a store without revisions answers %v, want an Invalid", err)
		}
	})
}

// A restore a unique index refuses changes nothing.
func TestARestoreAUniqueIndexRefusesChangesNothing(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := t.Context()
		must(t, Pages.Insert(ctx, Page{ID: "p1", Slug: "home", Title: "t1"}))
		_, err := Pages.Update(ctx, "p1", func(p *Page) error { p.Slug, p.Title = "start", "t2"; return nil })
		must(t, err)
		must(t, Pages.Insert(ctx, Page{ID: "p2", Slug: "home", Title: "other"}))
		if _, err := Pages.Restore(ctx, "p1", 1); !isConflict(err) {
			t.Fatalf("a restore that takes a unique key answers %v, want a Conflict", err)
		}
		if got, want := titles(t, "p1"), []string{"2 t2", "1 t1"}; !slices.Equal(got, want) {
			t.Errorf("versions %q after a refused restore, want %q", got, want)
		}
		if _, err := Pages.Restore(ctx, "p1", 1, "/title"); err != nil {
			t.Errorf("the title alone restores: %v", err)
		}
	})
}

// A write kit.Transact rolls back leaves no version behind: what never
// committed never shows, and the record reads as before.
func TestARollbackLeavesNoVersion(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := t.Context()
		must(t, Pages.Insert(ctx, Page{ID: "p1", Slug: "home", Title: "t1"}))
		_, err := Pages.Update(ctx, "p1", retitle("t2"))
		must(t, err)
		refused := errors.New("refused")
		err = kit.Transact(ctx, func(ctx context.Context) error {
			if _, err := Pages.Update(ctx, "p1", retitle("never")); err != nil {
				return err
			}
			if _, err := Pages.Update(ctx, "p1", retitle("never again")); err != nil {
				return err
			}
			return refused
		})
		if !errors.Is(err, refused) {
			t.Fatalf("the transaction answers %v", err)
		}
		cur, err := Pages.Get(ctx, "p1")
		must(t, err)
		if cur.Title != "t2" {
			t.Errorf("the record reads %q after the rollback", cur.Title)
		}
		for _, title := range titles(t, "p1") {
			if strings.Contains(title, "never") {
				t.Errorf("a rolled back write shows in the versions: %q", titles(t, "p1"))
			}
		}
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		if revs[0].Value.Title != "t2" || revs[0].By != "" || revs[len(revs)-1].Value.Title != "t1" {
			t.Errorf("versions %q: the rollback writes t2 back as kit's version, and prunes nothing", titles(t, "p1"))
		}
	})
}

// kit.Revisions keeps from 1 to 100 versions: any other number refuses the
// start, naming the store.
func TestRevisionsOutOfRangeRefuseTheStart(t *testing.T) {
	for _, n := range []int{0, -1, 101} {
		svc := kit.NewService("press-bad", "A store with a number of versions kit refuses.")
		svc.Store("pages", Page.Key, kit.Revisions(n))
		app := kit.NewApp("press-bad", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev),
			kit.Analyze(false), kit.Logs(io.Discard))
		err := app.Start(t.Context())
		if err == nil {
			t.Errorf("kit.Revisions(%d) started: %v", n, app.Stop(context.Background()))
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("kit.Revisions(%d)", n)) {
			t.Errorf("kit.Revisions(%d) refused with %v", n, err)
		}
	}
}

// A store whose files keep versions, declared without kit.Revisions, does
// not open, and says why: the document store never drops them unasked.
func TestFilesThatKeepVersionsNeedRevisions(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := startPress(t, kit.DataDir(dir))
	must(t, Pages.Insert(t.Context(), Page{ID: "p1", Slug: "home", Title: "t1"}))
	_, err := Pages.Update(t.Context(), "p1", retitle("t2"))
	must(t, err)
	stopClinic(t, app)
	svc := kit.NewService("press", "The press, its pages declared without their revisions.")
	svc.Store("pages", Page.Key, kit.Unique("slug", func(p Page) string { return p.Slug }))
	bare := kit.NewApp("press", svc).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev),
		kit.Analyze(false), kit.Logs(io.Discard))
	err = bare.Start(t.Context())
	if err == nil {
		t.Fatalf("a store over files that keep versions started without kit.Revisions: %v", bare.Stop(context.Background()))
	}
	if !strings.Contains(err.Error(), "STORE_VERSIONS") || !strings.Contains(err.Error(), "kit.Revisions") {
		t.Errorf("the refusal says %v", err)
	}
}

// A transition of the state alone that a rollback undoes leaves no trace
// either: it changed the version it found in place, which holds again what
// it held.
func TestARolledBackTransitionLeavesNoTrace(t *testing.T) {
	eachPress(t, func(t *testing.T, _ *clock.ManualClock) {
		ctx := t.Context()
		_, err := Publishing.Start(ctx, Page{ID: "p1", Slug: "home", Title: "t1"})
		must(t, err)
		refused := errors.New("refused")
		err = kit.Transact(ctx, func(ctx context.Context) error {
			if _, err := Publishing.Fire(ctx, "p1", "publish"); err != nil {
				return err
			}
			return refused
		})
		if !errors.Is(err, refused) {
			t.Fatalf("the transaction answers %v", err)
		}
		revs, err := Pages.Revisions(ctx, "p1")
		must(t, err)
		for _, r := range revs {
			if r.Value.State != "draft" {
				t.Errorf("version %d holds the state a rollback undid: %q", r.Number, r.Value.State)
			}
		}
		if cur, err := Pages.Get(ctx, "p1"); err != nil || cur.State != "draft" {
			t.Errorf("the record after the rollback: %+v %v", cur, err)
		}
	})
}

// isInvalid reports an Invalid.
func isInvalid(err error) bool {
	ke, ok := errors.AsType[*kit.Error](err)
	return ok && ke.Code == kit.WireInvalid
}
