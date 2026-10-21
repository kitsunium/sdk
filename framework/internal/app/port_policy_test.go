package app_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// guardAsk and guardPrice are what the guarded port takes and answers.
type guardAsk struct {
	Item string `json:"item"`
}

type guardPrice struct {
	Attempt int `json:"attempt"`
}

// dependency is what a guarded port is bound to: it fails as told, call
// after call, then answers the attempt's number; held, its first call waits
// until it is released or its context ends.
type dependency struct {
	mu      sync.Mutex
	calls   int
	fail    []error
	hold    bool
	started chan struct{}
	release chan struct{}
}

func newDependency(fail ...error) *dependency {
	return &dependency{fail: fail, started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (d *dependency) handle(ctx context.Context, _ guardAsk) (guardPrice, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	var err error
	if n <= len(d.fail) {
		err = d.fail[n-1]
	}
	d.mu.Unlock()
	d.started <- struct{}{}
	if d.hold && n == 1 {
		select {
		case <-d.release:
		case <-ctx.Done():
			return guardPrice{}, ctx.Err()
		}
	}
	if err != nil {
		return guardPrice{}, err
	}
	return guardPrice{Attempt: n}, nil
}

// count is how many calls reached the dependency.
func (d *dependency) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// guardedService is a service whose endpoint GET /orders/price asks its
// port quote, bound to dep and guarded by policies.
func guardedService(dep *dependency, policies ...app.PortPolicyConfigurer) *app.Service {
	s := app.NewService("orders", "Prices, from a dependency.")
	fallback := s.Endpoint("POST /orders/dependency", dep.handle, app.Name("dependency"))
	quote := s.Port[guardAsk, guardPrice]("quote", app.Fallback[guardAsk, guardPrice](fallback)).Guard(policies...)
	s.Endpoint("GET /orders/price", func(ctx context.Context, _ app.EmptyValue) (guardPrice, error) {
		return quote.Call(ctx, guardAsk{Item: "lamp"})
	}, app.Name("price"))
	return s
}

// everyPortPolicy guards a port with every policy, each given values of the
// code's.
func everyPortPolicy() []app.PortPolicyConfigurer {
	return []app.PortPolicyConfigurer{
		app.Timeout(time.Second), app.Throttle(3), app.Idempotent(), app.Hedge(50*time.Millisecond, 8),
		app.Breaker(4, 10*time.Second, app.BreakerWindow(time.Minute, 50, 0.5), app.HalfOpenTrials(2), app.OpenBackoff(time.Minute)),
		app.Budget(20, 0.2), app.Retry(3, 100*time.Millisecond, app.RetryMaxDelay(2*time.Second)),
	}
}

// portKnobs are the settings of everyPortPolicy's policies.
var portKnobs = []policyKnob{
	{"orders-port-quote-budget-tokens", "SHOP_ORDERS_PORT_QUOTE_BUDGET_TOKENS", "int", "20", "25"},
	{"orders-port-quote-budget-ratio", "SHOP_ORDERS_PORT_QUOTE_BUDGET_RATIO", "number", "0.2", "0.3"},
	{"orders-port-quote-retry-attempts", "SHOP_ORDERS_PORT_QUOTE_RETRY_ATTEMPTS", "int", "3", "4"},
	{"orders-port-quote-retry-delay", "SHOP_ORDERS_PORT_QUOTE_RETRY_DELAY", "duration", "100ms", "150ms"},
	{"orders-port-quote-retry-max-delay", "SHOP_ORDERS_PORT_QUOTE_RETRY_MAX_DELAY", "duration", "2s", "3s"},
	{"orders-port-quote-retry-jitter", "SHOP_ORDERS_PORT_QUOTE_RETRY_JITTER", "text", "full", "decorrelated"},
	{"orders-port-quote-retry-after", "SHOP_ORDERS_PORT_QUOTE_RETRY_AFTER", "bool", "true", "false"},
	{"orders-port-quote-hedge-delay", "SHOP_ORDERS_PORT_QUOTE_HEDGE_DELAY", "duration", "50ms", "80ms"},
	{"orders-port-quote-hedge-in-flight", "SHOP_ORDERS_PORT_QUOTE_HEDGE_IN_FLIGHT", "int", "8", "9"},
	{"orders-port-quote-hedge-max", "SHOP_ORDERS_PORT_QUOTE_HEDGE_MAX", "int", "1", "2"},
	{"orders-port-quote-breaker-failures", "SHOP_ORDERS_PORT_QUOTE_BREAKER_FAILURES", "int", "4", "6"},
	{"orders-port-quote-breaker-open", "SHOP_ORDERS_PORT_QUOTE_BREAKER_OPEN", "duration", "10s", "20s"},
	{"orders-port-quote-breaker-open-max", "SHOP_ORDERS_PORT_QUOTE_BREAKER_OPEN_MAX", "duration", "1m0s", "2m0s"},
	{"orders-port-quote-breaker-trials", "SHOP_ORDERS_PORT_QUOTE_BREAKER_TRIALS", "int", "2", "3"},
	{"orders-port-quote-breaker-window", "SHOP_ORDERS_PORT_QUOTE_BREAKER_WINDOW", "duration", "1m0s", "30s"},
	{"orders-port-quote-breaker-window-calls", "SHOP_ORDERS_PORT_QUOTE_BREAKER_WINDOW_CALLS", "int", "50", "60"},
	{"orders-port-quote-breaker-window-ratio", "SHOP_ORDERS_PORT_QUOTE_BREAKER_WINDOW_RATIO", "number", "0.5", "0.25"},
	{"orders-port-quote-throttle-k", "SHOP_ORDERS_PORT_QUOTE_THROTTLE_K", "number", "3", "2.5"},
	{"orders-port-quote-timeout", "SHOP_ORDERS_PORT_QUOTE_TIMEOUT", "duration", "1s", "4s"},
}

// A port's policies declare their settings in the port's service, the
// code's values their defaults, and the graph draws them on the port, the
// outermost first, the budget before them.
func TestAPortsPoliciesSettingsDefaultToTheCode(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{guardedService(newDependency(), everyPortPolicy()...)})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range portKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			if s.Key != k.key || s.Service != "orders" || s.Type != k.kind || s.From != model.SettingDefault || s.Value != k.def {
				t.Errorf("%+v, want the key %s, a %s from the code: %s", s, k.key, k.kind, k.def)
			}
		})
	}
	port := a.Graph().Node("orders/port/quote").Port
	var kinds []string
	for _, m := range port.Pipeline {
		kinds = append(kinds, m.Kind)
	}
	if got := strings.Join(kinds, " "); got != "budget retry hedge breaker throttle timeout" || !port.Idempotent {
		t.Errorf("the port draws %q, idempotent %v", got, port.Idempotent)
	}
}

