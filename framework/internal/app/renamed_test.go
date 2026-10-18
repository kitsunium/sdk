package app_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The runtime names of ADR 0180 §3: what a product shows an operator, a
// client and its graph carries no trace of the tool, and an old name met at
// the start is refused, naming the new one (Q12).

// renamedOf starts a and returns its refusal, which must carry CodeRenamed.
func renamedOf(t *testing.T, a *app.App) *app.DiagnosticsError {
	t.Helper()
	err := a.With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard)).Start(t.Context())
	if err == nil {
		_ = a.Stop(t.Context())
		t.Fatal("the start met an old name and went on")
	}
	var de *app.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want a DiagnosticsError", err)
	}
	if !errs.HasCode(err, app.CodeRenamed) {
		t.Errorf("the refusal carries no CodeRenamed: %v", err)
	}
	return de
}

// An operator's KIT_* variable is refused at the start, every one at once,
// each naming the variable that replaces it: ignored, KIT_DATA_DIR would
// start the product on an empty default directory.
func TestAnOldVariableIsRefusedNamingTheNewOne(t *testing.T) {
	t.Setenv("KIT_DATA_DIR", "/srv/shop")
	t.Setenv("KIT_SMTP_URL_FILE", "/run/secrets/smtp")
	t.Setenv("KIT_STUDIO", "off") // the tool's, read by the Studio subsystem alone
	de := renamedOf(t, app.NewApp("shop", Shop, Audit, Members))
	want := []string{"KIT_DATA_DIR -> SHOP_DATA_DIR", "KIT_SMTP_URL_FILE -> SHOP_SMTP_URL_FILE"}
	if !slices.Equal(de.Renamed, want) {
		t.Errorf("Renamed = %q, want %q", de.Renamed, want)
	}
	text := de.Error()
	for _, said := range []string{"KIT_DATA_DIR is set, but the framework no longer reads it: it reads SHOP_DATA_DIR", "it reads SHOP_SMTP_URL_FILE"} {
		if !strings.Contains(text, said) {
			t.Errorf("the refusal does not say %q:\n%s", said, text)
		}
	}
	if strings.Contains(text, "KIT_STUDIO") {
		t.Errorf("the tool's variable is not the operator's:\n%s", text)
	}
}

// Every command that reads the configuration refuses an old variable as the
// start does — the healthcheck included, which would probe a wrong address,
// with the configuration's exit status.
func TestTheCommandsRefuseAnOldVariable(t *testing.T) {
	t.Setenv("KIT_ADDR", "127.0.0.1:1")
	a := app.NewApp("shop", Shop, Audit, Members).With(app.InMemory(), app.Logs(io.Discard))
	for _, cmd := range [][]string{{"healthcheck"}, {"config"}, {"graph", "-static=false"}} {
		want := 1
		if cmd[0] == "healthcheck" {
			want = 78
		}
		if code := a.Main(t.Context(), cmd); code != want {
			t.Errorf("%s with KIT_ADDR set: exit %d, want %d", cmd[0], code, want)
		}
	}
}

// A product whose prefix is the old one reads those names as its own.
func TestAProductNamedLikeTheOldPrefixReadsItsOwn(t *testing.T) {
	t.Setenv("KIT_TRUST_PROXY", "on")
	a := app.NewApp("kit", Shop, Audit, Members).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer a.Stop(t.Context())
	for _, s := range a.Graph().Runtime.Config {
		if s.Name == "KIT_TRUST_PROXY" && (s.Value != "on" || s.From != model.SettingEnv) {
			t.Errorf("its own variable: %+v", s)
		}
	}
}

