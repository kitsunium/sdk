package app_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// policyKnob is one setting a policy declares: its key, its variable, its
// type, the value the code gives it, and another a configuration gives.
type policyKnob struct {
	key, variable, kind, def, other string
}

// policyKnobs are the settings of policyService's policies.
var policyKnobs = []policyKnob{
	{"orders-endpoint-checkout-rate", "SHOP_ORDERS_ENDPOINT_CHECKOUT_RATE", "number", "5", "7.5"},
	{"orders-endpoint-checkout-burst", "SHOP_ORDERS_ENDPOINT_CHECKOUT_BURST", "int", "3", "4"},
	{"orders-endpoint-checkout-rate-wait", "SHOP_ORDERS_ENDPOINT_CHECKOUT_RATE_WAIT", "duration", "20ms", "40ms"},
	{"orders-endpoint-checkout-timeout", "SHOP_ORDERS_ENDPOINT_CHECKOUT_TIMEOUT", "duration", "2s", "3s"},
	{"orders-endpoint-checkout-bulkhead", "SHOP_ORDERS_ENDPOINT_CHECKOUT_BULKHEAD", "int", "4", "6"},
	{"orders-endpoint-checkout-bulkhead-queue", "SHOP_ORDERS_ENDPOINT_CHECKOUT_BULKHEAD_QUEUE", "int", "8", "9"},
	{"orders-endpoint-checkout-bulkhead-wait", "SHOP_ORDERS_ENDPOINT_CHECKOUT_BULKHEAD_WAIT", "duration", "30ms", "50ms"},
	{"orders-query-lookup-client-rate", "SHOP_ORDERS_QUERY_LOOKUP_CLIENT_RATE", "number", "10", "12"},
	{"orders-query-lookup-client-burst", "SHOP_ORDERS_QUERY_LOOKUP_CLIENT_BURST", "int", "2", "5"},
}

// policyService is a service whose endpoint takes every policy that has
// settings of its own, and whose query is limited per client.
func policyService() *app.Service {
	s := app.NewService("orders", "Orders, behind every policy.")
	s.Endpoint("GET /orders/checkout", func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil },
		app.Name("checkout"), app.RateLimit(5, 3, app.MaxWait(20*time.Millisecond)), app.Timeout(2*time.Second),
		app.Bulkhead(4, app.BulkheadQueue(8), app.MaxWait(30*time.Millisecond)))
	s.Query("lookup", func(context.Context, app.EmptyValue) (int, error) { return 1, nil }, app.RateLimitPerClient(10, 2))
	return s
}

// startPolicies starts services as one app, or returns why it would not.
func startPolicies(t *testing.T, services []*app.Service, opts ...app.AppConfigurer) (*app.App, error) {
	t.Helper()
	t.Setenv("SHOP_SMTP_URL", "")
	a := app.NewApp("shop", services...).With(append([]app.AppConfigurer{
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a, nil
}

// A policy declares its settings in the service of the operation it guards,
// the code's value their default: every one of them, said by the
// configuration as coming from the code.
func TestAPolicysSettingsDefaultToTheCode(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{policyService()})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range policyKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			if s.Key != k.key || s.Service != "orders" || s.Type != k.kind || s.From != model.SettingDefault || s.Value != k.def {
				t.Errorf("%+v, want the key %s, a %s from the code: %s", s, k.key, k.kind, k.def)
			}
		})
	}
	pipeline := a.Graph().Node("orders/endpoint/checkout").Endpoint.Pipeline
	want := map[string]map[string]string{
		"ratelimit": {"rate": "5", "burst": "3", "maxWait": "20ms"},
		"timeout":   {"after": "2s"},
		"bulkhead":  {"max": "4", "queue": "8", "maxWait": "30ms"},
	}
	for _, m := range pipeline {
		for key, value := range want[m.Kind] {
			if m.Config[key] != value {
				t.Errorf("%s %s: %q, want %q", m.Kind, key, m.Config[key], value)
			}
		}
	}
}