// A configuration file overrides every setting of a port's policies, and the
// variable overrides the file; the graph draws the values run.
func TestAPortsPoliciesSettingsAreOverriddenByTheConfiguration(t *testing.T) {
	var file strings.Builder
	file.WriteString("{")
	for i, k := range portKnobs {
		if i > 0 {
			file.WriteString(",")
		}
		value := k.other
		if k.kind == "duration" || k.kind == "text" {
			value = `"` + value + `"`
		}
		file.WriteString(`"` + k.key + `":` + value)
	}
	file.WriteString("}")
	t.Setenv("SHOP_ORDERS_PORT_QUOTE_RETRY_ATTEMPTS", "5")
	a, err := startPolicies(t, []*app.Service{guardedService(newDependency(), everyPortPolicy()...)},
		app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(file.String())}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range portKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			want := model.Setting{From: model.SettingFile, Detail: "config/dev.json", Value: k.other}
			if k.variable == "SHOP_ORDERS_PORT_QUOTE_RETRY_ATTEMPTS" {
				want = model.Setting{From: model.SettingEnv, Value: "5"}
			}
			if s.From != want.From || s.Detail != want.Detail || s.Value != want.Value {
				t.Errorf("%+v, want %+v", s, want)
			}
		})
	}
	for _, m := range a.Graph().Node("orders/port/quote").Port.Pipeline {
		if m.Kind == "retry" && (m.Config["attempts"] != "5" || m.Config["jitter"] != "decorrelated") ||
			m.Kind == "breaker" && m.Config["window"] != "30s" || m.Kind == "throttle" && m.Config["k"] != "2.5" ||
			m.Kind == "budget" && m.Config["tokens"] != "25" || m.Kind == "timeout" && m.Config["after"] != "4s" {
			t.Errorf("the graph draws %s %v", m.Kind, m.Config)
		}
	}
}

