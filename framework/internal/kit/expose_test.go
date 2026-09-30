package kit_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// exposureCase is a request to an exposure and what it gets.
type exposureCase struct {
	method, path string
	body         any
	headers      []string
	status       int
	code         string
}

// An exposure is an endpoint: routed, decoded as strictly as any, its
// authentication the operation's, the rest of the pipeline the operation's
// own.
func TestAnExposureRefusesAsItsOperation(t *testing.T) {
	app := startCounter(t)
	placed := place(t, "alice", "tea")
	for _, c := range []exposureCase{
		{"POST", "/orders", CounterCart{Lines: []string{"tea"}}, nil, http.StatusUnauthorized, kit.WireUnauth},
		{"POST", "/orders", CounterCart{Lines: []string{"tea"}}, badge("guest"), http.StatusForbidden, kit.WireForbidden},
		{"POST", "/orders", CounterCart{}, badge("alice"), http.StatusBadRequest, kit.WireInvalid},
		{"POST", "/orders", `{"lines":["tea"],"price":0}`, badge("alice"), http.StatusBadRequest, kit.WireInvalid},
		{"POST", "/orders/" + placed.ID + "/cancel", nil, badge("bob"), http.StatusNotFound, kit.WireNotFound},
		{"POST", "/reindex", nil, badge("alice"), http.StatusForbidden, kit.WireForbidden},
	} {
		r := call(t, app, c.method+" "+c.path, c.body, c.headers...)
		if r.status != c.status || r.errorCode(t) != c.code {
			t.Errorf("%s %s %v: %d %s, want %d %s", c.method, c.path, c.headers, r.status, r.body, c.status, c.code)
		}
	}
}

// An exposure answers 200 with the result, 204 for Empty, and 202 for a
// queued command.
func TestAnExposureAnswersAsItsOperation(t *testing.T) {
	app := startCounter(t)
	r := call(t, app, "POST /orders", CounterCart{Lines: []string{"tea"}}, badge("alice")...)
	var placed CounterPlaced
	if r.json(t, &placed); r.status != http.StatusOK || placed.ID == "" {
		t.Fatalf("placing an order over HTTP: %d %s", r.status, r.body)
	}
	var mine []CounterOrder
	listed := call(t, app, "GET /orders", noBody, badge("alice")...)
	if listed.json(t, &mine); listed.status != http.StatusOK || len(mine) != 1 {
		t.Errorf("alice's orders over HTTP: %d %s", listed.status, listed.body)
	}
	if r := call(t, app, "POST /orders/"+placed.ID+"/cancel", noBody, badge("alice")...); r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Errorf("alice cancels, and the command answers Empty: %d %s", r.status, r.body)
	}
	if r := call(t, app, "POST /reindex", noBody, badge("root")...); r.status != http.StatusAccepted || len(r.body) != 0 {
		t.Errorf("a queued command, exposed: %d %s", r.status, r.body)
	}
}

// An exposure is named after its operation, says what it exposes, and
// draws a declared edge to it; its request's span leads to the dispatch.
func TestAnExposureIsNamedAfterItsOperation(t *testing.T) {
	app := startCounter(t)
	g := app.Graph()
	ep := g.Node("counter/endpoint/place-order").Endpoint
	type route struct{ method, path, exposes, auth string }
	if got := (route{ep.Method, ep.Path, ep.Exposes, ep.Auth}); got != (route{"POST", "/orders", "counter/command/place-order", model.AuthRequired}) {
		t.Errorf("the exposure's node: %+v", ep)
	}
	for _, id := range []string{"counter/endpoint/place-order|dispatches|counter/command/place-order", "counter/endpoint/my-orders|asks|counter/query/my-orders"} {
		if e := g.Edge(id); e == nil || !e.Declared {
			t.Errorf("%s: %+v", id, e)
		}
	}
	r := call(t, app, "POST /reindex", noBody, badge("root")...)
	if got := hopOfNode(t, traceOf(t, app, r), "counter/command/reindex"); got.From != "counter/endpoint/reindex" || got.Edge != model.EdgeDispatches || got.Op != model.OpDispatch {
		t.Errorf("the exposure's dispatch: %+v", got)
	}
}

// doors are exposures that say who may run their operation: a permission,
// a rule — given after Expose too, as a chain may —, or a marker that the
// exposure is open on purpose, to anyone or to any signed-in user.
func doors() *kit.Service {
	s := kit.NewService("doors", "Exposures that say who may run them.")
	pass := func(context.Context, LabInput) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil }
	rule := func(context.Context, LabInput) error { return nil }
	s.Command("allowed", pass).Allow(CounterPolicy, "place", "order").Expose("POST /doors/allowed")
	s.Query("ruled", pass).Authorize(rule).Expose("POST /doors/ruled")
	s.Command("later", pass).Expose("POST /doors/later").Authorize(rule)
	s.Command("public", pass).Expose("POST /doors/public", kit.Anyone())
	s.Query("greeting", pass, kit.AuthOptional()).Expose("POST /doors/greeting", kit.Anyone())
	s.Query("mine", pass, kit.Auth()).Expose("POST /doors/mine", kit.AnyUser(), kit.RateLimitPerClient(100, 100))
	s.Command("queued", pass, kit.Queued()).Expose("POST /doors/queued", kit.Anyone())
	return s
}

