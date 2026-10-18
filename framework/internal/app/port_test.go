package app_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// startApp runs services as one app for one test: in memory, on a free
// port, in dev.
func startApp(t *testing.T, services []*app.Service, opts ...app.AppConfigurer) *app.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	a := app.NewApp("shop", services...).With(append([]app.AppConfigurer{
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a
}

// quote asks the shop the price of an item, over HTTP.
func quote(t *testing.T, a *app.App, id string) (Quoted, response) {
	t.Helper()
	r := call(t, a, "GET /items/"+id+"/quote", noBody)
	if r.status != http.StatusOK {
		t.Fatalf("quote: %d %s", r.status, r.body)
	}
	var q Quoted
	r.json(t, &q)
	return q, r
}

// fixedPrice is a price nobody computes.
func fixedPrice(context.Context, QuoteInput) (Quoted, error) {
	return Quoted{Price: 1, By: "fixed"}, nil
}

// hop is how a span came to its node: from which node, over which edge, as
// which operation, under which span.
type hop struct {
	From   string
	Edge   model.EdgeKind
	Op     string
	Parent string
}

func hopOf(s model.Span) hop { return hop{From: s.From, Edge: s.Edge, Op: s.Op, Parent: s.ParentID} }

// expectBinding checks what a port calls and how, its contract, and that
// its binding is a declared edge.
func expectBinding(t *testing.T, g *model.Graph, port string, want model.PortInfo) {
	t.Helper()
	n := g.Node(port)
	if n == nil || n.Port == nil {
		t.Fatalf("no port %s", port)
	}
	got := *n.Port
	if got.Request == nil || got.Response == nil {
		t.Errorf("%s has no contract: %+v", port, got)
	}
	got.Request, got.Response = nil, nil
	if got != want {
		t.Errorf("%s: %+v, want %+v", port, got, want)
	}
	// The binding draws the verb of what it calls: a query is asked, a
	// command dispatched, an endpoint called.
	verb := model.EdgeCalls
	if b := g.Node(want.Bound); b != nil && b.Kind == model.KindQuery {
		verb = model.EdgeAsks
	} else if b != nil && b.Kind == model.KindCommand {
		verb = model.EdgeDispatches
	}
	if e := g.Edge(port + "|" + string(verb) + "|" + want.Bound); want.Bound != "" && (e == nil || !e.Declared) {
		t.Errorf("the binding of %s is not a declared %s edge: %+v", port, verb, e)
	}
}

// expectInvalid checks err is the 400 a validation answers, and returns it.
func expectInvalid(t *testing.T, err error, what string) *app.Error {
	t.Helper()
	var ke *app.Error
	if !errorsAs(err, &ke) || ke.Status != http.StatusBadRequest || ke.Code != app.WireInvalid {
		t.Errorf("%s: %v, want invalid", what, err)
	}
	return ke
}

// The shop without the members: nothing implements the quote, nothing binds
// it, so it calls its fallback — in a span on the port, from its caller, and
// then in the fallback's own.
func TestAPortCallsItsFallback(t *testing.T) {
	a := startApp(t, []*app.Service{Shop, Audit})
	it := create(t, a, "lamp", 1)
	q, r := quote(t, a, it.ID)
	if q != (Quoted{Price: 10, By: "list"}) {
		t.Fatalf("the fallback's answer: %+v", q)
	}
	tr := traceOf(t, a, r)
	port := spanOf(t, tr, "shop/port/quote")
	if got := hopOf(port); got.From != "shop/endpoint/QuoteItem" || got.Edge != model.EdgeCalls || got.Op != model.OpCall {
		t.Errorf("the port's span: %+v", port)
	}
	if got := hopOf(spanOf(t, tr, "shop/endpoint/ListPrice")); got != (hop{From: "shop/port/quote", Edge: model.EdgeCalls, Op: model.OpCall, Parent: port.SpanID}) {
		t.Errorf("the fallback's span: %+v, under the port's %s", got, port.SpanID)
	}
	g := a.Graph()
	expectBinding(t, g, "shop/port/quote", model.PortInfo{Fallback: "shop/endpoint/ListPrice", Bound: "shop/endpoint/ListPrice", Via: model.ViaFallback})
	if e := g.Edge("shop/endpoint/QuoteItem|calls|shop/port/quote"); e == nil || e.Observed == nil {
		t.Errorf("the caller's edge to the port, observed: %+v", e)
	}
}

// What a port calls validates what it is given, whoever calls it.
func TestWhatAPortCallsValidatesItsRequest(t *testing.T) {
	startApp(t, []*app.Service{Shop, Audit})
	_, err := Quote.Call(t.Context(), QuoteInput{})
	expectInvalid(t, err, "an invalid request through the port")
}

// app.Bind chooses, whatever the fallback says; the diagram draws what the
// app chose.
func TestBindWinsOverTheFallback(t *testing.T) {
	pricing := app.NewService("pricing", "Prices set by hand.")
	fixed := pricing.Query("fixed", fixedPrice)
	a := startApp(t, []*app.Service{Shop, Audit, pricing}, app.Bind(Quote, fixed))
	it := create(t, a, "lamp", 1)
	if q, _ := quote(t, a, it.ID); q.By != "fixed" {
		t.Fatalf("the bound operation's answer: %+v", q)
	}
	g := a.Graph()
	expectBinding(t, g, "shop/port/quote", model.PortInfo{Fallback: "shop/endpoint/ListPrice", Bound: "pricing/query/fixed", Via: model.ViaBind})
	if g.Edge("shop/port/quote|calls|shop/endpoint/ListPrice") != nil {
		t.Error("the fallback the app did not choose is drawn as the port's binding")
	}
}

// expectProblem checks the start said a problem holding said, at the line of
// file holding where, in French too.
func expectProblem(t *testing.T, de *app.DiagnosticsError, file, said, where string) {
	t.Helper()
	d := diagnosticSaying(de.Diagnostics, said)
	if d == nil {
		t.Errorf("no problem says %q:\n%v", said, de)
		return
	}
	if want := (model.Source{File: "framework/internal/app/" + file, Line: lineIn(t, file, where)}); d.Source == nil || *d.Source != want {
		t.Errorf("%q is said at %+v, want %+v", said, d.Source, want)
	}
	if d.Texts["fr"] == "" || d.Texts["fr"] == d.Message {
		t.Errorf("%q is not said in French: %+v", said, d.Texts)
	}
}

// startUnbindable starts an app whose ports cannot be bound, and returns what
// the start refused.
func startUnbindable(t *testing.T) *app.DiagnosticsError {
	t.Helper()
	desk := app.NewService("desk", "Ports that cannot be bound.")
	away := app.NewService("away", "A service the app does not mount.")
	remote := away.Query("remote", fixedPrice)
	elsewhere := away.Port[QuoteInput, Quoted]("elsewhere")
	lonely := desk.Port[QuoteInput, Quoted]("lonely")
	desk.Port[QuoteInput, Quoted]("loop", app.Fallback(lonely))
	desk.Port[QuoteInput, Quoted]("far", app.Fallback(remote))
	toPort := desk.Port[QuoteInput, Quoted]("to-port")
	toAway := desk.Port[QuoteInput, Quoted]("to-away")
	var nowhere *app.PortService[QuoteInput, Quoted]
	desk.Implement(nowhere, fixedPrice)
	err := app.NewApp("x", desk).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard),
		app.Bind(toPort, lonely),
		app.Bind(toAway, remote),
		app.Bind(elsewhere, remote),
	).Start(t.Context())
	var de *app.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want every problem", err)
	}
	return de
}