// A key no port's policy declares is refused: a knob of a policy the port
// does not have, of another port, or misspelled.
func TestAKeyNoPortPolicyDeclaresIsRefused(t *testing.T) {
	for _, stray := range []string{
		"orders-port-quote-retry-attempts", "orders-port-other-breaker-open", "orders-port-quote-breaker-opened",
		"orders-endpoint-quote-timeout",
	} {
		t.Run(stray, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{guardedService(newDependency(), app.Breaker(2, time.Second))},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + stray + `": 1}`)}}))
			if err == nil || !strings.Contains(err.Error(), `config/config.json sets "`+stray+`", which no service declares`) {
				t.Errorf("%s: %v", stray, err)
			}
		})
	}
}

// A value a port's policy cannot run with stops the start, from the
// configuration and from the code alike.
func TestAPortPolicyValueItCannotRunWithStopsTheStart(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"orders-port-quote-budget-tokens", "0"},
		{"orders-port-quote-budget-ratio", "0.0001"},
		{"orders-port-quote-retry-attempts", "0"},
		{"orders-port-quote-retry-delay", `"0s"`},
		{"orders-port-quote-retry-max-delay", `"-1s"`},
		{"orders-port-quote-retry-jitter", `"some"`},
		{"orders-port-quote-hedge-delay", `"0s"`},
		{"orders-port-quote-hedge-in-flight", "0"},
		{"orders-port-quote-hedge-max", "0"},
		{"orders-port-quote-breaker-failures", "0"},
		{"orders-port-quote-breaker-open", `"0s"`},
		{"orders-port-quote-breaker-open-max", `"-1s"`},
		{"orders-port-quote-breaker-trials", "0"},
		{"orders-port-quote-breaker-window", `"-1s"`},
		{"orders-port-quote-breaker-window-calls", "0"},
		{"orders-port-quote-breaker-window-ratio", "1.5"},
		{"orders-port-quote-throttle-k", "0.5"},
		{"orders-port-quote-timeout", `"0s"`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{guardedService(newDependency(), everyPortPolicy()...)},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": ` + tc.value + `}`)}}))
			if err == nil || !strings.Contains(err.Error(), `setting "`+tc.key+`"`) {
				t.Errorf("%s = %s: %v", tc.key, tc.value, err)
			}
		})
	}
	_, err := startPolicies(t, []*app.Service{guardedService(newDependency(), app.Retry(0, time.Second))})
	if err == nil || !strings.Contains(err.Error(), `setting "orders-port-quote-retry-attempts"`) {
		t.Errorf("no attempt in the code: %v", err)
	}
}

