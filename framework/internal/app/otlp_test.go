package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// otlpKnobs are the settings app.OTLP declares for the app, with the value
// otlpOptions gives in code and another a configuration gives.
var otlpKnobs = []policyKnob{
	{"otlp-endpoint", "SHOP_OTLP_ENDPOINT", "text", "", "http://collector:4318"},
	{"otlp-max-batches", "SHOP_OTLP_MAX_BATCHES", "int", "50", "60"},
	{"otlp-max-bytes", "SHOP_OTLP_MAX_BYTES", "int", "1048576", "2097152"},
	{"otlp-when-full", "SHOP_OTLP_WHEN_FULL", "text", "block", "drop-newest"},
	{"otlp-max-age", "SHOP_OTLP_MAX_AGE", "duration", "2h0m0s", "3h0m0s"},
	{"otlp-send-timeout", "SHOP_OTLP_SEND_TIMEOUT", "duration", "3s", "4s"},
	{"otlp-interval", "SHOP_OTLP_INTERVAL", "duration", "1m0s", "2m0s"},
	{"otlp-drain", "SHOP_OTLP_DRAIN", "duration", "5s", "0s"},
	{"otlp-settle", "SHOP_OTLP_SETTLE", "duration", "5s", "6s"},
}

// otlpOptions are the code's values otlpKnobs say, the endpoint left to the
// setting — the export is then off, and the settings alone are judged.
func otlpOptions() app.AppConfigurer {
	return app.OTLP(app.OTLPBounds(50, 1<<20), app.OTLPWhenFull(app.OTLPBlock), app.OTLPMaxAge(2*time.Hour), app.OTLPSendTimeout(3*time.Second))
}

// pingService is a service with one endpoint, whose run is a span the
// export forwards.
func pingService() *app.Service {
	s := app.NewService("orders", "Answers a ping.")
	s.Endpoint("GET /orders/ping", func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil },
		app.Name("ping"))
	return s
}

// The export's settings are the framework's, declared for the app: the
// code's values their defaults, said by the configuration as coming from
// the code, under no service.
func TestTheOTLPSettingsDefaultToTheCode(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{pingService()}, otlpOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range otlpKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			if s.Key != k.key || s.Service != "" || s.Type != k.kind || s.From != model.SettingDefault || s.Value != k.def {
				t.Errorf("%+v, want the key %s, a %s from the code: %q", s, k.key, k.kind, k.def)
			}
		})
	}
	if s := settingOf(t, a, "SHOP_OTLP_HEADERS"); !s.Secret || s.From != model.SettingDefault || s.Value != "" {
		t.Errorf("the headers' secret is shown as %+v", s)
	}
}

// A configuration file overrides each of the export's settings, and the
// variable overrides the file: the configuration says where each value came
// from.
func TestTheOTLPSettingsAreOverriddenByTheConfiguration(t *testing.T) {
	var file strings.Builder
	file.WriteString("{")
	for i, k := range otlpKnobs {
		if i > 0 {
			file.WriteString(",")
		}
		value := k.other
		if k.kind != "int" {
			value = `"` + value + `"`
		}
		file.WriteString(`"` + k.key + `":` + value)
	}
	file.WriteString("}")
	t.Setenv("SHOP_OTLP_MAX_AGE", "90m")
	// The endpoint names no collector that answers: the export opens, and
	// its first batch would wait — no span ends in this case.
	a, err := startPolicies(t, []*app.Service{pingService()}, otlpOptions(),
		app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(file.String())}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range otlpKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			want := model.Setting{From: model.SettingFile, Detail: "config/dev.json", Value: k.other}
			if k.variable == "SHOP_OTLP_MAX_AGE" {
				want = model.Setting{From: model.SettingEnv, Value: "1h30m0s"}
			}
			if s.From != want.From || s.Detail != want.Detail || s.Value != want.Value {
				t.Errorf("%+v, want %+v", s, want)
			}
		})
	}
}

