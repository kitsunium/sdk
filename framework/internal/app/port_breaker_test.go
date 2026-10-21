package app_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// degraded is what the readiness says of the quote port's breaker while it
// is not closed: a check that fails without taking the product out of
// rotation.
const degraded = `"name":"breaker:orders/port/quote","status":"degraded"`

// readiness is the product's readiness answer, its checks detailed — in dev.
func readiness(t *testing.T, a *app.App) response {
	t.Helper()
	return call(t, a, "GET /_app/health/ready", noBody)
}

// A port's breaker opens after its failures in a row, refuses at once while
// it is open — the dependency not reached —, degrades the product's
// readiness without taking it out of rotation, says so in the logs, and
// closes on a trial once its open time passed on the app's clock.
func TestAPortsBreakerOpensAndCloses(t *testing.T) {
	var logs syncBuffer
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	dep := newDependency(app.Unavailable("down"), app.Unavailable("down"))
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Breaker(2, 10*time.Second))}, app.Clock(clk), app.Logs(&logs))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusServiceUnavailable, app.WireUnavailable)
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusServiceUnavailable, app.WireUnavailable)
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusServiceUnavailable, app.WireUnavailable)
	if dep.count() != 2 {
		t.Errorf("an open breaker let a call through: %d calls", dep.count())
	}
	if r := readiness(t, a); r.status != http.StatusOK || !strings.Contains(string(r.body), degraded) {
		t.Errorf("the readiness: %d %s", r.status, r.body)
	}
	if !strings.Contains(logs.String(), "the breaker of orders/port/quote opened") {
		t.Errorf("the opening is not logged: %s", logs.String())
	}
	clk.Advance(10 * time.Second)
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusOK, "")
	if r := readiness(t, a); !strings.Contains(string(r.body), `"name":"breaker:orders/port/quote","status":"healthy"`) {
		t.Errorf("closed, the readiness says: %s", r.body)
	}
}

// A breaker the SDK refuses with the values the start resolved stops the
// start, naming what it refused: a window too short to be cut in buckets.
func TestABreakerTheSDKRefusesStopsTheStart(t *testing.T) {
	_, err := startPolicies(t, []*app.Service{guardedService(newDependency(), app.Breaker(2, time.Second))},
		app.Set("orders-port-quote-breaker-window", "5ns"))
	if err == nil || !strings.Contains(err.Error(), "its breaker is refused") || !strings.Contains(err.Error(), "Window.Size") {
		t.Errorf("a window of 5ns: %v", err)
	}
}

// breakerApp is an app whose Main runs commands, its data in dir.
func breakerApp(t *testing.T, dir string, s *app.Service) *app.App {
	t.Helper()
	t.Setenv("SHOP_SMTP_URL", "")
	opts := []app.AppConfigurer{app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard)}
	if dir == "" {
		opts = append(opts, app.InMemory())
	} else {
		opts = append(opts, app.DataDir(dir))
	}
	return app.NewApp("shop", s).With(opts...)
}

// An operator isolates a port's breaker with the product's breaker command,
// a mark in the data directory, and takes the isolation off; a port no
// breaker guards, a product in memory and a word the command does not know
// are refused.
func TestTheBreakerCommandMarksAPort(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	mark := filepath.Join(dir, "orders", "ports", "quote.isolated")
	a := breakerApp(t, dir, guardedService(newDependency(), app.Breaker(2, time.Second)))
	if code := a.Main(t.Context(), []string{"breaker", "isolate", "orders/port/quote"}); code != 0 {
		t.Fatalf("isolate: %d", code)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Errorf("no mark: %v", err)
	}
	if code := a.Main(t.Context(), []string{"breaker"}); code != 0 {
		t.Errorf("list: %d", code)
	}
	if code := a.Main(t.Context(), []string{"breaker", "reset", "orders/port/quote"}); code != 0 {
		t.Errorf("reset: %d", code)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Errorf("the mark is still there: %v", err)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"breaker", "isolate", "orders/port/other"}, 1},
		{[]string{"breaker", "open", "orders/port/quote"}, 2},
		{[]string{"breaker", "isolate"}, 2},
	} {
		if code := a.Main(t.Context(), tc.args); code != tc.code {
			t.Errorf("%v: %d, want %d", tc.args, code, tc.code)
		}
	}
	if code := breakerApp(t, "", guardedService(newDependency(), app.Breaker(2, time.Second))).Main(t.Context(), []string{"breaker"}); code != 1 {
		t.Errorf("in memory: %d, want 1", code)
	}
}

