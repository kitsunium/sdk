package kit_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// The module under test, reviews, beside the shop of product_test.go: two
// services — reviews, named like the module, and screening —, two stores, a
// setting, a secret, a port with a fallback and a watch; and ratings, the
// module it requires. Declarations are package-level, as a module's author
// writes them; the positions test reads this file.

var Reviewing = kit.NewService("reviews", "Reviews of the shop's items.")

var Screening = kit.NewService("screening", "Screens a review before it shows.")

var Starring = kit.NewService("ratings", "Stars given to the items.")

// Review is moderated content of the module's own: its watch never hears it.
type Review struct {
	ID    string `json:"id"`
	Text  string `json:"text" validate:"required" kit:"moderated"`
	Shown bool   `json:"shown"`
}

type Verdict struct {
	ID   string `json:"id"`
	Show bool   `json:"show"`
	By   string `json:"by"`
}

// Star is another module's: its note is moderated, so the reviews hear it.
type Star struct {
	Item  string `json:"item"`
	Count int    `json:"count"`
	Note  string `json:"note,omitempty" kit:"moderated"`
}

var Reviews = Reviewing.Store("reviews", func(r Review) string { return r.ID })

var (
	_ = Reviewing.Endpoint("POST /reviews", PostReview)
	_ = Reviewing.Endpoint("GET /reviews/{id}", GetReview)
)

// Screen decides whether a review shows: the product may say, and the
// module's own rule answers otherwise.
var Screen = Screening.Port[Review, Verdict]("screen", kit.Fallback(ScreenAPI))

var ScreenAPI = Screening.Endpoint("POST /internal/screen", ScreenByLength, kit.Private())

var Verdicts = Screening.Store("verdicts", func(v Verdict) string { return v.ID })

// MinLength is the shortest review shown.
var MinLength = Screening.Setting("min-length", 3)

// SigningKey signs what the module hands out: kit makes it.
var SigningKey = Screening.Secret("signing-key", kit.Generated(32))

// Moderation hears every write of a field marked moderated outside the
// module — the shop's items, the ratings' stars —, never of its own reviews
// (watch_test.go).
var Moderation = Screening.Watch("moderation", kit.Moderated, Hear)

var Stars = Starring.Store("stars", func(s Star) string { return s.Item })

var _ = Starring.Endpoint("GET /stars", CountStars)

// The modules' hand-written loops say when they stop, and in which order.
var (
	_ = Reviewing.Go("sweeper", func(ctx context.Context) error { return waitStopped(ctx, "reviews") })
	_ = Starring.Go("tally", func(ctx context.Context) error { return waitStopped(ctx, "ratings") })
)

var RatingsModule = kit.NewModule("ratings", "Stars given to the items.", Starring)

var ReviewsModule = kit.NewModule("reviews", "Reviews of the shop's items, screened.\n\nfr: Les avis sur les articles, modérés.",
	Reviewing, Screening, kit.Requires(RatingsModule))

// PostReview keeps a review, shown when the screening says so.
func PostReview(ctx context.Context, r Review) (Review, error) {
	v, err := Screen.Call(ctx, r)
	if err != nil {
		return Review{}, err
	}
	r.Shown = v.Show
	return r, Reviews.Put(ctx, r)
}

type ReviewID struct {
	ID string `path:"id"`
}

// GetReview reads a review.
func GetReview(ctx context.Context, in ReviewID) (Review, error) { return Reviews.Get(ctx, in.ID) }

// ScreenByLength shows a review as long as the module's setting asks.
func ScreenByLength(ctx context.Context, r Review) (Verdict, error) {
	v := Verdict{ID: r.ID, Show: len(r.Text) >= MinLength.Get(), By: "length"}
	return v, Verdicts.Put(ctx, v)
}

// CountStars says how many items have stars.
func CountStars(ctx context.Context, _ kit.EmptyValue) (int, error) { return Stars.Count(ctx) }

// stopped records the order the loops stop in.
var stopped struct {
	sync.Mutex
	names []string
}

func waitStopped(ctx context.Context, name string) error {
	<-ctx.Done()
	stopped.Lock()
	stopped.names = append(stopped.names, name)
	stopped.Unlock()
	return ctx.Err()
}

// startReviews runs the shop with the reviews module mounted.
func startReviews(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	return start(t, append([]kit.AppConfigurer{kit.Mount(ReviewsModule)}, opts...)...)
}

// startRefused starts app and returns what its start refused.
func startRefused(t *testing.T, app *kit.App) *kit.DiagnosticsError {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	err := app.With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want every problem", err)
	}
	return de
}

// A module adopts its services: what they declared before is renamed, what
// they declare after is born qualified, and the one named like the module
// takes the module's name.
func TestAModuleQualifiesItsServices(t *testing.T) {
	desk := kit.NewService("desk", "A module's service.")
	own := kit.NewService("board", "The service named like its module.")
	early := desk.Store("early", func(s string) string { return s })
	kit.NewModule("board", "A module.", desk, own)
	late := desk.Store("late", func(s string) string { return s })
	for got, want := range map[string]string{
		desk.Name(): "board.desk", own.Name(): "board",
		early.ID(): "board.desk/store/early", late.ID(): "board.desk/store/late",
	} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}

// Mounted, a module brings its services, qualified and marked, and the
// module it requires.
func TestAModuleIsMountedWithWhatItRequires(t *testing.T) {
	g := startReviews(t).Graph()
	for id, module := range map[string]string{
		"reviews": "reviews", "reviews/store/reviews": "reviews", "reviews.screening": "reviews",
		"reviews.screening/port/screen": "reviews", "reviews.screening/secret/signing-key": "reviews",
		"ratings": "ratings", "ratings/store/stars": "ratings", "shop/store/items": "",
	} {
		if n := g.Node(id); n == nil || n.Module != module {
			t.Errorf("%s: %+v, want module %q", id, n, module)
		}
	}
}

// moduleView is what a test checks of a module the graph says.
type moduleView struct {
	services, requires, requiredBy, prefix, pkg, goModule string
	mounted, french                                       bool
}

// viewOf is a module as moduleView says it; the zero view when it is none.
func viewOf(m *model.Module) moduleView {
	if m == nil {
		return moduleView{}
	}
	v := moduleView{
		services: strings.Join(m.Services, ","), requires: strings.Join(m.Requires, ","), requiredBy: strings.Join(m.RequiredBy, ","),
		prefix: m.Prefix, pkg: m.Package, mounted: m.Mount != nil, french: m.Docs["fr"] != "",
	}
	if m.Build != nil {
		v.goModule = m.Build.Module
	}
	return v
}

// The graph says each module the app mounts — its services, what it
// requires and what required it, its prefix, its package and Go module —;
// the one another required is mounted at its defaults, by nobody.
func TestTheGraphSaysTheModules(t *testing.T) {
	g := startReviews(t).Graph()
	const kitTest, platform = "github.com/kitsunium/sdk/framework/internal/kit_test", "github.com/kitsunium/sdk/framework"
	for name, want := range map[string]moduleView{
		"reviews": {services: "reviews,reviews.screening", requires: "ratings", prefix: "/reviews/", pkg: kitTest, goModule: platform, mounted: true, french: true},
		"ratings": {services: "ratings", requiredBy: "reviews", prefix: "/ratings/", pkg: kitTest, goModule: platform},
	} {
		if got := viewOf(g.ModuleOf(name)); got != want {
			t.Errorf("module %s: %+v, want %+v", name, got, want)
		}
	}
}