// A key the export does not declare is refused, as any key no service
// declares — misspelled, or the export's in an app that declares none.
func TestAKeyTheOTLPExportDoesNotDeclareIsRefused(t *testing.T) {
	for _, tc := range []struct {
		key  string
		opts []app.AppConfigurer
	}{
		{"otlp-endpoints", []app.AppConfigurer{otlpOptions()}},
		{"otlp-max-batch", []app.AppConfigurer{otlpOptions()}},
		{"otlp-endpoint", nil},
	} {
		t.Run(tc.key, func(t *testing.T) {
			opts := append(tc.opts, app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": "x"}`)}}))
			_, err := startPolicies(t, []*app.Service{pingService()}, opts...)
			if err == nil || !strings.Contains(err.Error(), `config/config.json sets "`+tc.key+`", which no service declares`) {
				t.Errorf("%s: %v", tc.key, err)
			}
		})
	}
}

// A value the export cannot run with stops the start, from the
// configuration and from the code alike.
func TestAnOTLPValueItCannotRunWithStopsTheStart(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"otlp-endpoint", `"ftp://collector:4318"`},
		{"otlp-endpoint", `"http://user:password@collector:4318"`},
		{"otlp-endpoint", `"collector:4318"`},
		{"otlp-endpoint", `"http://collector:4318/?tenant=a"`},
		{"otlp-max-batches", "0"},
		{"otlp-max-bytes", "0"},
		{"otlp-when-full", `"drop"`},
		{"otlp-max-age", `"0s"`},
		{"otlp-send-timeout", `"-1s"`},
		{"otlp-interval", `"0s"`},
		{"otlp-drain", `"-1s"`},
		{"otlp-settle", `"0s"`},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{pingService()}, otlpOptions(),
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": ` + tc.value + `}`)}}))
			if err == nil || !strings.Contains(err.Error(), `setting "`+tc.key+`"`) {
				t.Errorf("%s = %s: %v", tc.key, tc.value, err)
			}
		})
	}
	if _, err := startPolicies(t, []*app.Service{pingService()}, app.OTLP(app.OTLPWhenFull("spill"))); err == nil || !strings.Contains(err.Error(), `setting "otlp-when-full"`) {
		t.Errorf("an unknown policy in the code: %v", err)
	}
}

// A setting of the export a service of the product declares already is
// refused, naming it: the two would share a variable.
func TestAnOTLPSettingAProductDeclaresIsRefused(t *testing.T) {
	s := pingService()
	s.Setting("otlp-endpoint", "")
	_, err := startPolicies(t, []*app.Service{s}, otlpOptions())
	if err == nil || !strings.Contains(err.Error(), "the OTLP export's setting otlp-endpoint is a setting a service of the product declares already") {
		t.Errorf("a product's otlp-endpoint: %v", err)
	}
}

// collected is an OTLP/HTTP collector that answers 200 and keeps what it
// received, by path.
type collected struct {
	mu    sync.Mutex
	posts map[string][]string
}

// serve keeps the request's body under its path.
func (c *collected) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.posts[r.URL.Path] = append(c.posts[r.URL.Path], string(body))
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, "{}")
}

// holds reports whether a request on path carries every one of words.
func (c *collected) holds(path string, words ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.posts[path] {
		all := true
		for _, w := range words {
			all = all && strings.Contains(body, w)
		}
		if all {
			return true
		}
	}
	return false
}

// advanceUntil moves clk a tenth of a second at a time, yielding between
// steps, until done holds — at most ten minutes of the clock, and
// waitBudget on the wall clock, which it never sleeps on.
func advanceUntil(t *testing.T, clk *clock.ManualClock, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for step := 0; !done(); step++ {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached", what)
		}
		if step < 6000 {
			clk.Advance(100 * time.Millisecond)
		}
		runtime.Gosched()
	}
}