// What a port's policies are given wrong is refused when declared: a hedge
// on a port not said idempotent, a budget on a port that neither retries
// nor hedges, a second Guard, a policy twice.
func TestAPortGuardedWrongIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policies []app.PortPolicyConfigurer
		twice    bool
		want     string
	}{
		{"hedge", []app.PortPolicyConfigurer{app.Hedge(time.Second, 2)}, false, "a hedge runs its operation more than once"},
		{"budget", []app.PortPolicyConfigurer{app.Budget(5, 0.1), app.Timeout(time.Second)}, false, "neither retries nor hedges"},
		{"guard", []app.PortPolicyConfigurer{app.Timeout(time.Second)}, true, "is guarded twice"},
		{"retry", []app.PortPolicyConfigurer{app.Retry(2, time.Second), app.Retry(3, time.Second)}, false, "is declared already"},
		{"budgets", []app.PortPolicyConfigurer{app.Retry(2, time.Second), app.Budget(5, 0.1), app.Budget(6, 0.1)}, false, "is declared already"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep := newDependency()
			s := app.NewService("orders", "Guarded wrong.")
			fallback := s.Endpoint("POST /orders/dependency", dep.handle, app.Name("dependency"))
			p := s.Port[guardAsk, guardPrice]("quote", app.Fallback[guardAsk, guardPrice](fallback)).Guard(tc.policies...)
			if tc.twice {
				p.Guard(tc.policies...)
			}
			if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%v, want %q", err, tc.want)
			}
		})
	}
}

// A retry replays what fails for the dependency's reason, on the app's
// clock, its waits the backoff's — without jitter here —, counts its retries
// on the port's span, and replays no refusal of the caller's own.
func TestAPortRetriesOnTheAppsClock(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	dep := newDependency(app.Unavailable("down"), app.Unavailable("down"))
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Retry(3, 100*time.Millisecond))},
		app.Clock(clk), app.Set("orders-port-quote-retry-jitter", "none"))
	if err != nil {
		t.Fatal(err)
	}
	base := clk.Pending()
	r := answered(t, a, "GET /orders/price")
	armed(t, clk, base+1)
	clk.Advance(99 * time.Millisecond)
	if dep.count() != 1 {
		t.Fatalf("retried before its wait: %d calls", dep.count())
	}
	clk.Advance(time.Millisecond)
	armed(t, clk, base+1)
	clk.Advance(200 * time.Millisecond)
	got := <-r
	var price guardPrice
	expectAnswer(t, got, http.StatusOK, "")
	got.json(t, &price)
	if price.Attempt != 3 {
		t.Errorf("answered by attempt %d, want 3", price.Attempt)
	}
	if retries := spanOf(t, traceOf(t, a, got), "orders/port/quote").Attrs["retries"]; retries != "2" {
		t.Errorf("the port's span counts %q retries", retries)
	}
}

// A refusal of the caller's own is not replayed, and an attempt the retry
// cannot replay any more fails the call.
func TestAPortRetriesOnlyTheDependencysFailures(t *testing.T) {
	dep := newDependency(app.NotFound("no such item"))
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Retry(3, time.Millisecond))})
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusNotFound, app.WireNotFound)
	if dep.count() != 1 {
		t.Errorf("a 404 retried: %d calls", dep.count())
	}
	down := newDependency(app.Unavailable("down"), app.Unavailable("down"), app.Unavailable("down"))
	b, err := startPolicies(t, []*app.Service{guardedService(down, app.Retry(2, time.Millisecond, app.RetryMaxDelay(time.Millisecond)))})
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, b, "GET /orders/price", noBody), http.StatusInternalServerError, app.WireInternal)
	if down.count() != 2 {
		t.Errorf("an exhausted retry made %d calls, want 2", down.count())
	}
}

