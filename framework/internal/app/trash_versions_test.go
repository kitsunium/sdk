package app_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// A deletion by the product on a store with revisions keeps the record's
// versions in the trash (ADR 0178 C4): they read as they were, the deleted
// record first, and a record written again under its key continues their
// numbering — on every backend the members run on. Only the store's
// existing API is read, so that the test says the same before the trash
// existed: there, the deletion took the versions with it.
func TestADeletionKeepsTheVersionsInTheTrash(t *testing.T) {
	eachFolk(t, func(t *testing.T, _ *app.App, _ string, _ *clock.ManualClock) {
		ctx := t.Context()
		must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist"}))
		_, err := Folks.Update(ctx, "m1", renamed("Annabel Vesper"))
		must(t, err)
		must(t, Folks.Delete(ctx, "m1"))
		if _, err := Folks.Get(ctx, "m1"); err == nil {
			t.Fatal("a deleted record still reads")
		}
		if names := memberNames(t, "m1"); !slices.Equal(names, []string{"Annabel Vesper", "Annabel Quist"}) {
			t.Errorf("the versions of a deleted record: %q, want both kept in the trash", names)
		}
		must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Lowmoor"}))
		revs, err := Folks.Revisions(ctx, "m1")
		must(t, err)
		if len(revs) == 0 || revs[0].Number != 3 {
			t.Errorf("a record written again over the trash: %+v, want version 3", revs)
		}
	})
}

// folkBackends run a test in memory, in the data directory — where the
// members' personal fields are sealed — and on kit's fake database.
var folkBackends = []struct {
	name string
	opts func(t *testing.T) (dir string, opts []app.AppConfigurer)
}{
	{"memory", func(*testing.T) (string, []app.AppConfigurer) { return "", []app.AppConfigurer{app.InMemory()} }},
	{"files", func(t *testing.T) (string, []app.AppConfigurer) {
		needsFileStore(t)
		dir := t.TempDir()
		return dir, []app.AppConfigurer{app.DataDir(dir)}
	}},
	{"fake-database", func(t *testing.T) (string, []app.AppConfigurer) {
		needsFileStore(t)
		t.Setenv("FOLK_MAIN_URL", verifiedURL())
		return "", []app.AppConfigurer{app.DataDir(t.TempDir()), app.Database("main", app.NewFakeDB(sql.DialectPostgres).Engine())}
	}},
}

// eachFolk runs fn on each backend, on a manual clock at epoch.
func eachFolk(t *testing.T, fn func(t *testing.T, a *app.App, dir string, clk *clock.ManualClock)) {
	for _, b := range folkBackends {
		t.Run(b.name, func(t *testing.T) {
			dir, opts := b.opts(t)
			clk := clock.NewManualClock(epoch)
			a := startFolk(t, append(opts, app.Clock(clk))...)
			fn(t, a, dir, clk)
		})
	}
}