// A request's span reaches the collector on the trace signal's path, and
// the framework's metrics of it on the metrics', each an OTLP/JSON request
// naming the product: the export runs as a component of the app, on its
// clock, and a CLI command exports nothing.
func TestAnAppsSpansAndMetricsReachTheCollector(t *testing.T) {
	col := &collected{posts: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(col.serve))
	t.Cleanup(srv.Close)
	clk := clock.NewManualClock(time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC))
	a, err := startPolicies(t, []*app.Service{pingService()}, app.Clock(clk),
		app.OTLP(app.OTLPEndpoint(srv.URL), app.OTLPSendTimeout(10*time.Hour)), app.Set("otlp-drain", time.Duration(0)))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	advanceUntil(t, clk, "the span and the metrics sent", func() bool {
		return col.holds("/v1/traces", `"shop"`, "ping") && col.holds("/v1/metrics", "app.node.runs", "orders/endpoint/ping")
	})
	if r := readiness(t, a); !strings.Contains(string(r.body), `"name":"otlp","status":"healthy"`) {
		t.Errorf("the readiness: %s", r.body)
	}
	if c := componentNamed(a, "otlp"); c == nil || c.State != model.ComponentUp {
		t.Errorf("the export's component: %+v", c)
	}
}

// A batch lost — here dead-lettered by the product's own sender, which
// refuses it for good — degrades the readiness without taking the product
// out of rotation, once: the next answer is healthy again while nothing
// more is lost.
func TestALostBatchDegradesTheReadinessOnce(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC))
	var mu sync.Mutex
	var signals []string
	send := func(_ context.Context, b app.OTLPBatchValue) error {
		mu.Lock()
		signals = append(signals, b.Signal)
		mu.Unlock()
		return app.NewError(http.StatusBadRequest, "invalid", "refused")
	}
	a, err := startPolicies(t, []*app.Service{pingService()}, app.Clock(clk),
		app.OTLP(app.OTLPSender(send), app.OTLPSendTimeout(10*time.Hour)), app.Set("otlp-drain", time.Duration(0)))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	advanceUntil(t, clk, "the batch refused", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(signals) > 0
	})
	advanceUntil(t, clk, "the readiness degraded", func() bool {
		r := readiness(t, a)
		if r.status != http.StatusOK {
			t.Fatalf("a lost batch took the product out of rotation: %d %s", r.status, r.body)
		}
		return strings.Contains(string(r.body), `"name":"otlp","status":"degraded"`)
	})
	if r := readiness(t, a); r.status != http.StatusOK || !strings.Contains(string(r.body), `"name":"otlp","status":"healthy"`) {
		t.Errorf("after the degraded answer, nothing more lost, the readiness says %d %s", r.status, r.body)
	}
}

// With a data directory, each signal's queue lies under <data>/.otlp —
// durable where the SDK's durable queue runs; where it refuses the
// platform, the export keeps it in memory and the start says so.
func TestTheOTLPQueuesLieInTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	a, err := startPolicies(t, []*app.Service{pingService()}, app.DataDir(dir), app.OTLP(app.OTLPEndpoint("http://127.0.0.1:1")))
	if err != nil {
		if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
			t.Skipf("the data directory is refused on %s by design: %v", runtime.GOOS, err)
		}
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		for _, signal := range []string{"traces", "metrics"} {
			if info, err := os.Stat(filepath.Join(dir, ".otlp", signal)); err != nil || !info.IsDir() {
				t.Errorf("no queue directory for %s: %v", signal, err)
			}
		}
	default:
		if !problemSays(a, "the OTLP export keeps its traces in memory") {
			t.Errorf("the memory fallback is not said: %+v", a.Graph().Diagnostics)
		}
	}
}

// componentNamed is the app's lifecycle component named name, nil when
// there is none.
func componentNamed(a *app.App, name string) *model.Component {
	for _, c := range a.Graph().Runtime.Components {
		if c.Name == name {
			return &c
		}
	}
	return nil
}

// problemSays reports whether one of the app's problems says text.
func problemSays(a *app.App, text string) bool {
	for _, p := range a.Graph().Diagnostics {
		if strings.Contains(p.Message, text) {
			return true
		}
	}
	return false
}
