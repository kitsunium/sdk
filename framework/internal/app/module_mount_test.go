package app_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The reviews module (module_test.go) mounted and run: its routes under a
// prefix and at the root, its settings and its secret by their qualified
// names, the order it starts and stops in, a mount's binding, and every
// collision the start refuses.

// A module's routes are served under its prefix, "/<module>/" by default,
// and its data lies in its services' qualified directories.
func TestAModulesRoutesAreUnderItsPrefix(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	a := startOnDisk(t, dir, app.Mount(ReviewsModule))
	r := call(t, a, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"})
	if r.status != http.StatusOK {
		t.Fatalf("POST /reviews/reviews: %d %s", r.status, r.body)
	}
	var got Review
	r.json(t, &got)
	if !got.Shown {
		t.Errorf("the module's own screening did not show a long review: %+v", got)
	}
	if r := call(t, a, "GET /reviews/reviews/r1", noBody); r.status != http.StatusOK {
		t.Errorf("GET /reviews/reviews/r1: %d", r.status)
	}
	if r := call(t, a, "GET /ratings/stars", noBody); r.status != http.StatusOK {
		t.Errorf("the required module's route: %d", r.status)
	}
	if r := call(t, a, "POST /reviews", Review{ID: "r2", Text: "x"}); r.status != http.StatusNotFound {
		t.Errorf("a module's route is served outside its prefix: %d", r.status)
	}
	if ep := a.Graph().Node("reviews/endpoint/PostReview").Endpoint; ep.Path != "/reviews/reviews" {
		t.Errorf("the graph says the route %q", ep.Path)
	}
	if ReviewsModule.Prefix() != "/reviews/" {
		t.Errorf("the running module's prefix: %q", ReviewsModule.Prefix())
	}
	for _, file := range []string{"reviews/reviews.json", "reviews.screening/verdicts.json"} {
		eventuallyFile(t, dir, file)
	}
}

// startOnDisk runs the shop with its data in dir.
func startOnDisk(t *testing.T, dir string, opts ...app.AppConfigurer) *app.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	a := app.NewApp("shop", Shop, Audit, Members).With(append([]app.AppConfigurer{
		app.DataDir(dir), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stopApp(t, a) })
	return a
}

// eventuallyFile waits for a store's snapshot, or its journal, to appear.
func eventuallyFile(t *testing.T, dir, file string) {
	t.Helper()
	eventually(t, file, func() bool {
		_, snapshot := os.Stat(filepath.Join(dir, file))
		_, journal := os.Stat(filepath.Join(dir, file+".d"))
		return snapshot == nil || journal == nil
	})
}

// At "/", a module shares the product's route space: its routes are the
// ones it declares.
func TestAModuleMountedAtTheRoot(t *testing.T) {
	a := startReviews(t, app.Mount(ReviewsModule, app.Prefix("/")))
	if r := call(t, a, "POST /reviews", Review{ID: "r1", Text: "no"}); r.status != http.StatusOK {
		t.Fatalf("POST /reviews at the root: %d %s", r.status, r.body)
	}
	if ReviewsModule.Prefix() != "/" {
		t.Errorf("the prefix given last: %q", ReviewsModule.Prefix())
	}
	if m := a.Graph().ModuleOf("reviews"); m == nil || m.Prefix != "/" {
		t.Errorf("the graph's prefix: %+v", m)
	}
}

// A module's settings are read from its section of a file, from
// <APP>_<MODULE>_<NAME> and from app.Set, by their qualified key.
func TestAModulesSettingsAreQualified(t *testing.T) {
	files := fstest.MapFS{"config/config.yaml": {Data: []byte("reviews:\n  min-length: 5\n")}}
	a := startReviews(t, app.ConfigFiles(files))
	if got := MinLength.Get(); got != 5 {
		t.Errorf("the section's value: %d", got)
	}
	var s *model.Setting
	for i, x := range a.Graph().Runtime.Config {
		if x.Key == "reviews.min-length" {
			s = &a.Graph().Runtime.Config[i]
		}
	}
	if s == nil || s.Name != "SHOP_REVIEWS_MIN_LENGTH" || s.Service != "reviews.screening" || s.From != model.SettingFile || s.Value != "5" {
		t.Errorf("the configuration says %+v", s)
	}
	stopApp(t, a)
	t.Setenv("SHOP_REVIEWS_MIN_LENGTH", "7")
	startReviews(t, app.ConfigFiles(files))
	if got := MinLength.Get(); got != 7 {
		t.Errorf("the variable's value: %d", got)
	}
}

// stopApp stops an app before the test ends: its services are free for the
// next one.
func stopApp(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// app.Set names a module's setting by its qualified key; a file that sets
// one outside its section, or a key its section does not declare, is
// refused like any unknown key.
func TestAModulesSettingIsSetByItsKey(t *testing.T) {
	startReviews(t, app.Set("reviews.min-length", 9))
	if got := MinLength.Get(); got != 9 {
		t.Errorf("app.Set's value: %d", got)
	}
	files := fstest.MapFS{"config/config.yaml": {Data: []byte("reviews.min-length: 5\nreviews:\n  max-length: 9\nratings: 1\n")}}
	de := startRefused(t, app.NewApp("shop", Shop, Audit, Members).With(app.Mount(ReviewsModule), app.ConfigFiles(files)))
	for _, said := range []string{
		`config/config.yaml sets "reviews.min-length", which no service declares`,
		`config/config.yaml sets "reviews.max-length", which no service declares`,
		`config/config.yaml: "ratings:" is module ratings's section`,
	} {
		if diagnosticSaying(de.Diagnostics, said) == nil {
			t.Errorf("no problem says %q:\n%v", said, de)
		}
	}
}

// A module's secret is kept under its qualified name, and read from its
// qualified variable.
func TestAModulesSecretIsQualified(t *testing.T) {
	store := secret.NewMemory(secret.MemoryConfig{})
	a := startReviews(t, app.SecretStore(store))
	if _, err := store.Versions(t.Context(), "reviews-signing-key"); err != nil {
		t.Errorf("the generated secret is not kept as reviews-signing-key: %v", err)
	}
	if info := a.Graph().Node("reviews.screening/secret/signing-key").Secret; info == nil || info.Variable != "SHOP_REVIEWS_SIGNING_KEY" {
		t.Errorf("the secret's variable: %+v", info)
	}
}

// The modules start first — the required one before the one that requires
// it — and stop last, in reverse.
func TestModulesStartFirstAndStopLast(t *testing.T) {
	stopped.Lock()
	stopped.names = nil
	stopped.Unlock()
	a := startReviews(t)
	var order []string
	for _, c := range a.Graph().Runtime.Components {
		if name, ok := strings.CutPrefix(c.Name, "store:"); ok {
			order = append(order, name)
		}
	}
	stars, reviews, items := slices.Index(order, "ratings/store/stars"), slices.Index(order, "reviews/store/reviews"), slices.Index(order, "shop/store/items")
	if stars < 0 || reviews < stars || items < reviews {
		t.Errorf("the stores start in the order %v", order)
	}
	stopApp(t, a)
	stopped.Lock()
	defer stopped.Unlock()
	if !slices.Equal(stopped.names, []string{"reviews", "ratings"}) {
		t.Errorf("the loops stopped in the order %v, want reviews then ratings", stopped.names)
	}
}

// app.Bind, given to the mount, binds one of the module's ports.
func TestAMountBindsTheModulesPorts(t *testing.T) {
	strict := app.NewService("strict", "The product's own screening.")
	never := strict.Query("never", func(_ context.Context, r Review) (Verdict, error) {
		return Verdict{ID: r.ID, By: "product"}, nil
	})
	a := startApp(t, []*app.Service{Shop, Audit, Members, strict}, app.Mount(ReviewsModule, app.Bind(Screen, never)))
	r := call(t, a, "POST /reviews/reviews", Review{ID: "r1", Text: "long enough"})
	var got Review
	r.json(t, &got)
	if r.status != http.StatusOK || got.Shown {
		t.Errorf("the product's binding did not answer: %d %+v", r.status, got)
	}
	expectBinding(t, a.Graph(), "reviews.screening/port/screen",
		model.PortInfo{Fallback: "reviews.screening/query/by-length", Bound: "strict/query/never", Via: model.ViaBind})
}

// startColliding starts an app whose modules collide with each other and
// with the product in every way the start refuses, and returns what it
// refused.
func startColliding(t *testing.T) *app.DiagnosticsError {
	t.Helper()
	desk := app.NewService("desk", "A service two modules list.")
	one := app.NewModule("one", "The first.", desk)
	two := app.NewModule("two", "The second.", desk)
	limits := app.NewService("limits", "The other one's service.")
	limits.Setting("limit", 1)
	other := app.NewModule("one", "Another module named one.", limits)
	product := app.NewService("orders", "The product's.")
	product.Setting("one-limit", 2)
	product.Setting("two", "x")
	named := app.NewService("one", "A service named like a module.")
	fixed := product.Query("fixed", fixedPrice)
	price := product.Port[QuoteInput, Quoted]("price", app.Fallback(fixed))
	var none *app.Module
	return startRefused(t, app.NewApp("shop", product, named, desk).With(
		app.Mount(one), app.Mount(two, app.Bind(price, fixed)), app.Mount(other), none, app.Mount(nil),
		app.Mount(app.NewModule("kit", "Reserved.")), app.Mount(app.NewModule("Bad", "Upper case.")),
		app.Mount(app.NewModule("schema", "Its version table would be the product's.")),
		app.Mount(app.NewModule("privacy", "Kit's own service.", app.Privacy)),
		app.Mount(app.NewModule("under", "Under kit's own."), app.Prefix("/_kit/under/")),
	))
}

// The start refuses what collides once qualified — every problem at once,
// each where it is written, the other declaration named.
func TestModuleCollisionsRefuseTheStart(t *testing.T) {
	de := startColliding(t)
	for said, where := range map[string]string{
		`module two lists service "one.desk", which module one lists already`:                           `two := app.NewModule("two"`,
		`the app mounts service "one.desk" itself, which module one lists`:                              `app.NewApp("shop", product, named, desk)`,
		`two modules the app mounts are named one`:                                                      `other := app.NewModule("one"`,
		`setting "one-limit" of orders and "one.limit" of one.limits share the variable SHOP_ONE_LIMIT`: `product.Setting("one-limit"`,
		`setting "two" of orders is named like a module the app mounts`:                                 `product.Setting("two"`,
		`the product's service "one" is named like a module the app mounts`:                             `named := app.NewService("one"`,
		`app.Mount of module two binds port orders/port/price, which is not the module's`:               `app.Mount(two, app.Bind(price, fixed))`,
		`the app mounts a nil module`:                                                                   `app.NewApp("shop", product, named, desk)`,
		`module name "kit" is kit's`:                                                                    `app.Mount(app.NewModule("kit"`,
		`module name "schema" is kit's`:                                                                 `app.Mount(app.NewModule("schema"`,
		`module name "Bad" must be lower-case letters`:                                                  `app.Mount(app.NewModule("Bad"`,
		`module privacy lists kit.privacy, kit's own service`:                                           `app.Mount(app.NewModule("privacy"`,
		`module under is mounted under "/_kit/under/"`:                                                  `app.Prefix("/_kit/under/")`,
	} {
		expectProblem(t, de, "module_mount_test.go", said, where)
	}
}

// At "/", a module shares the product's route space: a route both declare
// is refused, naming both.
func TestAModulesRouteMayCollideAtTheRoot(t *testing.T) {
	who := app.NewService("who", "A module's service with the shop's route.")
	who.Endpoint("GET /whoami", func(context.Context, app.EmptyValue) (string, error) { return "module", nil }, app.Name("whoami"))
	m := app.NewModule("who", "A module.", who)
	de := startRefused(t, app.NewApp("shop", Shop, Audit, Members).With(app.Mount(m, app.Prefix("/"))))
	d := diagnosticSaying(de.Diagnostics, `route "GET /whoami" of`)
	if d == nil || !strings.Contains(d.Message, "who/endpoint/whoami") || !strings.Contains(d.Message, "shop/endpoint/WhoAmI") {
		t.Errorf("the conflict does not name both routes: %v", de)
	}
}

// A module's service may share its bare name with one of the product's —
// the product's audit and the module's —: qualified, they never meet.
func TestAModulesServiceMayShareAProductServicesName(t *testing.T) {
	audit := app.NewService("audit", "The module's own audit.")
	audit.Store("events", func(s string) string { return s })
	g := start(t, app.NewModule("trail", "A module with an audit.", audit)).Graph()
	for _, id := range []string{"audit", "audit/subscription/record", "trail.audit", "trail.audit/store/events"} {
		if g.Node(id) == nil {
			t.Errorf("no node %s", id)
		}
	}
}
