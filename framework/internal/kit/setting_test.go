package kit_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// configured is a service with one setting of each kind.
type configured struct {
	svc      *kit.Service
	base     *kit.SettingService[string]
	delay    *kit.SettingService[time.Duration]
	limit    *kit.SettingService[int]
	ratio    *kit.SettingService[float64]
	open     *kit.SettingService[bool]
	hosts    *kit.SettingService[[]string]
	required *kit.SettingService[string]
}

func newConfigured() configured {
	svc := kit.NewService("notify", "Mail and its links.")
	return configured{
		svc:      svc,
		base:     svc.Setting("base-url", "http://localhost:4000"),
		delay:    svc.Setting("archive-after", 24*time.Hour),
		limit:    svc.Setting("page-size", 50),
		ratio:    svc.Setting("sample-ratio", 0.5),
		open:     svc.Setting("open-signup", false),
		hosts:    svc.Setting("hosts", []string{"a.example"}),
		required: svc.Setting("sender", "", kit.Required()),
	}
}

// settingOf finds a setting in the configuration the start read.
func settingOf(t *testing.T, app *kit.App, variable string) model.Setting {
	t.Helper()
	for _, s := range app.Graph().Runtime.Config {
		if s.Name == variable {
			return s
		}
	}
	t.Fatalf("no setting %s in the configuration", variable)
	return model.Setting{}
}

// A value is the default, overridden by the file every environment reads,
// by the environment's own, by the variable, and by the code — and the
// configuration says which.
func TestSettingsResolveInOrder(t *testing.T) {
	c := newConfigured()
	files := fstest.MapFS{
		"config/config.yaml":     {Data: []byte("page-size: 20\nsample-ratio: 1\nhosts: [x.example, y.example]\nsender: ops@example.com\n")},
		"config/production.yaml": {Data: []byte("page-size: 30\nopen-signup: true\n")},
		"config/dev.toml":        {Data: []byte("page-size = 99\n")},
	}
	t.Setenv("NEWS_BASE_URL", "https://news.example/")
	t.Setenv("NEWS_ARCHIVE_AFTER", "90m")
	app := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
		kit.ConfigFiles(files), kit.Set("open-signup", "false"))

	if got := c.limit.Get(); got != 50 {
		t.Fatalf("before the start, the default: %d", got)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())

	if got := c.base.Get(); got != "https://news.example/" {
		t.Errorf("base-url %q", got)
	}
	if got := c.delay.Get(); got != 90*time.Minute {
		t.Errorf("archive-after %s", got)
	}
	if got := c.limit.Get(); got != 30 {
		t.Errorf("page-size %d: production's file wins over the common one", got)
	}
	if got := c.ratio.Get(); got != 1 {
		t.Errorf("sample-ratio %v", got)
	}
	if got := c.open.Get(); got {
		t.Error("open-signup: kit.Set wins over the file")
	}
	if got := c.hosts.Get(); len(got) != 2 || got[1] != "y.example" {
		t.Errorf("hosts %v", got)
	}
	c.hosts.Get()[0] = "changed"
	if c.hosts.Get()[0] != "x.example" {
		t.Error("a caller changed the list for everyone")
	}
	if got := c.required.Get(); got != "ops@example.com" {
		t.Errorf("sender %q", got)
	}

	for _, tc := range []struct {
		variable string
		want     model.Setting
	}{
		{"NEWS_BASE_URL", model.Setting{From: model.SettingEnv, Value: "https://news.example/", Type: "text"}},
		{"NEWS_ARCHIVE_AFTER", model.Setting{From: model.SettingEnv, Value: "1h30m0s", Type: "duration"}},
		{"NEWS_PAGE_SIZE", model.Setting{From: model.SettingFile, Detail: "config/production.yaml", Value: "30", Type: "int"}},
		{"NEWS_SAMPLE_RATIO", model.Setting{From: model.SettingFile, Detail: "config/config.yaml", Value: "1", Type: "number"}},
		{"NEWS_OPEN_SIGNUP", model.Setting{From: model.SettingOption, Option: "kit.Set", Value: "false", Type: "bool"}},
		{"NEWS_HOSTS", model.Setting{From: model.SettingFile, Detail: "config/config.yaml", Value: "x.example,y.example", Type: "list"}},
	} {
		variable, want := tc.variable, tc.want
		s := settingOf(t, app, variable)
		if s.From != want.From || s.Detail != want.Detail || s.Option != want.Option || s.Value != want.Value || s.Type != want.Type || s.Service != "notify" || s.Key == "" {
			t.Errorf("%s: %+v, want %+v", variable, s, want)
		}
	}
}