// An operation reached over HTTP says who may run it: an exposure whose
// operation declares neither a permission nor a rule, and that does not say
// it is open on purpose — or says it wrongly —, refuses the start at its
// line, in French too; one that says it passes.
func TestAnExposureSaysWhoMayRunIt(t *testing.T) {
	s := kit.NewService("walls", "Exposures that do not say who may run them, or say it wrong.")
	pass := func(context.Context, LabInput) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil }
	rule := func(context.Context, LabInput) error { return nil }
	s.Command("open", pass).Expose("POST /walls/open")
	s.Query("signed", pass, kit.Auth()).Expose("POST /walls/signed")
	s.Command("stranger", pass, kit.Auth()).Expose("POST /walls/stranger", kit.Anyone())
	s.Query("anonymous", pass).Expose("POST /walls/anonymous", kit.AnyUser())
	s.Query("optional", pass, kit.AuthOptional()).Expose("POST /walls/optional", kit.AnyUser())
	s.Command("marked", pass).Authorize(rule).Expose("POST /walls/marked", kit.Anyone())
	s.Command("both", pass).Expose("POST /walls/both", kit.Anyone(), kit.AnyUser())
	s.Command("background", pass, kit.Queued()).Expose("POST /walls/background")
	de := startRefused(t, kit.NewApp("walls", Staff, s, doors()))
	for said, where := range map[string]string{
		"the exposure of walls/command/open does not say who may run it":                                           `Expose("POST /walls/open")`,
		"the exposure of walls/query/signed does not say who may run it":                                           `Expose("POST /walls/signed")`,
		"the exposure of walls/command/background does not say who may run it":                                     `Expose("POST /walls/background")`,
		"the exposure of walls/command/stranger is marked kit.Anyone(), but the operation requires a user":         `Expose("POST /walls/stranger", kit.Anyone())`,
		"the exposure of walls/query/anonymous is marked kit.AnyUser(), but the operation does not require a user": `Expose("POST /walls/anonymous", kit.AnyUser())`,
		"the exposure of walls/query/optional is marked kit.AnyUser(), but the operation does not require a user":  `Expose("POST /walls/optional", kit.AnyUser())`,
		"the exposure of walls/command/marked is marked kit.Anyone(), but the operation says who may run it":       `Expose("POST /walls/marked", kit.Anyone())`,
		"the exposure of walls/command/both is marked both kit.Anyone() and kit.AnyUser()":                         `Expose("POST /walls/both", kit.Anyone(), kit.AnyUser())`,
	} {
		expectProblem(t, de, "expose_test.go", said, where)
	}
	if n := len(de.Diagnostics); n != 8 {
		t.Errorf("%d problems, want 8 — none of the doors':\n%v", n, de)
	}
}

// An exposure open on purpose says so on its node, and adds no
// authentication: kit.AnyUser still takes the user its operation asks for,
// and kit.Anyone lets a caller without one through.
func TestAnExposureOpenOnPurposeSaysSo(t *testing.T) {
	app := startApp(t, []*kit.Service{Staff, doors()})
	g := app.Graph()
	for id, want := range map[string]string{
		"doors/endpoint/allowed": "", "doors/endpoint/ruled": "", "doors/endpoint/later": "",
		"doors/endpoint/public": model.AccessAnyone, "doors/endpoint/greeting": model.AccessAnyone,
		"doors/endpoint/mine": model.AccessAnyUser, "doors/endpoint/queued": model.AccessAnyone,
	} {
		if n := g.Node(id); n == nil || n.Endpoint == nil || n.Endpoint.Access != want {
			t.Errorf("%s says access %+v, want %q", id, n, want)
		}
	}
	for _, c := range []exposureCase{
		{"POST", "/doors/public", LabInput{Key: "k"}, nil, http.StatusNoContent, ""},
		{"POST", "/doors/greeting", LabInput{Key: "k"}, nil, http.StatusNoContent, ""},
		{"POST", "/doors/queued", LabInput{Key: "k"}, nil, http.StatusAccepted, ""},
		{"POST", "/doors/mine", LabInput{Key: "k"}, nil, http.StatusUnauthorized, kit.WireUnauth},
		{"POST", "/doors/mine", LabInput{Key: "k"}, badge("guest"), http.StatusNoContent, ""},
		{"POST", "/doors/allowed", LabInput{Key: "k"}, badge("guest"), http.StatusForbidden, kit.WireForbidden},
		{"POST", "/doors/allowed", LabInput{Key: "k"}, badge("alice"), http.StatusNoContent, ""},
	} {
		r := call(t, app, c.method+" "+c.path, c.body, c.headers...)
		code := ""
		if r.status >= http.StatusBadRequest {
			code = r.errorCode(t)
		}
		if r.status != c.status || code != c.code {
			t.Errorf("%s %s %v: %d %s, want %d %s", c.method, c.path, c.headers, r.status, r.body, c.status, c.code)
		}
	}
}
