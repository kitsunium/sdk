package app_test

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
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
	{"otlp-sample-ratio", "SHOP_OTLP_SAMPLE_RATIO", "number", "0.5", "0.25"},
}

// otlpOptions are the code's values otlpKnobs say, the endpoint left to the
// setting — the export is then off, and the settings alone are judged.
func otlpOptions() app.AppConfigurer {
	return app.OTLP(app.OTLPBounds(50, 1<<20), app.OTLPWhenFull(app.OTLPBlock), app.OTLPMaxAge(2*time.Hour), app.OTLPSendTimeout(3*time.Second),
		app.OTLPSampleRatio(0.5))
}

// The export's own defaults, when the code gives no value: every trace kept
// (otlp-sample-ratio 1).
func TestTheOTLPSettingsHaveTheirDefaults(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{pingService()}, app.OTLP())
	if err != nil {
		t.Fatal(err)
	}
	for variable, want := range map[string]string{
		"SHOP_OTLP_SAMPLE_RATIO": "1", "SHOP_OTLP_MAX_BATCHES": "10000", "SHOP_OTLP_WHEN_FULL": "drop-oldest",
	} {
		if s := settingOf(t, a, variable); s.From != model.SettingDefault || s.Value != want {
			t.Errorf("%s: %+v, want %q by default", variable, s, want)
		}
	}
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
		if k.kind != "int" && k.kind != "number" {
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
		{"otlp-sample-rate", []app.AppConfigurer{otlpOptions()}},
		{"otlp-sample-ratio", nil},
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
		{"otlp-sample-ratio", "-0.1"},
		{"otlp-sample-ratio", "1.5"},
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
	for _, ratio := range []float64{-1, 2, math.NaN()} {
		if _, err := startPolicies(t, []*app.Service{pingService()}, app.OTLP(app.OTLPSampleRatio(ratio))); err == nil || !strings.Contains(err.Error(), `setting "otlp-sample-ratio"`) {
			t.Errorf("a ratio of %v in the code: %v", ratio, err)
		}
	}
	if _, err := startPolicies(t, []*app.Service{pingService()}, otlpOptions(),
		app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"otlp-sample-ratio": "half"}`)}})); err == nil || !strings.Contains(err.Error(), "otlp-sample-ratio is not a number") {
		t.Errorf("a ratio that is not a number: %v", err)
	}
	t.Setenv("SHOP_OTLP_SAMPLE_RATIO", "1.01")
	if _, err := startPolicies(t, []*app.Service{pingService()}, otlpOptions()); err == nil || !strings.Contains(err.Error(), `setting "otlp-sample-ratio"`) {
		t.Errorf("a ratio of 1.01 in the environment: %v", err)
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
// received, by path, each answer delay after the request on the wall clock.
type collected struct {
	mu    sync.Mutex
	posts map[string][]string
	delay time.Duration
}

// serve keeps the request's body under its path.
func (c *collected) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	// A slow round trip, as a loaded runner's loopback can be: the wall
	// time the export waits on, which no driven clock may turn into hours.
	time.Sleep(c.delay)
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
	armed chan armedWait
	// horizon is the instant drive never advances the clock past: its
	// start plus driveHorizon.
	horizon time.Time
	// mu guards held.
	mu sync.Mutex
	// held maps each timer whose last arming a drive took for an echo, and
	// did not advance, to the instant that arming fires; a timer leaves it
	// when it is armed again or stopped.
	held map[*drivenTimer]time.Time
}

// driveHorizon is how far drive ever moves a drivenClock from its start.
// The batch sink re-arms its five-second delay each time it fires, so a
// drive that advanced every wait without a horizon moved the clock five
// seconds per turn of its loop for as long as a send took on the wall clock
// — thousands of turns a second —, past an attempt's bound (10h here) and a
// batch's age (24h) within one slow loopback round trip: the batches were
// counted expired and the readiness said degraded, as a Windows runner
// showed (issue #598). drive no longer advances that echo (issue #1408); the
// horizon stays the bound of what it may advance. An hour holds every wait a
// case needs — the sink's delay, the metrics' interval, a hundred backoffs
// of the default curve — and stays far below every bound a case does not
// test.
const driveHorizon = time.Hour

// armedWait is one wait armed on a drivenClock: the timer that armed it —
// nil for a ticker or an After —, its duration, and the instant it fires.
type armedWait struct {
	timer *drivenTimer
	d     time.Duration
	at    time.Time
}

// newDrivenClock is a drivenClock at a fixed instant.
func newDrivenClock() *drivenClock {
	start := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	return &drivenClock{
		ManualClock: clock.NewManualClock(start), armed: make(chan armedWait, 1<<16), horizon: start.Add(driveHorizon),
		held: map[*drivenTimer]time.Time{},
	}
}

// report says timer — nil for a ticker or an After — armed a wait of d.
func (c *drivenClock) report(timer *drivenTimer, d time.Duration) {
	select {
	case c.armed <- armedWait{timer: timer, d: d, at: c.Now().Add(d)}:
	default:
	}
}

// NewTimer arms a timer and reports it; its Reset reports too.
func (c *drivenClock) NewTimer(d time.Duration) clock.Timer {
	timer := &drivenTimer{Timer: c.ManualClock.NewTimer(d), c: c}
	c.report(timer, d)
	return timer
}

// NewTicker arms a ticker and reports its period.
func (c *drivenClock) NewTicker(d time.Duration) clock.Ticker {
	ticker := c.ManualClock.NewTicker(d)
	c.report(nil, d)
	return ticker
}

// After arms a wait and reports it.
func (c *drivenClock) After(d time.Duration) <-chan time.Time {
	ch := c.ManualClock.After(d)
	c.report(nil, d)
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
	t.c.report(t, d)
	return active
}

// Stop disarms the timer: no drive advances a wait of it any more.
func (t *drivenTimer) Stop() bool {
	t.c.mu.Lock()
	delete(t.c.held, t)
	t.c.mu.Unlock()
	return t.Timer.Stop()
}

// drive advances the clock past each wait of one second to ten minutes as
// it is armed, until done holds, failing once twenty seconds passed on the
// wall clock, which it never sleeps on. A wait below a second — a consumer's
// idle poll — is never advanced: a batch queued in the process wakes its
// consumer at once. Nor is a wait that would take the clock past its
// horizon (driveHorizon): past it drive only waits for done, so an attempt's
// bound — and with it the batch sink's hand-over of a batch to its queue —,
// ten hours in these cases, and a batch's age are never reached, however
// long a send takes on the wall clock.
//
// Nor is a timer re-armed with no other wait armed since its last arming:
// the batch sink re-arms its delay each time it fires, so advancing that
// echo fired it again, at once, and drive moved the clock five seconds per
// turn of its loop while a send took its time on the wall clock — toward the
// horizon, past which the forwarder's next backoff is never advanced (issue
// #1408). The clock moves when the work arms a wait — a send's bound, a
// backoff, a ticker —, not for the sink's loop alone. A retry refused at
// once is work all the same: a case whose sender refuses a batch it does not
// look at moves the clock a backoff per turn, and keeps its sender to the
// batches it reads.
//
// An echo is held, not forgotten: a drive starts by moving the clock to the
// latest instant a held echo still fires at. The sink's echo is reported
// once, when it is armed; a drive that read it as an echo before it ended —
// the export's hand-over returned before the case's condition held, as on
// OpenBSD — left the sink waiting on it, and a span the case queued next sat
// there with no wait reported to move the clock
// (TestAnOTLPSwitchToTheDatabaseWarnsThenMigrates). A case queues its work
// before it drives, so that work stands on the held echo; moved once a
// drive, the clock moves a delay per drive, never per turn.
func (c *drivenClock) drive(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	c.advanceHeld()
	var last *drivenTimer
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached (the clock at %s, its horizon at %s)", what, c.Now().Format(time.TimeOnly), c.horizon.Format(time.TimeOnly))
		}
		select {
		case w := <-c.armed:
			echo := w.timer != nil && w.timer == last
			last = w.timer
			c.mu.Lock()
			delete(c.held, w.timer)
			if echo && c.advances(w.d) {
				c.held[w.timer] = w.at
			}
			c.mu.Unlock()
			if !echo && c.advances(w.d) {
				c.Advance(w.d)
			}
		default:
			runtime.Gosched()
		}
	}
}

// advances reports whether drive moves the clock past a wait of d armed now:
// one of a second to ten minutes that ends short of the horizon.
func (c *drivenClock) advances(d time.Duration) bool {
	return d >= time.Second && d < 10*time.Minute && !c.Now().Add(d).After(c.horizon)
}

// advanceHeld moves the clock to the latest instant an echo held by a past
// drive still fires at, short of the horizon, and forgets every held echo.
func (c *drivenClock) advanceHeld() {
	c.mu.Lock()
	var latest time.Time
	for _, at := range c.held {
		if at.After(latest) {
			latest = at
		}
	}
	clear(c.held)
	c.mu.Unlock()
	if now := c.Now(); latest.After(now) && !latest.After(c.horizon) {
		c.Advance(latest.Sub(now))
	}
}

// sinkLoop runs the batch sink's loop as drive sees it, until the case
// ends: one timer of five seconds, re-armed each time it fires, and an
// export — its bound of ten hours armed, then stopped — for each span queued
// since the last one. It returns the spans queued, the spans exported and
// the times the timer was re-armed.
func sinkLoop(t *testing.T, clk *drivenClock) (queued, exported, rearmed *atomic.Int64) {
	t.Helper()
	queued, exported, rearmed = &atomic.Int64{}, &atomic.Int64{}, &atomic.Int64{}
	timer := clk.NewTimer(5 * time.Second)
	stop, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		for {
			select {
			case <-stop:
				return
			case <-timer.C():
			}
			for exported.Load() < queued.Load() {
				bound := clk.NewTimer(10 * time.Hour)
				exported.Add(1)
				bound.Stop()
			}
			timer.Reset(5 * time.Second)
			rearmed.Add(1)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-ended
		timer.Stop()
	})
	return queued, exported, rearmed
}

// A drive that read the sink's echo before its condition held — the order
// OpenBSD's slower hand-over gave TestAnOTLPSwitchToTheDatabaseWarnsThenMigrates
// — leaves the next span on that echo, which no wait reported since can
// move: the next drive moves the clock to it, once, and the span is
// exported. The first drive ends only once the sink re-armed after an empty
// turn and drive read that echo, so the order is forced on every platform.
func TestDriveAdvancesTheEchoItHeldBack(t *testing.T) {
	clk := newDrivenClock()
	queued, exported, rearmed := sinkLoop(t, clk)
	queued.Add(1)
	clk.drive(t, "the first span exported, the sink's echo read", func() bool {
		return exported.Load() == 1 && rearmed.Load() >= 2 && len(clk.armed) == 0
	})
	at := clk.Now()
	queued.Add(1)
	clk.drive(t, "the second span exported", func() bool { return exported.Load() == 2 })
	if moved := clk.Now().Sub(at); moved > 10*time.Second {
		t.Errorf("the clock moved %v for one span, want a delay or two", moved)
	}
}

// A request's span reaches the collector on the trace signal's path, and
// the framework's metrics of it on the metrics', each an OTLP/JSON request
// naming the product: the export runs as a component of the app, on its
// clock, and a CLI command exports nothing. A collector that answers slowly
// loses nothing either: while the export waits on the wall clock, the
// app's clock stays below every bound (issue #598).
func TestAnAppsSpansAndMetricsReachTheCollector(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
	}{{"at once", 0}, {"slowly", 200 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			col := &collected{posts: map[string][]string{}, delay: tc.delay}
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
		})
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

// sent is the product's own sender of a case: it keeps every batch it is
// handed, by signal, and accepts it.
type sent struct {
	mu      sync.Mutex
	batches map[string][]string
}

// newSent is a sender that has kept nothing yet.
func newSent() *sent { return &sent{batches: map[string][]string{}} }

// send keeps the batch.
func (s *sent) send(_ context.Context, b app.OTLPBatchValue) error {
	s.mu.Lock()
	s.batches[b.Signal] = append(s.batches[b.Signal], string(b.Document))
	s.mu.Unlock()
	return nil
}

// count is how many batches of signal were kept, and of them how many hold
// every one of words.
func (s *sent) count(signal string, words ...string) (all, holding int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, body := range s.batches[signal] {
		all++
		if containsAll(body, words) {
			holding++
		}
	}
	return all, holding
}

// containsAll reports whether body holds every one of words.
func containsAll(body string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(body, w) {
			return false
		}
	}
	return true
}

// startSampled starts the ping service with an export to send, the
// metrics collected every seven seconds of its clock.
func startSampled(t *testing.T, clk *drivenClock, send *sent, opts ...app.AppConfigurer) *app.App {
	t.Helper()
	a, err := startPolicies(t, []*app.Service{pingService()}, append([]app.AppConfigurer{
		app.Clock(clk), app.OTLP(app.OTLPSender(send.send), app.OTLPSendTimeout(10*time.Hour)),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 7*time.Second),
	}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// otlp-sample-ratio decides which of the traces the app starts the export
// keeps: at 1, the default, every one reaches the sender; at 0 none does —
// while the framework's metrics still count every request, kept or not. The
// case at 1 is the control of the case at 0: the same requests, the same
// two collections waited for.
func TestTheSampleRatioDecidesWhichTracesAreKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ratio  string
		traces bool
	}{{"every trace at 1", "1", true}, {"none at 0", "0", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHOP_OTLP_SAMPLE_RATIO", tc.ratio)
			clk, send := newDrivenClock(), newSent()
			a := startSampled(t, clk, send)
			for range 8 {
				expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
			}
			clk.drive(t, "two collections counting the eight requests", func() bool {
				_, counted := send.count("metrics", "app.node.runs", "orders/endpoint/ping", `"asInt":"8"`)
				traces, _ := send.count("traces")
				return counted >= 2 && (!tc.traces || traces > 0)
			})
			if traces, _ := send.count("traces"); (traces > 0) != tc.traces {
				t.Errorf("at a ratio of %s, %d batches of traces were sent", tc.ratio, traces)
			}
		})
	}
}

// A trace another process started and kept is kept here too, whatever the
// ratio: at 0, a request whose traceparent says sampled reaches the sender
// under that trace's identifier.
func TestAParentsSampledTraceIsKeptAtAnyRatio(t *testing.T) {
	clk, send := newDrivenClock(), newSent()
	a := startSampled(t, clk, send, app.Set("otlp-sample-ratio", 0.0))
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody, "traceparent", "00-"+traceID+"-b7ad6b7169203331-01"),
		http.StatusNoContent, "")
	clk.drive(t, "the parent's trace sent", func() bool {
		_, holding := send.count("traces", traceID)
		return holding > 0
	})
}

// The product's own meter lands in the export's metrics stream: a counter a
// handler increments reaches the sender in a batch of its own, under the
// app's resource and a scope named after the app, apart from the
// framework's instruments.
func TestTheProductsMeterReachesTheExport(t *testing.T) {
	clk, send := newDrivenClock(), newSent()
	s := app.NewService("orders", "Answers a ping.")
	s.Endpoint("GET /orders/ping", func(ctx context.Context, _ app.EmptyValue) (app.EmptyValue, error) {
		app.Meter(ctx).Counter("orders.pinged", metrics.String("channel", "web")).Inc()
		return app.EmptyValue{}, nil
	}, app.Name("ping"))
	a, err := startPolicies(t, []*app.Service{s}, app.Clock(clk),
		app.OTLP(app.OTLPSender(send.send), app.OTLPSendTimeout(10*time.Hour)),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	}
	clk.drive(t, "the product's counter sent", func() bool {
		_, holding := send.count("metrics", "orders.pinged", `"asInt":"3"`, `"channel"`, `"service.name"`, `"scope":{"name":"shop"}`)
		return holding > 0
	})
	if _, mixed := send.count("metrics", "orders.pinged", "app.node.runs"); mixed > 0 {
		t.Error("the product's instruments and the framework's share a batch, and so a scope")
	}
}
