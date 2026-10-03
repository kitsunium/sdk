package kit_test

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

	"github.com/kitsunium/sdk/framework/internal/kit"
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
	app := startOnDisk(t, dir, kit.Mount(ReviewsModule))
	r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "lovely"})
	if r.status != http.StatusOK {
		t.Fatalf("POST /reviews/reviews: %d %s", r.status, r.body)
	}
	var got Review
	r.json(t, &got)
	if !got.Shown {
		t.Errorf("the module's own screening did not show a long review: %+v", got)
	}
	if r := call(t, app, "GET /reviews/reviews/r1", noBody); r.status != http.StatusOK {
		t.Errorf("GET /reviews/reviews/r1: %d", r.status)
	}
	if r := call(t, app, "GET /ratings/stars", noBody); r.status != http.StatusOK {
		t.Errorf("the required module's route: %d", r.status)
	}
	if r := call(t, app, "POST /reviews", Review{ID: "r2", Text: "x"}); r.status != http.StatusNotFound {
		t.Errorf("a module's route is served outside its prefix: %d", r.status)
	}
	if ep := app.Graph().Node("reviews/endpoint/PostReview").Endpoint; ep.Path != "/reviews/reviews" {
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
func startOnDisk(t *testing.T, dir string, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	app := kit.NewApp("shop", Shop, Audit, Members).With(append([]kit.AppConfigurer{
		kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stopApp(t, app) })
	return app
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
	app := startReviews(t, kit.Mount(ReviewsModule, kit.Prefix("/")))
	if r := call(t, app, "POST /reviews", Review{ID: "r1", Text: "no"}); r.status != http.StatusOK {
		t.Fatalf("POST /reviews at the root: %d %s", r.status, r.body)
	}
	if ReviewsModule.Prefix() != "/" {
		t.Errorf("the prefix given last: %q", ReviewsModule.Prefix())
	}
	if m := app.Graph().ModuleOf("reviews"); m == nil || m.Prefix != "/" {
		t.Errorf("the graph's prefix: %+v", m)
	}
}

// A module's settings are read from its section of a file, from
// <APP>_<MODULE>_<NAME> and from kit.Set, by their qualified key.
func TestAModulesSettingsAreQualified(t *testing.T) {
	files := fstest.MapFS{"config/config.yaml": {Data: []byte("reviews:\n  min-length: 5\n")}}
	app := startReviews(t, kit.ConfigFiles(files))
	if got := MinLength.Get(); got != 5 {
		t.Errorf("the section's value: %d", got)
	}
	var s *model.Setting
	for i, x := range app.Graph().Runtime.Config {
		if x.Key == "reviews.min-length" {
			s = &app.Graph().Runtime.Config[i]
		}
	}
	if s == nil || s.Name != "SHOP_REVIEWS_MIN_LENGTH" || s.Service != "reviews.screening" || s.From != model.SettingFile || s.Value != "5" {
		t.Errorf("the configuration says %+v", s)
	}
	stopApp(t, app)
	t.Setenv("SHOP_REVIEWS_MIN_LENGTH", "7")
	startReviews(t, kit.ConfigFiles(files))
	if got := MinLength.Get(); got != 7 {
		t.Errorf("the variable's value: %d", got)
	}
}

// stopApp stops an app before the test ends: its services are free for the
// next one.
func stopApp(t *testing.T, app *kit.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// kit.Set names a module's setting by its qualified key; a file that sets
// one outside its section, or a key its section does not declare, is
// refused like any unknown key.
func TestAModulesSettingIsSetByItsKey(t *testing.T) {
	startReviews(t, kit.Set("reviews.min-length", 9))
	if got := MinLength.Get(); got != 9 {
		t.Errorf("kit.Set's value: %d", got)
	}
	files := fstest.MapFS{"config/config.yaml": {Data: []byte("reviews.min-length: 5\nreviews:\n  max-length: 9\nratings: 1\n")}}
	de := startRefused(t, kit.NewApp("shop", Shop, Audit, Members).With(kit.Mount(ReviewsModule), kit.ConfigFiles(files)))
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
	app := startReviews(t, kit.SecretStore(store))
	if _, err := store.Versions(t.Context(), "reviews-signing-key"); err != nil {
		t.Errorf("the generated secret is not kept as reviews-signing-key: %v", err)
	}
	if info := app.Graph().Node("reviews.screening/secret/signing-key").Secret; info == nil || info.Variable != "SHOP_REVIEWS_SIGNING_KEY" {
		t.Errorf("the secret's variable: %+v", info)
	}
}

// The modules start first — the required one before the one that requires
// it — and stop last, in reverse.
func TestModulesStartFirstAndStopLast(t *testing.T) {
	stopped.Lock()
	stopped.names = nil
	stopped.Unlock()
	app := startReviews(t)
	var order []string
	for _, c := range app.Graph().Runtime.Components {
		if name, ok := strings.CutPrefix(c.Name, "store:"); ok {
			order = append(order, name)
		}
	}
	stars, reviews, items := slices.Index(order, "ratings/store/stars"), slices.Index(order, "reviews/store/reviews"), slices.Index(order, "shop/store/items")
	if stars < 0 || reviews < stars || items < reviews {
		t.Errorf("the stores start in the order %v", order)
	}
	stopApp(t, app)
	stopped.Lock()
	defer stopped.Unlock()
	if !slices.Equal(stopped.names, []string{"reviews", "ratings"}) {
		t.Errorf("the loops stopped in the order %v, want reviews then ratings", stopped.names)
	}
}

// kit.Bind, given to the mount, binds one of the module's ports.
func TestAMountBindsTheModulesPorts(t *testing.T) {
	strict := kit.NewService("strict", "The product's own screening.")
	never := strict.Query("never", func(_ context.Context, r Review) (Verdict, error) {
		return Verdict{ID: r.ID, By: "product"}, nil
	})
	app := startApp(t, []*kit.Service{Shop, Audit, Members, strict}, kit.Mount(ReviewsModule, kit.Bind(Screen, never)))
	r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "long enough"})
	var got Review
	r.json(t, &got)
	if r.status != http.StatusOK || got.Shown {
		t.Errorf("the product's binding did not answer: %d %+v", r.status, got)
	}
	expectBinding(t, app.Graph(), "reviews.screening/port/screen",
		model.PortInfo{Fallback: "reviews.screening/query/by-length", Bound: "strict/query/never", Via: model.ViaBind})
}

// startColliding starts an app whose modules collide with each other and
// with the product in every way the start refuses, and returns what it
// refused.
func startColliding(t *testing.T) *kit.DiagnosticsError {
	t.Helper()
	desk := kit.NewService("desk", "A service two modules list.")
	one := kit.NewModule("one", "The first.", desk)
	two := kit.NewModule("two", "The second.", desk)
	limits := kit.NewService("limits", "The other one's service.")
	limits.Setting("limit", 1)
	other := kit.NewModule("one", "Another module named one.", limits)
	product := kit.NewService("orders", "The product's.")
	product.Setting("one-limit", 2)
	product.Setting("two", "x")
	named := kit.NewService("one", "A service named like a module.")
	fixed := product.Query("fixed", fixedPrice)
	price := product.Port[QuoteInput, Quoted]("price", kit.Fallback(fixed))
	var none *kit.Module
	return startRefused(t, kit.NewApp("shop", product, named, desk).With(
		kit.Mount(one), kit.Mount(two, kit.Bind(price, fixed)), kit.Mount(other), none, kit.Mount(nil),
		kit.Mount(kit.NewModule("kit", "Reserved.")), kit.Mount(kit.NewModule("Bad", "Upper case.")),
		kit.Mount(kit.NewModule("schema", "Its version table would be the product's.")),
		kit.Mount(kit.NewModule("privacy", "Kit's own service.", kit.Privacy)),
		kit.Mount(kit.NewModule("under", "Under kit's own."), kit.Prefix("/_kit/under/")),
	))
}

// The start refuses what collides once qualified — every problem at once,
// each where it is written, the other declaration named.
func TestModuleCollisionsRefuseTheStart(t *testing.T) {
	de := startColliding(t)
	for said, where := range map[string]string{
		`module two lists service "one.desk", which module one lists already`:                           `two := kit.NewModule("two"`,
		`the app mounts service "one.desk" itself, which module one lists`:                              `kit.NewApp("shop", product, named, desk)`,
		`two modules the app mounts are named one`:                                                      `other := kit.NewModule("one"`,
		`setting "one-limit" of orders and "one.limit" of one.limits share the variable SHOP_ONE_LIMIT`: `product.Setting("one-limit"`,
		`setting "two" of orders is named like a module the app mounts`:                                 `product.Setting("two"`,
		`the product's service "one" is named like a module the app mounts`:                             `named := kit.NewService("one"`,
		`kit.Mount of module two binds port orders/port/price, which is not the module's`:               `kit.Mount(two, kit.Bind(price, fixed))`,
		`the app mounts a nil module`:                                                                   `kit.NewApp("shop", product, named, desk)`,
		`module name "kit" is kit's`:                                                                    `kit.Mount(kit.NewModule("kit"`,
		`module name "schema" is kit's`:                                                                 `kit.Mount(kit.NewModule("schema"`,
		`module name "Bad" must be lower-case letters`:                                                  `kit.Mount(kit.NewModule("Bad"`,
		`module privacy lists kit.privacy, kit's own service`:                                           `kit.Mount(kit.NewModule("privacy"`,
		`module under is mounted under "/_kit/under/"`:                                                  `kit.Prefix("/_kit/under/")`,
	} {
		expectProblem(t, de, "module_mount_test.go", said, where)
	}
}

// At "/", a module shares the product's route space: a route both declare
// is refused, naming both.
func TestAModulesRouteMayCollideAtTheRoot(t *testing.T) {
	who := kit.NewService("who", "A module's service with the shop's route.")
	who.Endpoint("GET /whoami", func(context.Context, kit.EmptyValue) (string, error) { return "module", nil }, kit.Name("whoami"))
	m := kit.NewModule("who", "A module.", who)
	de := startRefused(t, kit.NewApp("shop", Shop, Audit, Members).With(kit.Mount(m, kit.Prefix("/"))))
	d := diagnosticSaying(de.Diagnostics, `route "GET /whoami" of`)
	if d == nil || !strings.Contains(d.Message, "who/endpoint/whoami") || !strings.Contains(d.Message, "shop/endpoint/WhoAmI") {
		t.Errorf("the conflict does not name both routes: %v", de)
	}
}

// A module's service may share its bare name with one of the product's —
// the product's audit and the module's —: qualified, they never meet.
func TestAModulesServiceMayShareAProductServicesName(t *testing.T) {
	audit := kit.NewService("audit", "The module's own audit.")
	audit.Store("events", func(s string) string { return s })
	g := start(t, kit.NewModule("trail", "A module with an audit.", audit)).Graph()
	for _, id := range []string{"audit", "audit/subscription/record", "trail.audit", "trail.audit/store/events"} {
		if g.Node(id) == nil {
			t.Errorf("no node %s", id)
		}
	}
}