// Tagged is a store's entity whose fields still carry the framework's
// former tag key, one of them nested.
type Tagged struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"personal"`
	Inner Inner  `json:"inner"`
}

// Inner is nested in Tagged.
type Inner struct {
	Token string `json:"token" kit:"secret"`
}

// A field still tagged kit:"…" is refused at the start, every field at
// once, naming the key that replaces it: a personal field whose tag is no
// longer read would be stored in clear.
func TestAFieldStillTaggedKitIsRefused(t *testing.T) {
	svc := app.NewService("tagged", "Fields under the former tag key.")
	svc.Store("records", func(r Tagged) string { return r.ID })
	de := renamedOf(t, app.NewApp("tags", svc))
	want := []string{
		`app_test.Inner.Token kit:"secret" -> app:"secret"`,
		`app_test.Tagged.Email kit:"personal" -> app:"personal"`,
	}
	if !slices.Equal(de.Renamed, want) {
		t.Errorf("Renamed = %q, want %q", de.Renamed, want)
	}
	if text := de.Error(); !strings.Contains(text, `app_test.Tagged.Email is tagged kit:"personal", which the framework no longer reads: tag it app:"personal"`) {
		t.Errorf("the refusal does not name the new key:\n%s", text)
	}
}

// The probes answer under /_app/health/; their former paths under
// /_kit/health/ answer the same, and the first request of the run to each
// is logged as deprecated, naming the path to probe. The rest of /_kit/
// stays reserved.
func TestTheFormerProbePathsAnswerLoggedAsDeprecated(t *testing.T) {
	var logs syncBuffer
	a := start(t, app.Env(app.EnvProduction), app.Logs(&logs))
	for _, probe := range []string{"live", "ready", "startup"} {
		if r := call(t, a, "GET /_app/health/"+probe, noBody); r.status != http.StatusOK {
			t.Errorf("probe %s: %d %s", probe, r.status, r.body)
		}
		for range 2 {
			if r := call(t, a, "GET /_kit/health/"+probe, noBody); r.status != http.StatusOK {
				t.Errorf("former probe %s: %d %s", probe, r.status, r.body)
			}
		}
	}
	said := logs.String()
	if n := strings.Count(said, "a probe asked a deprecated path"); n != 3 {
		t.Errorf("%d deprecation lines, want one per former path:\n%s", n, said)
	}
	if !strings.Contains(said, `"path":"/_kit/health/ready"`) || !strings.Contains(said, `"use":"/_app/health/ready"`) {
		t.Errorf("the deprecation names neither the path asked nor the one to use:\n%s", said)
	}
	for _, path := range []string{"/_kit/", "/_kit/api/graph", "/_app/anything"} {
		if r := call(t, a, "GET "+path, noBody); r.status != http.StatusNotFound {
			t.Errorf("%s is not reserved: %d", path, r.status)
		}
	}
}

// The graph and the probes say the framework's name: "app" as a loop's
// library, the build's and the graph's field, the probes' checks.
func TestTheGraphSaysTheFrameworksName(t *testing.T) {
	a := start(t)
	raw, err := json.Marshal(a.Graph())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		App map[string]any `json:"app"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, old := doc.App["kit"]; old {
		t.Errorf("the graph still carries a field kit: %v", doc.App)
	}
	if b, ok := doc.App["build"].(map[string]any); ok {
		if _, has := b["app"]; !has {
			t.Errorf("the build says no framework: %v", b)
		}
	}
	r := call(t, a, "GET /_app/health/ready", noBody)
	if !strings.Contains(string(r.body), `"app"`) || strings.Contains(string(r.body), `"kit"`) {
		t.Errorf("the readiness detail names no check app: %s", r.body)
	}
	for _, s := range a.Graph().Runtime.Config {
		if strings.HasPrefix(s.Name, "KIT_") && !strings.HasPrefix(s.Name, "KIT_STUDIO") && s.Name != "KIT_ANALYZE" {
			t.Errorf("a setting still named after the tool: %+v", s)
		}
	}
}

// A setting or a secret of the product read from one of the framework's
// own variables is refused: the two would share it.
func TestAProductVariableTheFrameworkReadsIsRefused(t *testing.T) {
	svc := app.NewService("taken", "Names the framework reads.")
	svc.Secret("smtp-url")
	svc.Setting("log-level", "info")
	err := app.NewApp("shop", svc).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard)).Start(t.Context())
	if err == nil {
		t.Fatal("a product variable the framework reads was accepted")
	}
	for _, said := range []string{
		`secret or database URL "smtp-url" would be read from SHOP_SMTP_URL, a variable of the framework's own`,
		`setting "log-level" would be read from SHOP_LOG_LEVEL, a variable of the framework's own`,
	} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("the refusal does not say %q:\n%v", said, err)
		}
	}
}