// With returns a new app and leaves the one it is called on as it was: a
// test's kit.Set ends with the test, and two copies share nothing an option
// changes.
func TestWithLeavesItsReceiverUntouched(t *testing.T) {
	c := newConfigured()
	product := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
		kit.Set("sender", "ops@example.com"))
	pageSize := func(app *kit.App) int {
		t.Helper()
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer app.Stop(context.Background())
		return c.limit.Get()
	}
	first := product.With(kit.Set("page-size", 7))
	if first == product {
		t.Fatal("With returned the app it was called on")
	}
	second := product.With(kit.Set("page-size", 8))
	if got := pageSize(first); got != 7 {
		t.Errorf("the copy's own kit.Set: page-size %d", got)
	}
	if got := pageSize(second); got != 8 {
		t.Errorf("a second copy: page-size %d — the first copy's option reached it", got)
	}
	if got := pageSize(product); got != 50 {
		t.Errorf("the app With was called on: page-size %d, want its default — a copy's option reached it", got)
	}
	if err := product.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer product.Stop(context.Background())
	if got := c.required.Get(); got != "ops@example.com" {
		t.Errorf("the options of the app With was called on are lost: sender %q", got)
	}
}

// In dev, the environment's file is config/dev.<ext>, whatever the format.
func TestTheEnvironmentNamesItsFile(t *testing.T) {
	c := newConfigured()
	files := fstest.MapFS{
		"config/dev.toml":    {Data: []byte("page-size = 7\nsender = \"dev@example.com\"\n")},
		"config/labs.json":   {Data: []byte(`{"page-size": 8, "sender": "labs@example.com"}`)},
		"config/config.json": {Data: []byte(`{"page-size": 1}`)},
	}
	for env, want := range map[string]int{"dev": 7, "labs": 8} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("KIT_ENV", env)
			app := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Studio(false), kit.Analyze(false), kit.Logs(io.Discard), kit.ConfigFiles(files))
			if err := app.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer app.Stop(context.Background())
			if got := c.limit.Get(); got != want {
				t.Errorf("page-size %d, want %d", got, want)
			}
		})
	}
}

// Every problem with the settings is said at once, each where it is.
func TestSettingProblems(t *testing.T) {
	c := newConfigured()
	other := kit.NewService("billing", "Invoices.")
	other.Setting("page-size", 10)
	other.Secret("stripe-key")
	other.Setting("stripe-key", "x")
	other.Setting("Bad_Name", "x")
	files := fstest.MapFS{
		"config/config.yaml":     {Data: []byte("page-size: ten\nhosts: nope\nstripe-key: sk_live_123\nunknown-key: 1\narchive-after: 90\n")},
		"config/production.yaml": {Data: []byte("page-size: 1\n")},
		"config/production.json": {Data: []byte(`{}`)},
	}
	t.Setenv("NEWS_OPEN_SIGNUP", "maybe")
	t.Setenv("NEWS_STRIPE_KEY", "sk_test")
	app := kit.NewApp("news", c.svc, other).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
		kit.ConfigFiles(files), kit.Set("nope", 1), kit.Set("sample-ratio", "half"))
	err := app.Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		`setting "page-size" is declared by notify and billing`,
		`setting "stripe-key" has the name of a secret`,
		`setting name "Bad_Name" must be 1 to 63 lower-case letters`,
		"config/config.yaml: page-size is not a whole number",
		"config/config.yaml: hosts is not a list of texts",
		`config/config.yaml: archive-after is not a duration with its unit, as a text: "90s", "15m", "24h"`,
		`config/config.yaml sets "stripe-key", a secret: a file is committed and a secret never is`,
		`config/config.yaml sets "unknown-key", which no service declares`,
		"config/production.json and config/production.yaml: one file per environment",
		"NEWS_OPEN_SIGNUP is not a boolean",
		`kit.Set names "nope", which no service declares`,
		`kit.Set("sample-ratio"): the value is not a number`,
		`setting "sender" of notify is required: set NEWS_SENDER, or "sender" in config/production.yaml`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "sk_live_123") || strings.Contains(msg, "maybe") || strings.Contains(msg, "half") {
		t.Errorf("a problem quotes a value:\n%s", msg)
	}
}

// A variable under the product's prefix that nothing reads is a typo, most
// often: a warning says so, and the platform's own variables are left alone.
func TestStrayVariablesAreWarned(t *testing.T) {
	c := newConfigured()
	t.Setenv("NEWS_SENDER", "ops@example.com")
	t.Setenv("NEWS_BASE_UR", "https://typo.example")
	t.Setenv("NEWS_DB_SERVICE_HOST", "10.0.0.1")
	t.Setenv("NEWS_DB_PORT_5432_TCP_ADDR", "10.0.0.1")
	app := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	var warned []string
	for _, d := range app.Graph().Diagnostics {
		if d.Severity == "warning" && strings.Contains(d.Message, "no setting or secret of the product reads it") {
			warned = append(warned, d.Message)
		}
	}
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "NEWS_BASE_UR is set") {
		t.Errorf("warnings %q", warned)
	}
}