// The start refuses a port it cannot bind and a binding it cannot make —
// every one of them at once, each where it is written.
func TestPortProblemsRefuseTheStart(t *testing.T) {
	de := startUnbindable(t)
	for said, where := range map[string]string{
		"port desk/port/lonely has nothing bound: implement it on a service the app mounts — var _ = Service.Implement(port, handler), the handler a func(context.Context, app_test.QuoteInput) (app_test.Quoted, error)": `lonely := desk.Port`,
		"port desk/port/loop falls back to port desk/port/lonely: a port calls an operation, never another port":                                                                                                          `desk.Port[QuoteInput, Quoted]("loop"`,
		`port desk/port/far falls back to away/query/remote, of service "away", which the app does not mount`:                                                                                                             `desk.Port[QuoteInput, Quoted]("far"`,
		"app.Bind binds port desk/port/to-port to port desk/port/lonely":                                                                                                                                                  `app.Bind(toPort, lonely)`,
		`app.Bind binds port desk/port/to-away to away/query/remote, of service "away", which the app does not mount`:                                                                                                     `app.Bind(toAway, remote)`,
		`app.Bind binds port away/port/elsewhere, of service "away", which the app does not mount`:                                                                                                                        `app.Bind(elsewhere, remote)`,
		`service "desk" implements a nil port`: `desk.Implement(nowhere, fixedPrice)`,
	} {
		expectProblem(t, de, "port_test.go", said, where)
	}
	// A port is refused once: loop's fallback is, loop is not unbound on top.
	if d := diagnosticSaying(de.Diagnostics, "port desk/port/loop has nothing bound"); d != nil {
		t.Errorf("a port whose fallback is refused is also said unbound: %s", d.Message)
	}
	if n := len(de.Diagnostics); n != 7 {
		t.Errorf("%d problems, want 7:\n%v", n, de)
	}
}

// diagnosticSaying is the diagnostic whose message holds said, or nil.
func diagnosticSaying(ds []model.Diagnostic, said string) *model.Diagnostic {
	for i := range ds {
		if strings.Contains(ds[i].Message, said) {
			return &ds[i]
		}
	}
	return nil
}

// lineIn is the line of file, in this package, that holds text.
func lineIn(t *testing.T, file, text string) int {
	t.Helper()
	raw, rawErr := os.ReadFile(file)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, text) {
			return i + 1
		}
	}
	t.Fatalf("%s has no line holding %q", file, text)
	return 0
}
