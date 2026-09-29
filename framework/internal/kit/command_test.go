package kit_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// startCounter runs the counter, its staff and the lab as one app for one test.
func startCounter(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	return startApp(t, []*kit.Service{Staff, Counter, Lab}, opts...)
}

// as is ctx acting as a badge's holder, as a job or a test acts.
func as(ctx context.Context, badge string) context.Context {
	return kit.WithUser(ctx, kit.UID(badge), staffBadges[badge])
}

// badge is the header a request shows a badge with.
func badge(token string) []string { return []string{"X-Badge", token} }

// expectRefused checks that err is the kit.Error of status and code.
func expectRefused(t *testing.T, err error, status int, code, what string) {
	t.Helper()
	var ke *kit.Error
	if !errorsAs(err, &ke) || ke.Status != status || ke.Code != code {
		t.Errorf("%s: %v, want %d %s", what, err, status, code)
	}
}

// place places an order for a badge's holder, in process.
func place(t *testing.T, holder string, lines ...string) CounterPlaced {
	t.Helper()
	placed, err := CounterPlace.Dispatch(as(t.Context(), holder), CounterCart{Lines: lines})
	if err != nil || placed.ID == "" {
		t.Fatalf("%s places an order: %+v %v", holder, placed, err)
	}
	return placed
}

// A command is dispatched in process, and its result comes back to the
// caller: the order is placed for the user the dispatch carried.
func TestACommandIsDispatched(t *testing.T) {
	startCounter(t)
	placed := place(t, "alice", "tea")
	if o, err := CounterOrders.Get(t.Context(), placed.ID); err != nil || o.Customer != "alice" {
		t.Errorf("the order placed: %+v %v", o, err)
	}
}

// A command runs its pipeline in order: the caller's user — implied by a
// permission —, the input's validate tags, the permission, the handler.
func TestACommandRunsItsPipeline(t *testing.T) {
	startCounter(t)
	_, err := CounterPlace.Dispatch(t.Context(), CounterCart{Lines: []string{"tea"}})
	expectRefused(t, err, http.StatusUnauthorized, kit.WireUnauth, "a dispatch without a user, a permission implying one")
	_, err = CounterPlace.Dispatch(as(t.Context(), "alice"), CounterCart{})
	if ke := expectInvalid(t, err, "an empty cart"); ke != nil && (len(ke.Violations) != 1 || ke.Violations[0].Path != "lines") {
		t.Errorf("the violations: %+v", ke.Violations)
	}
	for _, who := range []string{"guest", "root"} {
		_, err = CounterPlace.Dispatch(as(t.Context(), who), CounterCart{Lines: []string{"tea"}})
		expectRefused(t, err, http.StatusForbidden, kit.WireForbidden, who+", whom the policy does not let place an order")
	}
}

// A query is asked, and answers what its caller may read.
func TestAQueryIsAsked(t *testing.T) {
	startCounter(t)
	placed := place(t, "alice", "tea", "cake")
	mine, err := CounterMine.Ask(as(t.Context(), "alice"), kit.EmptyValue{})
	if err != nil || len(mine) != 1 || mine[0].ID != placed.ID {
		t.Errorf("alice's orders: %+v %v", mine, err)
	}
	if o, err := CounterGet.Ask(as(t.Context(), "alice"), CounterByID(placed)); err != nil || len(o.Lines) != 2 {
		t.Errorf("alice reads her order: %+v %v", o, err)
	}
}

// A rule that needs the data hides what exists from a stranger, and lets
// its owner through.
func TestARuleNeedsTheData(t *testing.T) {
	startCounter(t)
	placed := place(t, "alice", "tea")
	_, err := CounterGet.Ask(as(t.Context(), "bob"), CounterByID(placed))
	expectRefused(t, err, http.StatusNotFound, kit.WireNotFound, "bob reads alice's order")
	_, err = CounterCancel.Dispatch(as(t.Context(), "bob"), CounterByID(placed))
	expectRefused(t, err, http.StatusNotFound, kit.WireNotFound, "bob cancels alice's order")
	if _, err := CounterCancel.Dispatch(as(t.Context(), "alice"), CounterByID(placed)); err != nil {
		t.Fatalf("alice cancels her order: %v", err)
	}
	if o, err := CounterOrders.Get(t.Context(), placed.ID); err != nil || !o.Canceled {
		t.Error("the order was not canceled")
	}
}

// Whatever a rule refuses with, the caller reads the one sentence: a denial,
// an abstention and a fault look alike from the other side.
func TestARefusalSaysOneSentence(t *testing.T) {
	startCounter(t)
	for _, q := range []*kit.Query[LabInput, int]{LabHidden, LabBroken} {
		_, err := q.Ask(t.Context(), LabInput{Key: "x"})
		expectRefused(t, err, http.StatusForbidden, kit.WireForbidden, q.ID())
		if err != nil && strings.Contains(err.Error(), "canary") {
			t.Errorf("%s told the caller why: %v", q.ID(), err)
		}
	}
}

// expectAnswer checks what a request to an exposure answers: its status, and
// its error's code when it is one.
func expectAnswer(t *testing.T, r response, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.errorCode(t) != code) {
		t.Errorf("%d %s, want %d %s", r.status, r.body, status, code)
	}
}

// The policies an endpoint takes are a query's too: a rate limit.
func TestAnOperationIsRateLimited(t *testing.T) {
	// A bucket of its own: the one of a package-level declaration would
	// stay empty for the next run of the test.
	limits := kit.NewService("limits", "A query with a rate limit.")
	limits.Query("limited", func(context.Context, LabInput) (int, error) { return 1, nil }, kit.RateLimit(0.001, 1)).
		Expose("POST /lab/limited")
	app := startApp(t, []*kit.Service{Staff, Counter, Lab, limits})
	expectAnswer(t, call(t, app, "POST /lab/limited", LabInput{Key: "x"}), http.StatusOK, "")
	expectAnswer(t, call(t, app, "POST /lab/limited", LabInput{Key: "x"}), http.StatusTooManyRequests, kit.WireRateLimited)
}