// A workflow timer can wait a setting: each environment gives its own delay,
// and one that is not positive stops the start.
func TestATimerWaitsASetting(t *testing.T) {
	type doc struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	svc := kit.NewService("docs", "Documents that expire.")
	docs := svc.Store("docs", func(d doc) string { return d.ID })
	ttl := svc.Setting("ttl", time.Hour)
	flow := svc.Workflow("life", docs, func(d *doc) *string { return &d.State }).
		Initial("new").
		After("expire", ttl, "new", "expired")

	t.Setenv("PAPERS_TTL", "2m")
	clk := clock.NewManualClock(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC))
	app := kit.NewApp("papers", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard), kit.Clock(clk))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	if _, err := flow.Start(t.Context(), doc{ID: "d1"}); err != nil {
		t.Fatal(err)
	}
	var tr model.TransitionInfo
	for _, x := range app.Graph().Node("docs/workflow/life").Workflow.Transitions {
		if x.Event == "expire" {
			tr = x
		}
	}
	if tr.After != "2m0s" || tr.Setting != "ttl" {
		t.Errorf("the diagram's timer %+v", tr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, dErr := docs.Get(context.Background(), "d1")
		if dErr != nil {
			t.Fatal(dErr)
		}
		if d.State == "expired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %q after the setting's delay", d.State)
		}
		clk.Advance(10 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if clk.Now().Sub(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)) > 3*time.Minute {
		t.Errorf("expired after %s, not the 2m the environment gave", clk.Now().Sub(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)))
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PAPERS_TTL", "0s")
	app = kit.NewApp("papers", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	err := app.Start(t.Context())
	if err == nil {
		app.Stop(context.Background())
	}
	if err == nil || !strings.Contains(err.Error(), `setting "ttl" must be a positive duration, for the timer expire of workflow docs/workflow/life`) {
		t.Fatalf("Start = %v", err)
	}
}

// KIT_ENV names its file as it is written: local is dev, and reads
// config/local.yaml.
func TestAnAliasOfDevReadsItsOwnFile(t *testing.T) {
	c := newConfigured()
	t.Setenv("KIT_ENV", "local")
	app := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Studio(false), kit.Analyze(false), kit.Logs(io.Discard),
		kit.ConfigFiles(fstest.MapFS{"config/local.yaml": {Data: []byte("page-size: 3\nsender: me@example.com\n")}}))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	if got := c.limit.Get(); got != 3 {
		t.Errorf("page-size %d: config/local.yaml was not read", got)
	}
}

// A JSON number is a float64, exact only up to 2^53: a whole number past it
// is refused rather than rounded.
func TestAJSONIntegerTooLargeToBeExactIsRefused(t *testing.T) {
	svc := kit.NewService("big", "Large numbers.")
	svc.Setting("limit", int64(0))
	app := kit.NewApp("big", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
		kit.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"limit": 9007199254740993}`)}}))
	err := app.Start(t.Context())
	if err == nil {
		app.Stop(context.Background())
	}
	if err == nil || !strings.Contains(err.Error(), "config/config.json: limit is not a whole number") {
		t.Fatalf("Start = %v", err)
	}
}

// A text setting named like a secret is not shown, whatever it holds: it
// belongs in Service.Secret, but one put in a setting still does not leak.
// A number named so is shown: it cannot hold a credential.
func TestASettingNamedLikeASecretIsNotShown(t *testing.T) {
	svc := kit.NewService("probe", "Settings named like secrets.")
	svc.Setting("api-token", "")
	svc.Setting("session-hours", 12)
	t.Setenv("PROBE_API_TOKEN", "sk_live_abcdef123456")
	app := kit.NewApp("probe", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	if s := settingOf(t, app, "PROBE_API_TOKEN"); s.Value != "[redacted]" {
		t.Errorf("api-token shown as %q", s.Value)
	}
	if s := settingOf(t, app, "PROBE_SESSION_HOURS"); s.Value != "12" {
		t.Errorf("session-hours shown as %q", s.Value)
	}
}

// What a platform sets under the product's prefix is left alone; a typo is
// not, even one that ends like a platform's variable.
func TestPlatformVariablesAreNotStray(t *testing.T) {
	c := newConfigured()
	t.Setenv("NEWS_SENDER", "ops@example.com")
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"NEWS_DB_SERVICE_HOST", "10.0.0.1"},
		{"NEWS_DB_SERVICE_PORT_HTTP", "80"},
		{"NEWS_DB_PORT", "tcp://10.0.0.1:5432"},
		{"NEWS_DB_PORT_5432_TCP_ADDR", "10.0.0.1"},
		{"NEWS_DB_NAME", "/news/db"},
		{"NEWS_DB_ENV_POSTGRES_USER", "news"},
		{"NEWS_SMTP_PORT", "25"},
	} {
		name, value := tc.name, tc.value
		t.Setenv(name, value)
	}
	app := kit.NewApp("news", c.svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	var warned []string
	for _, d := range app.Graph().Diagnostics {
		if d.Severity == "warning" && strings.Contains(d.Message, "no setting or secret of the product reads it") {
			warned = append(warned, d.Message)
		}
	}
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "NEWS_SMTP_PORT is set") {
		t.Errorf("warnings %q", warned)
	}
}
