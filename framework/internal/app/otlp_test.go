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

// drivenClock is a manual clock that reports every wait armed on it — a
// timer made or reset, a ticker, an After —, so a case advances it exactly
// when something waits, as otlpengine's harness does.
type drivenClock struct {
	*clock.ManualClock
	armed chan time.Duration
}

// newDrivenClock is a drivenClock at a fixed instant.
func newDrivenClock() *drivenClock {
	return &drivenClock{ManualClock: clock.NewManualClock(time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)), armed: make(chan time.Duration, 1<<16)}
}

// report says a wait of d was armed.
func (c *drivenClock) report(d time.Duration) {
	select {
	case c.armed <- d:
	default:
	}
}

// NewTimer arms a timer and reports it; its Reset reports too.
func (c *drivenClock) NewTimer(d time.Duration) clock.Timer {
	timer := &drivenTimer{Timer: c.ManualClock.NewTimer(d), c: c}
	c.report(d)
	return timer
}

// NewTicker arms a ticker and reports its period.
func (c *drivenClock) NewTicker(d time.Duration) clock.Ticker {
	ticker := c.ManualClock.NewTicker(d)
	c.report(d)
	return ticker
}

// After arms a wait and reports it.
func (c *drivenClock) After(d time.Duration) <-chan time.Time {
	ch := c.ManualClock.After(d)
	c.report(d)
	return ch
}

// drivenTimer is a timer whose Reset is reported.
type drivenTimer struct {
	clock.Timer
	c *drivenClock
}

// Reset re-arms the timer and reports it.
func (t *drivenTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.c.report(d)
	return active
}

// drive advances the clock past each wait of one second to ten minutes as
// it is armed, until done holds, failing once twenty seconds passed on the
// wall clock, which it never sleeps on. A wait below a second — a consumer's
// idle poll — is never advanced: a batch queued in the process wakes its
// consumer at once. Nor is an attempt's bound — and with it the batch sink's
// hand-over of a batch to its queue —, ten hours in these cases, which no
// advance reaches however far the recurring waits move the clock.
func (c *drivenClock) drive(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached", what)
		}
		select {
		case d := <-c.armed:
			if d >= time.Second && d < 10*time.Minute {
				c.Advance(d)
			}
		default:
			runtime.Gosched()
		}
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
	clk := newDrivenClock()
	a, err := startPolicies(t, []*app.Service{pingService()}, app.Clock(clk),
		app.OTLP(app.OTLPEndpoint(srv.URL), app.OTLPSendTimeout(10*time.Hour)),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	clk.drive(t, "the span and the metrics sent", func() bool {
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
	clk := newDrivenClock()
	var mu sync.Mutex
	var signals []string
	send := func(_ context.Context, b app.OTLPBatchValue) error {
		mu.Lock()
		signals = append(signals, b.Signal)
		mu.Unlock()
		return app.NewError(http.StatusBadRequest, "invalid", "refused")
	}
	a, err := startPolicies(t, []*app.Service{pingService()}, app.Clock(clk),
		app.OTLP(app.OTLPSender(send), app.OTLPSendTimeout(10*time.Hour)),
		// No snapshot of the metrics within the case: the one batch lost is
		// the span's.
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 100*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	clk.drive(t, "the batch refused", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(signals) > 0
	})
	clk.drive(t, "the readiness degraded", func() bool {
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

// With a data directory, each signal's queue lies under <data>/.otlp. A
// platform whose data directory the SDK refuses (ADR 0018, ADR 0144) refuses
// the app's start before any export: needsFileStore asserts that refusal,
// then skips — otlpengine's suite holds the export's memory fallback there.
func TestTheOTLPQueuesLieInTheDataDirectory(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	if _, err := startPolicies(t, []*app.Service{pingService()}, app.DataDir(dir), app.OTLP(app.OTLPEndpoint("http://127.0.0.1:1"))); err != nil {
		t.Fatal(err)
	}
	for _, signal := range []string{"traces", "metrics"} {
		if info, err := os.Stat(filepath.Join(dir, ".otlp", signal)); err != nil || !info.IsDir() {
			t.Errorf("no queue directory for %s: %v", signal, err)
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