// A command past its timeout answers 504; a query past its bulkhead 503.
//
// Goroutine lifecycle: one goroutine makes the first call and reports on a
// buffered channel; the release lets it end, and the test waits for it.
func TestAnOperationHasATimeoutAndABulkhead(t *testing.T) {
	app := startCounter(t)
	labSlow.hold()
	defer labSlow.release()
	expectAnswer(t, call(t, app, "POST /lab/slow", LabInput{}), http.StatusGatewayTimeout, kit.WireTimeout)
	labCrowd.hold()
	done := make(chan response, 1)
	go func() { done <- call(t, app, "POST /lab/crowded", LabInput{}) }()
	eventually(t, "the first question in the bulkhead", func() bool { return labCrowd.running.Load() == 1 })
	expectAnswer(t, call(t, app, "POST /lab/crowded", LabInput{}), http.StatusServiceUnavailable, kit.WireUnavailable)
	labCrowd.release()
	expectAnswer(t, <-done, http.StatusNoContent, "")
}

// A panic is an internal error that says nothing of it, over HTTP and in
// process.
func TestAPanickingCommandIsAnInternalError(t *testing.T) {
	app := startCounter(t)
	r := call(t, app, "POST /lab/panics", LabInput{})
	if expectAnswer(t, r, http.StatusInternalServerError, kit.WireInternal); strings.Contains(string(r.body), "canary") {
		t.Errorf("the panic reached the caller: %s", r.body)
	}
	if _, err := LabPanics.Dispatch(t.Context(), LabInput{}); statusOf(err) != http.StatusInternalServerError {
		t.Errorf("a panicking command, in process: %v", err)
	}
}

// A port bound to a command dispatches it: the binding is a dispatches
// edge, and a call through the port runs the command's whole pipeline.
func TestAPortBoundToACommandDispatchesIt(t *testing.T) {
	module := kit.NewService("module", "A module's service, whose port the counter answers.")
	placer := module.Port[CounterCart, CounterPlaced]("placer")
	app := startApp(t, []*kit.Service{Staff, Counter, Lab, module}, kit.Bind(placer, CounterPlace))
	if _, err := placer.Call(as(t.Context(), "alice"), CounterCart{Lines: []string{"tea"}}); err != nil {
		t.Fatalf("a call through the port: %v", err)
	}
	_, err := placer.Call(as(t.Context(), "guest"), CounterCart{Lines: []string{"tea"}})
	expectRefused(t, err, http.StatusForbidden, kit.WireForbidden, "a guest through the port")
	if e := app.Graph().Edge("module/port/placer|dispatches|counter/command/place-order"); e == nil || !e.Declared {
		t.Errorf("the port's binding: %+v", e)
	}
}

// A test replaces a command's handler, and the pipeline still runs: the
// input is validated, the permission checked.
func TestAReplacedCommandKeepsItsPipeline(t *testing.T) {
	startCounter(t, kit.Replace(CounterPlace, func(context.Context, CounterCart) (CounterPlaced, error) {
		return CounterPlaced{ID: "replaced"}, nil
	}))
	if p, err := CounterPlace.Dispatch(as(t.Context(), "alice"), CounterCart{Lines: []string{"tea"}}); err != nil || p.ID != "replaced" {
		t.Errorf("the replacement: %+v %v", p, err)
	}
	_, err := CounterPlace.Dispatch(as(t.Context(), "alice"), CounterCart{})
	expectInvalid(t, err, "an invalid cart, the handler replaced")
	_, err = CounterPlace.Dispatch(as(t.Context(), "guest"), CounterCart{Lines: []string{"tea"}})
	expectRefused(t, err, http.StatusForbidden, kit.WireForbidden, "a guest, the handler replaced")
}

// An operation that asks for a user in an app with no auth handler refuses
// the start, as an endpoint does.
func TestAnOperationAskingForAUserNeedsAnAuthHandler(t *testing.T) {
	lonely := kit.NewService("lonely", "An operation asking for a user, and no one to say who.")
	lonely.Query("who", func(context.Context, kit.EmptyValue) (int, error) { return 1, nil }, kit.Auth())
	err := kit.NewApp("lonely", lonely).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "lonely/query/who asks for a user, but the app mounts no auth handler") {
		t.Errorf("an operation asking for a user, without an auth handler: %v", err)
	}
}

// Auth data that gives the SDK's authz no attributes is warned of: every
// Allow refuses.
func TestAuthDataThatIsNoPrincipalIsWarnedOf(t *testing.T) {
	strict := kit.NewService("strict", "A permission the auth data cannot give.")
	strict.Command("guarded", func(context.Context, kit.EmptyValue) (int, error) { return 1, nil }).Allow(CounterPolicy, "run", "thing")
	app := startApp(t, []*kit.Service{strict, Members})
	for _, d := range app.Graph().Diagnostics {
		if d.Severity == "warning" && strings.Contains(d.Message, "does not implement kit.Principal") && strings.Contains(d.Message, "strict/command/guarded") {
			return
		}
	}
	t.Errorf("no warning that every Allow refuses: %+v", app.Graph().Diagnostics)
}

// hopOfNode is how the span of node in tr came to it.
func hopOfNode(t *testing.T, tr model.Trace, node string) hop {
	t.Helper()
	return hopOf(spanOf(t, tr, node))
}