// A configuration file overrides each setting of a policy, and the variable
// overrides the file: the configuration says where each value came from,
// and the graph draws the values the policies run with.
func TestAPolicysSettingsAreOverriddenByTheConfiguration(t *testing.T) {
	var file strings.Builder
	file.WriteString("{")
	for i, k := range policyKnobs {
		if i > 0 {
			file.WriteString(",")
		}
		value := k.other
		if k.kind == "duration" {
			value = `"` + value + `"`
		}
		file.WriteString(`"` + k.key + `":` + value)
	}
	file.WriteString("}")
	t.Setenv("SHOP_ORDERS_ENDPOINT_CHECKOUT_TIMEOUT", "4s")
	a, err := startPolicies(t, []*app.Service{policyService()},
		app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(file.String())}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range policyKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			want := model.Setting{From: model.SettingFile, Detail: "config/dev.json", Value: k.other}
			if k.variable == "SHOP_ORDERS_ENDPOINT_CHECKOUT_TIMEOUT" {
				want = model.Setting{From: model.SettingEnv, Value: "4s"}
			}
			if s.From != want.From || s.Detail != want.Detail || s.Value != want.Value {
				t.Errorf("%+v, want %+v", s, want)
			}
		})
	}
	for _, m := range a.Graph().Node("orders/endpoint/checkout").Endpoint.Pipeline {
		if m.Kind == "timeout" && m.Config["after"] != "4s" || m.Kind == "bulkhead" && m.Config["max"] != "6" ||
			m.Kind == "ratelimit" && (m.Config["rate"] != "7.5" || m.Config["maxWait"] != "40ms") {
			t.Errorf("the graph draws %s %v", m.Kind, m.Config)
		}
	}
}

// A key no operation's policy declares is refused, as any key no service
// declares: a setting of an operation that does not exist, of another kind,
// or misspelled.
func TestAKeyNoPolicyDeclaresIsRefused(t *testing.T) {
	for _, k := range policyKnobs {
		t.Run(k.key, func(t *testing.T) {
			prefix, knob := "orders-endpoint-checkout-", strings.TrimPrefix(k.key, "orders-endpoint-checkout-")
			if knob == k.key {
				prefix, knob = "orders-query-lookup-", strings.TrimPrefix(k.key, "orders-query-lookup-")
			}
			for _, stray := range []string{"orders-endpoint-cancel-" + knob, prefix + knob + "s", "orders-command-checkout-" + knob} {
				_, err := startPolicies(t, []*app.Service{policyService()},
					app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + stray + `": 1}`)}}))
				if err == nil || !strings.Contains(err.Error(), `config/config.json sets "`+stray+`", which no service declares`) {
					t.Errorf("%s: %v", stray, err)
				}
			}
		})
	}
}

// A value a policy cannot run with stops the start, from the configuration
// and from the code alike — where the SDK would refuse every call.
func TestAPolicyValueItCannotRunWithStopsTheStart(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"orders-endpoint-checkout-rate", "0"},
		{"orders-endpoint-checkout-burst", "0"},
		{"orders-endpoint-checkout-rate-wait", `"-1s"`},
		{"orders-endpoint-checkout-timeout", `"0s"`},
		{"orders-endpoint-checkout-bulkhead", "0"},
		{"orders-endpoint-checkout-bulkhead-queue", "-1"},
		{"orders-endpoint-checkout-bulkhead-wait", `"-1ms"`},
		{"orders-query-lookup-client-rate", "-2"},
		{"orders-query-lookup-client-burst", "0"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{policyService()},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": ` + tc.value + `}`)}}))
			if err == nil || !strings.Contains(err.Error(), `setting "`+tc.key+`"`) {
				t.Errorf("%s = %s: %v", tc.key, tc.value, err)
			}
		})
	}
	s := app.NewService("orders", "A rate of zero.")
	s.Query("lookup", func(context.Context, app.EmptyValue) (int, error) { return 1, nil }, app.RateLimit(0, 1))
	if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), `setting "orders-query-lookup-rate"`) {
		t.Errorf("a rate of zero in the code: %v", err)
	}
}

// An operation takes one policy of a kind: a second one would share its
// settings, and the declaration says so.
func TestAPolicyTwiceOnAnOperationIsRefused(t *testing.T) {
	s := app.NewService("orders", "Two timeouts.")
	s.Query("lookup", func(context.Context, app.EmptyValue) (int, error) { return 1, nil }, app.Timeout(time.Second), app.Timeout(time.Minute))
	if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), `its policy's setting "orders-query-lookup-timeout" is declared already`) {
		t.Errorf("two timeouts: %v", err)
	}
}

