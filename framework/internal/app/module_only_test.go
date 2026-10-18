package app_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// app.Only (#274 Q3, ADR 0178's decisions of 15 October): a mount that holds
// only in the environments it names — elsewhere as if it had not been given,
// its prefix and its bindings with it — and what the start refuses of it.

// A mount given app.Only mounts its module in an environment it names, and
// not in another: the module, the module it requires and their routes are
// absent there.
func TestOnlyMountsAModuleInTheEnvironmentsItNames(t *testing.T) {
	a := start(t, app.Mount(ReviewsModule, app.Only("dev", "staging")))
	if a.Graph().ModuleOf("reviews") == nil {
		t.Fatal("a mount for dev is not mounted in dev")
	}
	if r := call(t, a, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusOK {
		t.Errorf("the module's route is not served in dev: %d %s", r.status, r.body)
	}
	stopApp(t, a)

	a = start(t, app.Mount(ReviewsModule, app.Only("staging")))
	for _, name := range []string{"reviews", "ratings"} {
		if m := a.Graph().ModuleOf(name); m != nil {
			t.Errorf("module %s is mounted in dev by a mount for staging: %+v", name, m)
		}
	}
	if r := call(t, a, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusNotFound {
		t.Errorf("a module mounted only in staging serves its route in dev: %d", r.status)
	}
}

// The graph says in which environments a module is mounted: the union of
// the environments its mounts name, sorted, when every mount of it is given
// app.Only; nothing when one mount holds everywhere, nor for a module only
// another module's app.Requires mounts — as its Mount says nothing.
func TestTheGraphSaysWhereAModuleIsMounted(t *testing.T) {
	type tc struct {
		name   string
		mounts []app.AppConfigurer
		// reviews is the Only the reviews module says; ratings is whether
		// the module it requires is checked to say none.
		reviews []string
		ratings bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		a := start(t, c.mounts...)
		g := a.Graph()
		if m := g.ModuleOf("reviews"); m == nil || !slices.Equal(m.Only, c.reviews) {
			t.Errorf("reviews says %+v, want only %q", m, c.reviews)
		}
		if m := g.ModuleOf("ratings"); c.ratings && (m == nil || m.Only != nil) {
			t.Errorf("a module another module requires says %+v", m)
		}
	}
	for _, c := range []tc{
		{
			name: "every mount limited",
			mounts: []app.AppConfigurer{
				app.Mount(ReviewsModule, app.Only("staging", "dev")),
				app.Mount(ReviewsModule, app.Only("dev", "qa")),
			},
			reviews: []string{"dev", "qa", "staging"},
			ratings: true,
		},
		{
			name: "one mount everywhere",
			mounts: []app.AppConfigurer{
				app.Mount(ReviewsModule, app.Only("staging")),
				app.Mount(ReviewsModule),
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The environment is the name KIT_ENV gives, lower-cased, and app.Env wins
// over it as it does for everything else.
func TestOnlyReadsTheEnvironmentsName(t *testing.T) {
	t.Setenv("KIT_ENV", "Staging")
	a := app.NewApp("shop", Shop, Audit, Members).With(app.Mount(ReviewsModule, app.Only("staging")))
	if !mountsReviews(a) {
		t.Error("KIT_ENV=Staging does not hold a mount for staging")
	}
	if mountsReviews(a.With(app.Env("production"))) {
		t.Error("app.Env(production) holds a mount for staging")
	}
	t.Setenv("KIT_ENV", "")
	if !mountsReviews(app.NewApp("shop", Shop, Audit, Members).With(app.Mount(ReviewsModule, app.Only("production")))) {
		t.Error("an unset KIT_ENV is not production")
	}
}

// The start resolves the mounts again for the environment it reads: an app
// declared before KIT_ENV changed mounts what the environment it starts in
// says, and keeps doing so when it starts again.
func TestOnlyIsResolvedAgainAtTheStart(t *testing.T) {
	t.Setenv("KIT_SMTP_URL", "")
	t.Setenv("KIT_ENV", "")
	a := app.NewApp("shop", Shop, Audit, Members).With(app.Mount(ReviewsModule, app.Only("dev")),
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Analyze(false), app.Logs(io.Discard))
	if mountsReviews(a) {
		t.Fatal("a mount for dev holds in production")
	}
	t.Setenv("KIT_ENV", "dev")
	for range 2 {
		if err := a.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		if !mountsReviews(a) {
			t.Error("the start in dev does not mount what a mount for dev mounts")
		}
		if r := call(t, a, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"}); r.status != http.StatusOK {
			t.Errorf("the module's route in dev: %d %s", r.status, r.body)
		}
		stopApp(t, a)
	}
}

// mountsReviews reports whether app, described without running, mounts the
// reviews module.
func mountsReviews(a *app.App) bool {
	return a.Graph().ModuleOf("reviews") != nil
}

// Outside its environments a mount is as if it had not been given: another
// mount of the module without app.Only still mounts it, at its own prefix,
// and the excluded mount's binding is neither applied nor judged.
func TestAMountOutsideItsEnvironmentsIsNotGiven(t *testing.T) {
	a := start(t,
		app.Mount(ReviewsModule, app.Only("staging"), app.Prefix("/elsewhere/")),
		app.Mount(ReviewsModule),
	)
	if m := a.Graph().ModuleOf("reviews"); m == nil || m.Prefix != "/reviews/" {
		t.Errorf("the mount that holds is not the one applied: %+v", m)
	}
	stopApp(t, a)

	strict := app.NewService("strict", "The product's own screening.")
	never := strict.Query("never", func(_ context.Context, r Review) (Verdict, error) {
		return Verdict{ID: r.ID, By: "product"}, nil
	})
	a = startApp(t, []*app.Service{Shop, Audit, Members, strict},
		app.Mount(ReviewsModule),
		app.Mount(ReviewsModule, app.Only("staging"), app.Bind(Screen, never)),
	)
	expectBinding(t, a.Graph(), "reviews.screening/port/screen",
		model.PortInfo{Fallback: "reviews.screening/query/by-length", Bound: "reviews.screening/query/by-length", Via: model.ViaFallback})
}

// The start refuses a module a mount for other environments leaves out that
// a mounted module requires, an app.Only naming no environment, and a name
// that cannot be one — each where it is written.
func TestOnlyProblemsRefuseTheStart(t *testing.T) {
	never := app.NewModule("never", "Mounted nowhere.")
	odd := app.NewModule("odd", "Mounted in environments that cannot be.")
	de := startRefused(t, app.NewApp("shop", Shop, Audit, Members).With(app.Env(app.EnvDev),
		app.Mount(RatingsModule, app.Only("staging")),
		app.Mount(ReviewsModule),
		app.Mount(never, app.Only()),
		app.Mount(odd, app.Only("Prod", "a b")),
	))
	for said, where := range map[string]string{
		`module ratings is mounted only in staging, but reviews, mounted in dev, requires it`: `app.Mount(RatingsModule, app.Only("staging"))`,
		`module never is mounted with app.Only and no environment`:                            `app.Mount(never, app.Only())`,
		`app.Only of module odd names "Prod", which is not an environment's name`:             `app.Mount(odd, app.Only("Prod", "a b"))`,
		`app.Only of module odd names "a b", which is not an environment's name`:              `app.Mount(odd, app.Only("Prod", "a b"))`,
	} {
		expectProblem(t, de, "module_only_test.go", said, where)
	}
}
