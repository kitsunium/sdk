package kit_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// kit.Only (#274 Q3, ADR 0178's decisions of 15 October): a mount that holds
// only in the environments it names — elsewhere as if it had not been given,
// its prefix and its bindings with it — and what the start refuses of it.

// A mount given kit.Only mounts its module in an environment it names, and
// not in another: the module, the module it requires and their routes are
// absent there.
func TestOnlyMountsAModuleInTheEnvironmentsItNames(t *testing.T) {
	app := start(t, kit.Mount(ReviewsModule, kit.Only("dev", "staging")))
	if app.Graph().ModuleOf("reviews") == nil {
		t.Fatal("a mount for dev is not mounted in dev")
	}
	if r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusOK {
		t.Errorf("the module's route is not served in dev: %d %s", r.status, r.body)
	}
	stopApp(t, app)

	app = start(t, kit.Mount(ReviewsModule, kit.Only("staging")))
	for _, name := range []string{"reviews", "ratings"} {
		if m := app.Graph().ModuleOf(name); m != nil {
			t.Errorf("module %s is mounted in dev by a mount for staging: %+v", name, m)
		}
	}
	if r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusNotFound {
		t.Errorf("a module mounted only in staging serves its route in dev: %d", r.status)
	}
}

// The graph says in which environments a module is mounted: the union of
// the environments its mounts name, sorted, when every mount of it is given
// kit.Only; nothing when one mount holds everywhere, nor for a module only
// another module's kit.Requires mounts — as its Mount says nothing.
func TestTheGraphSaysWhereAModuleIsMounted(t *testing.T) {
	app := start(t, kit.Mount(ReviewsModule, kit.Only("staging", "dev")), kit.Mount(ReviewsModule, kit.Only("dev", "qa")))
	g := app.Graph()
	if m := g.ModuleOf("reviews"); m == nil || !slices.Equal(m.Only, []string{"dev", "qa", "staging"}) {
		t.Errorf("a module mounted for staging, dev and qa says %+v", m)
	}
	if m := g.ModuleOf("ratings"); m == nil || m.Only != nil {
		t.Errorf("a module another module requires says %+v", m)
	}
	stopApp(t, app)

	app = start(t, kit.Mount(ReviewsModule, kit.Only("staging")), kit.Mount(ReviewsModule))
	if m := app.Graph().ModuleOf("reviews"); m == nil || m.Only != nil {
		t.Errorf("a module one mount mounts everywhere says %+v", m)
	}
}

// The environment is the name KIT_ENV gives, lower-cased, and kit.Env wins
// over it as it does for everything else.
func TestOnlyReadsTheEnvironmentsName(t *testing.T) {
	t.Setenv("KIT_ENV", "Staging")
	app := kit.NewApp("shop", Shop, Audit, Members).With(kit.Mount(ReviewsModule, kit.Only("staging")))
	if !mountsReviews(app) {
		t.Error("KIT_ENV=Staging does not hold a mount for staging")
	}
	if mountsReviews(app.With(kit.Env("production"))) {
		t.Error("kit.Env(production) holds a mount for staging")
	}
	t.Setenv("KIT_ENV", "")
	if !mountsReviews(kit.NewApp("shop", Shop, Audit, Members).With(kit.Mount(ReviewsModule, kit.Only("production")))) {
		t.Error("an unset KIT_ENV is not production")
	}
}

// The start resolves the mounts again for the environment it reads: an app
// declared before KIT_ENV changed mounts what the environment it starts in
// says, and keeps doing so when it starts again.
func TestOnlyIsResolvedAgainAtTheStart(t *testing.T) {
	t.Setenv("KIT_SMTP_URL", "")
	t.Setenv("KIT_ENV", "")
	app := kit.NewApp("shop", Shop, Audit, Members).With(kit.Mount(ReviewsModule, kit.Only("dev")),
		kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Analyze(false), kit.Logs(io.Discard))
	if mountsReviews(app) {
		t.Fatal("a mount for dev holds in production")
	}
	t.Setenv("KIT_ENV", "dev")
	for range 2 {
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		if !mountsReviews(app) {
			t.Error("the start in dev does not mount what a mount for dev mounts")
		}
		if r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusOK {
			t.Errorf("the module's route in dev: %d %s", r.status, r.body)
		}
		stopApp(t, app)
	}
}

// mountsReviews reports whether app, described without running, mounts the
// reviews module.
func mountsReviews(app *kit.App) bool {
	return app.Graph().ModuleOf("reviews") != nil
}

// Outside its environments a mount is as if it had not been given: another
// mount of the module without kit.Only still mounts it, at its own prefix,
// and the excluded mount's binding is neither applied nor judged.
func TestAMountOutsideItsEnvironmentsIsNotGiven(t *testing.T) {
	app := start(t,
		kit.Mount(ReviewsModule, kit.Only("staging"), kit.Prefix("/elsewhere/")),
		kit.Mount(ReviewsModule),
	)
	if m := app.Graph().ModuleOf("reviews"); m == nil || m.Prefix != "/reviews/" {
		t.Errorf("the mount that holds is not the one applied: %+v", m)
	}
	stopApp(t, app)

	strict := kit.NewService("strict", "The product's own screening.")
	never := strict.Query("never", func(_ context.Context, r Review) (Verdict, error) {
		return Verdict{ID: r.ID, By: "product"}, nil
	})
	app = startApp(t, []*kit.Service{Shop, Audit, Members, strict},
		kit.Mount(ReviewsModule),
		kit.Mount(ReviewsModule, kit.Only("staging"), kit.Bind(Screen, never)),
	)
	expectBinding(t, app.Graph(), "reviews.screening/port/screen",
		model.PortInfo{Fallback: "reviews.screening/query/by-length", Bound: "reviews.screening/query/by-length", Via: model.ViaFallback})
}

// The start refuses a module a mount for other environments leaves out that
// a mounted module requires, a kit.Only naming no environment, and a name
// that cannot be one — each where it is written.
func TestOnlyProblemsRefuseTheStart(t *testing.T) {
	never := kit.NewModule("never", "Mounted nowhere.")
	odd := kit.NewModule("odd", "Mounted in environments that cannot be.")
	de := startRefused(t, kit.NewApp("shop", Shop, Audit, Members).With(kit.Env(kit.EnvDev),
		kit.Mount(RatingsModule, kit.Only("staging")),
		kit.Mount(ReviewsModule),
		kit.Mount(never, kit.Only()),
		kit.Mount(odd, kit.Only("Prod", "a b")),
	))
	for said, where := range map[string]string{
		`module ratings is mounted only in staging, but reviews, mounted in dev, requires it`: `kit.Mount(RatingsModule, kit.Only("staging"))`,
		`module never is mounted with kit.Only and no environment`:                            `kit.Mount(never, kit.Only())`,
		`kit.Only of module odd names "Prod", which is not an environment's name`:             `kit.Mount(odd, kit.Only("Prod", "a b"))`,
		`kit.Only of module odd names "a b", which is not an environment's name`:              `kit.Mount(odd, kit.Only("Prod", "a b"))`,
	} {
		expectProblem(t, de, "module_only_test.go", said, where)
	}
}