// A rate limit's 429 says, in Retry-After, how many whole seconds the
// bucket needs to earn its next token — rounded up —, and the
// configuration's burst is the one the bucket holds.
func TestARateLimitSaysWhenToRetry(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	s := app.NewService("orders", "Rate limited.")
	noop := func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil }
	s.Endpoint("GET /orders/shared", noop, app.Name("shared"), app.RateLimit(0.4, 1))
	s.Endpoint("GET /orders/mine", noop, app.Name("mine"), app.RateLimitPerClient(1, 1))
	a, err := startPolicies(t, []*app.Service{s}, app.Clock(clk), app.Set("orders-endpoint-shared-burst", 2))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/shared", noBody), http.StatusNoContent, "")
	expectAnswer(t, call(t, a, "GET /orders/shared", noBody), http.StatusNoContent, "")
	r := call(t, a, "GET /orders/shared", noBody)
	if expectAnswer(t, r, http.StatusTooManyRequests, app.WireRateLimited); r.header.Get("Retry-After") != "3" {
		t.Errorf("a token due in 2.5s: Retry-After %q", r.header.Get("Retry-After"))
	}
	expectAnswer(t, call(t, a, "GET /orders/mine", noBody), http.StatusNoContent, "")
	r = call(t, a, "GET /orders/mine", noBody)
	if expectAnswer(t, r, http.StatusTooManyRequests, app.WireRateLimited); r.header.Get("Retry-After") != "1" {
		t.Errorf("a client's token due in 1s: Retry-After %q", r.header.Get("Retry-After"))
	}
	clk.Advance(time.Second)
	expectAnswer(t, call(t, a, "GET /orders/mine", noBody), http.StatusNoContent, "")
}

// held is a handler that waits until it is released or its context ends,
// and says when it starts.
type held struct {
	started chan struct{}
	release chan struct{}
}

func newHeld() *held { return &held{started: make(chan struct{}, 8), release: make(chan struct{})} }

func (h *held) handle(ctx context.Context, _ app.EmptyValue) (app.EmptyValue, error) {
	h.started <- struct{}{}
	select {
	case <-h.release:
		return app.EmptyValue{}, nil
	case <-ctx.Done():
		return app.EmptyValue{}, ctx.Err()
	}
}

// answered runs a request on a goroutine of its own, and hands its answer.
func answered(t *testing.T, a *app.App, route string) <-chan response {
	done := make(chan response, 1)
	go func() { done <- call(t, a, route, noBody) }()
	return done
}

// A rate limit given MaxWait holds a call whose token is due within it,
// on the app's clock, instead of refusing it; a bulkhead given a queue and a
// wait holds the overflow in it, refuses what the queue cannot hold, and
// what waited past its wait.
func TestAPolicyWaitsOnTheAppsClock(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	s := app.NewService("orders", "Waiting.")
	noop := func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil }
	s.Endpoint("GET /orders/paced", noop, app.Name("paced"), app.RateLimit(1, 1, app.MaxWait(time.Second)))
	crowd := newHeld()
	s.Endpoint("GET /orders/crowded", crowd.handle, app.Name("crowded"), app.Bulkhead(1, app.BulkheadQueue(1), app.MaxWait(time.Minute)))
	a, err := startPolicies(t, []*app.Service{s}, app.Clock(clk))
	if err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/paced", noBody), http.StatusNoContent, "")
	base := clk.Pending()
	paced := answered(t, a, "GET /orders/paced")
	armed(t, clk, base+1)
	clk.Advance(time.Second)
	expectAnswer(t, <-paced, http.StatusNoContent, "")

	first := answered(t, a, "GET /orders/crowded")
	<-crowd.started
	base = clk.Pending()
	queued := answered(t, a, "GET /orders/crowded")
	armed(t, clk, base+1)
	expectAnswer(t, call(t, a, "GET /orders/crowded", noBody), http.StatusServiceUnavailable, app.WireUnavailable)
	crowd.release <- struct{}{}
	expectAnswer(t, <-first, http.StatusNoContent, "")
	<-crowd.started
	crowd.release <- struct{}{}
	expectAnswer(t, <-queued, http.StatusNoContent, "")

	first = answered(t, a, "GET /orders/crowded")
	<-crowd.started
	base = clk.Pending()
	late := answered(t, a, "GET /orders/crowded")
	armed(t, clk, base+1)
	clk.Advance(time.Minute)
	expectAnswer(t, <-late, http.StatusServiceUnavailable, app.WireUnavailable)
	crowd.release <- struct{}{}
	expectAnswer(t, <-first, http.StatusNoContent, "")
}