// A failure that says how long to wait — the dependency's rate limiter's
// next token — is waited that long, unless the port's setting says not to.
func TestAPortRetryHonoursRetryAfter(t *testing.T) {
	for _, honour := range []bool{true, false} {
		t.Run(map[bool]string{true: "honoured", false: "ignored"}[honour], func(t *testing.T) {
			clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
			s := app.NewService("orders", "A rate-limited dependency.")
			dep := newDependency()
			fallback := s.Endpoint("POST /orders/dependency", dep.handle, app.Name("dependency"), app.RateLimit(5, 1))
			quote := s.Port[guardAsk, guardPrice]("quote", app.Fallback[guardAsk, guardPrice](fallback)).
				Guard(app.Retry(5, 100*time.Millisecond))
			s.Endpoint("GET /orders/price", func(ctx context.Context, _ app.EmptyValue) (guardPrice, error) {
				return quote.Call(ctx, guardAsk{})
			}, app.Name("price"))
			a, err := startPolicies(t, []*app.Service{s}, app.Clock(clk),
				app.Set("orders-port-quote-retry-jitter", "none"), app.Set("orders-port-quote-retry-after", honour))
			if err != nil {
				t.Fatal(err)
			}
			expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusOK, "")
			base := clk.Pending()
			r := answered(t, a, "GET /orders/price")
			armed(t, clk, base+1)
			// The token is due in 200ms; the backoff waits 100ms, then 200ms.
			retries := "2"
			if honour {
				clk.Advance(199 * time.Millisecond)
				if dep.count() != 1 {
					t.Fatalf("retried before the token was due")
				}
				clk.Advance(time.Millisecond)
				retries = "1"
			} else {
				clk.Advance(100 * time.Millisecond)
				armed(t, clk, base+1)
				clk.Advance(200 * time.Millisecond)
			}
			got := <-r
			expectAnswer(t, got, http.StatusOK, "")
			if n := spanOf(t, traceOf(t, a, got), "orders/port/quote").Attrs["retries"]; n != retries {
				t.Errorf("%s retries, want %s", n, retries)
			}
		})
	}
}

// A port's retry budget refuses a retry once the dependency drained it:
// the call fails without waiting.
func TestAPortsBudgetRefusesARetry(t *testing.T) {
	dep := newDependency(app.Unavailable("down"), app.Unavailable("down"))
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Retry(3, time.Hour), app.Budget(2, 0.1))})
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/price", noBody), http.StatusInternalServerError, app.WireInternal)
	if dep.count() != 1 {
		t.Errorf("a drained budget let %d calls through", dep.count())
	}
}

// A port's timeout is a deadline on each attempt, on the app's clock: the
// attempt that outruns it is replayed by the retry around it.
func TestAPortsTimeoutBoundsEachAttempt(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	dep := newDependency()
	dep.hold = true
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Timeout(time.Second), app.Retry(2, time.Millisecond))},
		app.Clock(clk), app.Set("orders-port-quote-retry-jitter", "none"))
	if err != nil {
		t.Fatal(err)
	}
	base := clk.Pending()
	r := answered(t, a, "GET /orders/price")
	<-dep.started
	armed(t, clk, base+1)
	clk.Advance(time.Second)
	armed(t, clk, base+1)
	clk.Advance(time.Millisecond)
	var price guardPrice
	got := <-r
	expectAnswer(t, got, http.StatusOK, "")
	got.json(t, &price)
	if price.Attempt != 2 {
		t.Errorf("answered by attempt %d, want the retry's", price.Attempt)
	}
}

// A hedge on an idempotent port races a slow attempt with a duplicate once
// its delay passed on the app's clock: the first to answer answers.
func TestAPortHedgesItsSlowCall(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	dep := newDependency()
	dep.hold = true
	a, err := startPolicies(t, []*app.Service{guardedService(dep, app.Idempotent(), app.Hedge(100*time.Millisecond, 4))}, app.Clock(clk))
	if err != nil {
		t.Fatal(err)
	}
	base := clk.Pending()
	r := answered(t, a, "GET /orders/price")
	<-dep.started
	armed(t, clk, base+1)
	clk.Advance(99 * time.Millisecond)
	if dep.count() != 1 {
		t.Fatalf("a duplicate before the delay")
	}
	clk.Advance(time.Millisecond)
	var price guardPrice
	got := <-r
	expectAnswer(t, got, http.StatusOK, "")
	got.json(t, &price)
	if price.Attempt != 2 {
		t.Errorf("answered by attempt %d, want the duplicate", price.Attempt)
	}
}
