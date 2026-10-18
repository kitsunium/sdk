package app_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// With the members, the quote has one implementation among the mounted
// services: the port calls it, in its own span under the port's.
func TestAPortCallsItsImplementation(t *testing.T) {
	a := start(t)
	it := create(t, a, "lamp", 1)
	q, r := quote(t, a, it.ID)
	if q != (Quoted{Price: 10, By: "guest"}) {
		t.Fatalf("the implementation's answer: %+v", q)
	}
	tr := traceOf(t, a, r)
	port := spanOf(t, tr, "shop/port/quote")
	if port.From != "shop/endpoint/QuoteItem" {
		t.Errorf("the port's span: %+v", port)
	}
	if got := hopOf(spanOf(t, tr, "members/endpoint/MemberPrice")); got != (hop{From: "shop/port/quote", Edge: model.EdgeCalls, Op: model.OpCall, Parent: port.SpanID}) {
		t.Errorf("the implementation's span: %+v, under the port's %s", got, port.SpanID)
	}
	g := a.Graph()
	expectBinding(t, g, "shop/port/quote", model.PortInfo{Fallback: "shop/endpoint/ListPrice", Bound: "members/endpoint/MemberPrice", Via: model.ViaImplement})
	type route struct{ method, path, expose, implements string }
	ep := g.Node("members/endpoint/MemberPrice").Endpoint
	if got := (route{ep.Method, ep.Path, ep.Expose, ep.Implements}); got != (route{expose: model.ExposePrivate, implements: "shop/port/quote"}) {
		t.Errorf("the implementation has a route, or does not say what it implements: %+v", ep)
	}
}

// The caller's user goes through the port to what it calls, as through
// Endpoint.Call: both spans carry it.
func TestAPortCarriesItsCallersUser(t *testing.T) {
	a := start(t)
	it := create(t, a, "lamp", 1)
	ctx := app.WithUser(t.Context(), "u-ann", Who{Name: "Ann"})
	if q, err := Quote.Call(ctx, QuoteInput{ID: it.ID}); err != nil || q != (Quoted{Price: 9, By: "u-ann"}) {
		t.Fatalf("a member's quote: %+v %v", q, err)
	}
	var traces []model.Trace
	call(t, a, "GET /_kit/api/traces?node=members/endpoint/MemberPrice", noBody).json(t, &traces)
	for _, tr := range traces {
		if carries(tr, "u-ann", "shop/port/quote", "members/endpoint/MemberPrice") {
			return
		}
	}
	t.Errorf("no trace carries the user through the port: %+v", traces)
}

// carries reports whether every node's span in tr acts for user.
func carries(tr model.Trace, user string, nodes ...string) bool {
	users := map[string]string{}
	for _, s := range tr.Spans {
		users[s.Node] = s.User
	}
	for _, n := range nodes {
		if users[n] != user {
			return false
		}
	}
	return true
}

// The implementation validates what it is given, as any endpoint does.
func TestAnImplementationValidatesItsRequest(t *testing.T) {
	start(t)
	_, err := Quote.Call(t.Context(), QuoteInput{})
	if ke := expectInvalid(t, err, "an invalid request to the implementation"); ke != nil && len(ke.Violations) != 1 {
		t.Errorf("the violations: %+v", ke.Violations)
	}
}

// One rule decides what a port calls: app.Bind, else the one implementation
// among the services the app mounts, else the fallback.
func TestBindWinsOverImplementWhichWinsOverFallback(t *testing.T) {
	pricing := app.NewService("pricing", "Prices set by hand.")
	fixed := pricing.Query("fixed", fixedPrice)
	for _, c := range []struct {
		name     string
		services []*app.Service
		opts     []app.AppConfigurer
		by, via  string
	}{
		{"the fallback, alone", []*app.Service{Shop, Audit}, nil, "list", model.ViaFallback},
		{"an implementation over the fallback", []*app.Service{Shop, Audit, Members}, nil, "guest", model.ViaImplement},
		{"a binding over both", []*app.Service{Shop, Audit, Members, pricing}, []app.AppConfigurer{app.Bind(Quote, fixed)}, "fixed", model.ViaBind},
		{"the last binding", []*app.Service{Shop, Audit, Members, pricing}, []app.AppConfigurer{app.Bind(Quote, fixed), app.Bind(Quote, ListPriceAPI)}, "list", model.ViaBind},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := startApp(t, c.services, c.opts...)
			it := create(t, a, "lamp", 1)
			if q, _ := quote(t, a, it.ID); q.By != c.by {
				t.Errorf("answered by %q, want %q", q.By, c.by)
			}
			if p := a.Graph().Node("shop/port/quote").Port; p.Via != c.via {
				t.Errorf("via %q, want %q", p.Via, c.via)
			}
		})
	}
}

// Two implementations and no binding: the start will not guess, and names
// both; a binding chooses.
func TestTwoImplementationsNeedABinding(t *testing.T) {
	rival := app.NewService("rival", "Another price for the shop.")
	rivalPrice := rival.Implement(Quote, fixedPrice)
	err := app.NewApp("shop", Shop, Audit, Members, rival).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard)).Start(t.Context())
	var de *app.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	expectProblem(t, de, "product_test.go",
		"port shop/port/quote has several implementations, members/endpoint/MemberPrice and rival/endpoint/fixedPrice, and no app.Bind",
		"var Quote = Shop.Port")
	a := startApp(t, []*app.Service{Shop, Audit, Members, rival}, app.Bind(Quote, rivalPrice))
	it := create(t, a, "lamp", 1)
	if q, _ := quote(t, a, it.ID); q.By != "fixed" {
		t.Errorf("the chosen implementation's answer: %+v", q)
	}
}

type pricer struct{}

// Price is a method value's price.
func (pricer) Price(context.Context, QuoteInput) (Quoted, error) { return Quoted{By: "method"}, nil }

// An implementation is named like every endpoint: app.Name, else its
// handler's name, else — a function literal — its port's name.
func TestAnImplementationIsNamedLikeAnEndpoint(t *testing.T) {
	desk := app.NewService("naming-desk", "")
	check := desk.Port[QuoteInput, Quoted]("price-check")
	impl := app.NewService("naming-impl", "")
	literal := func(context.Context, QuoteInput) (Quoted, error) { return Quoted{}, nil }
	for got, want := range map[string]string{
		impl.Implement(check, fixedPrice).ID():                     "naming-impl/endpoint/fixedPrice",
		impl.Implement(check, pricer{}.Price).ID():                 "naming-impl/endpoint/Price",
		impl.Implement(check, fixedPrice, app.Name("chosen")).ID(): "naming-impl/endpoint/chosen",
		impl.Implement(check, literal).ID():                        "naming-impl/endpoint/price-check",
	} {
		if got != want {
			t.Errorf("an implementation named %q, want %q", got, want)
		}
	}
}