// A timeout runs on the app's clock, and its configured value is the one
// it waits.
func TestATimeoutRunsOnTheAppsClock(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	s := app.NewService("orders", "Slow.")
	slow := newHeld()
	s.Endpoint("GET /orders/slow", slow.handle, app.Name("slow"), app.Timeout(time.Hour))
	a, err := startPolicies(t, []*app.Service{s}, app.Clock(clk), app.Set("orders-endpoint-slow-timeout", "5s"))
	if err != nil {
		t.Fatal(err)
	}
	base := clk.Pending()
	r := answered(t, a, "GET /orders/slow")
	<-slow.started
	armed(t, clk, base+1)
	clk.Advance(4 * time.Second)
	select {
	case got := <-r:
		t.Fatalf("answered before its timeout: %d %s", got.status, got.body)
	default:
	}
	clk.Advance(time.Second)
	expectAnswer(t, <-r, http.StatusGatewayTimeout, app.WireTimeout)
}

// A caller that leaves before the answer is no timeout and no failure of
// the product: no 504, no error logged — the request is recorded as 499,
// as information.
func TestACallerThatLeavesIsNoTimeout(t *testing.T) {
	var logs syncBuffer
	s := app.NewService("orders", "Slow.")
	slow := newHeld()
	s.Endpoint("GET /orders/slow", slow.handle, app.Name("slow"), app.Timeout(time.Hour))
	a, err := startPolicies(t, []*app.Service{s}, app.Logs(&logs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL()+"/orders/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	<-slow.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller's request: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "the caller left before the answer") {
		if time.Now().After(deadline) {
			t.Fatalf("the departure is not logged: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	out := logs.String()
	if !strings.Contains(out, "499") || strings.Contains(out, "request failed") || strings.Contains(out, "deadline") {
		t.Errorf("the logs: %s", out)
	}
}

// An operation whose policy's setting would be longer than a setting's name
// may be still runs it, with the code's values: the declaration warns, and
// the start goes on.
func TestAPolicyWithATooLongNameKeepsTheCodesValues(t *testing.T) {
	s := app.NewService("orders", "A long name.")
	route := "GET /orders/" + strings.Repeat("x", 60)
	s.Endpoint(route, func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.Timeout(time.Second))
	a, err := startPolicies(t, []*app.Service{s})
	if err != nil {
		t.Fatalf("a long name stopped the start: %v", err)
	}
	warned := false
	for _, d := range a.Graph().Diagnostics {
		warned = warned || d.Severity == "warning" && strings.Contains(d.Message, "the policy runs with the code's values")
	}
	if !warned {
		t.Errorf("no warning: %+v", a.Graph().Diagnostics)
	}
	expectAnswer(t, call(t, a, route, noBody), http.StatusNoContent, "")
}

// A command and the endpoint that exposes it are named alike, and each may
// take a policy of the same kind: their settings are told apart by kind.
func TestACommandAndItsExposureEachTakeAPolicy(t *testing.T) {
	s := app.NewService("orders", "A command and its exposure.")
	s.Command("place", func(context.Context, app.EmptyValue) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.Timeout(time.Second)).
		Expose("POST /orders/place", app.Anyone(), app.Timeout(2*time.Second))
	a, err := startPolicies(t, []*app.Service{s})
	if err != nil {
		t.Fatal(err)
	}
	if got := settingOf(t, a, "SHOP_ORDERS_COMMAND_PLACE_TIMEOUT"); got.Value != "1s" {
		t.Errorf("the command's: %+v", got)
	}
	if got := settingOf(t, a, "SHOP_ORDERS_ENDPOINT_PLACE_TIMEOUT"); got.Value != "2s" {
		t.Errorf("the exposure's: %+v", got)
	}
}
