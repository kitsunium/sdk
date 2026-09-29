package kit_test

import (
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