// answersWith waits until the price answers status: what the breakers'
// component applies, it applies on its own goroutine.
func answersWith(t *testing.T, a *app.App, status int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for call(t, a, "GET /orders/price", noBody).status != status {
		if time.Now().After(deadline) {
			t.Fatalf("the price never answered %d", status)
		}
		runtime.Gosched()
	}
}

// An isolation is applied at the start, before any call, and a running
// product applies a mark taken off or put back once it read the marks again
// on the app's clock.
func TestAnIsolatedBreakerRefusesUntilItIsReset(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	mark := filepath.Join(dir, "orders", "ports", "quote.isolated")
	if err := os.MkdirAll(filepath.Dir(mark), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mark, []byte("orders/port/quote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	dep := newDependency()
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Breaker(2, time.Second))}, app.Clock(clk), app.DataDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusServiceUnavailable, app.WireUnavailable)
	if r := readiness(t, a); !strings.Contains(string(r.body), degraded) {
		t.Errorf("the readiness: %s", r.body)
	}
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	answersWith(t, a, http.StatusOK)
	if err := os.WriteFile(mark, []byte("orders/port/quote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	answersWith(t, a, http.StatusServiceUnavailable)
	if dep.count() == 0 {
		t.Errorf("the reset breaker reached nothing")
	}
}

// A product's CLI command runs with the operator's isolations applied, its
// data directory opened for them although it keeps no store; and no product
// command may take Main's breaker, which an app has once a port declares one.
func TestACLICommandMeetsAnIsolatedBreaker(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	mark := filepath.Join(dir, "orders", "ports", "quote.isolated")
	if err := os.MkdirAll(filepath.Dir(mark), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mark, []byte("orders/port/quote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dep := newDependency()
	s := app.NewService("orders", "Prices, asked from the command line.")
	fallback := s.Query("dependency", dep.handle)
	quote := s.Port[guardAsk, guardPrice]("quote", app.Fallback[guardAsk, guardPrice](fallback)).Guard(app.Breaker(2, time.Second))
	s.CLI("price", "Print a price.", func(ctx context.Context, _ []string, _ app.StdioValue) int {
		if _, err := quote.Call(ctx, guardAsk{}); err != nil {
			return 7
		}
		return 0
	})
	if status := breakerApp(t, dir, s).With(app.Profile(model.ProfileCLI)).Main(t.Context(), []string{"price"}); status != 7 || dep.count() != 0 {
		t.Errorf("an isolated port called from the command line: status %d, %d calls", status, dep.count())
	}

	taken := app.NewService("orders", "A command named breaker.")
	taken.CLI("breaker", "Mine.", func(context.Context, []string, app.StdioValue) int { return 0 })
	if _, err := startPolicies(t, []*app.Service{taken}); err == nil || !strings.Contains(err.Error(), `takes the name "breaker"`) {
		t.Errorf("a product command named breaker: %v", err)
	}
}

// A product whose ports declare no breaker has no breaker command.
func TestNoBreakerNoCommand(t *testing.T) {
	a := breakerApp(t, t.TempDir(), guardedService(newDependency(), app.Timeout(time.Second)))
	if code := a.Main(context.Background(), []string{"breaker"}); code != 2 {
		t.Errorf("breaker without one: %d, want the usage's 2", code)
	}
}
