package kit_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// startReplaced runs the test product with four of its endpoints replaced:
// a search that answers what it was asked, a slow endpoint that waits for
// its end, the members' me, and the shop's count.
func startReplaced(t *testing.T) *kit.App {
	t.Helper()
	return start(t,
		kit.Replace(SearchAPI, func(_ context.Context, in SearchInput) (SearchInput, error) {
			in.Q = "replaced: " + in.Q
			return in, nil
		}),
		kit.Replace(SlowAPI, func(ctx context.Context, _ kit.EmptyValue) (kit.EmptyValue, error) {
			<-ctx.Done()
			return kit.EmptyValue{}, ctx.Err()
		}),
		kit.Replace(MeAPI, func(ctx context.Context, _ kit.EmptyValue) (Profile, error) {
			uid, _ := kit.UserID(ctx)
			return Profile{ID: string(uid), Name: "stand-in"}, nil
		}),
		kit.Replace(CountAPI, func(context.Context, kit.EmptyValue) (CountOutput, error) { return CountOutput{Items: 99}, nil }),
	)
}

// A test replaces an endpoint's code with a typed function; the request is
// still decoded as the endpoint decodes it, and the run says it was
// replaced.
func TestAReplacementGetsTheDecodedRequest(t *testing.T) {
	app := startReplaced(t)
	r := call(t, app, "GET /search?q=lamp&limit=3", noBody)
	var got SearchInput
	r.json(t, &got)
	if r.status != http.StatusOK || got.Q != "replaced: lamp" || got.Limit != 3 {
		t.Fatalf("a replaced endpoint over HTTP: %d %s", r.status, r.body)
	}
	if s := spanOf(t, traceOf(t, app, r), "shop/endpoint/Search"); s.Attrs["mock"] != model.MockReplace {
		t.Errorf("the replaced run's span: %+v", s.Attrs)
	}
	if r := call(t, app, "GET /search?limit=many", noBody); r.status != http.StatusBadRequest {
		t.Errorf("a request that does not decode reached the replacement: %d %s", r.status, r.body)
	}
}

// It replaces the code, not the mechanics: the policies wrap the
// replacement, and authentication runs before it.
func TestPoliciesAndAuthenticationStillRun(t *testing.T) {
	app := startReplaced(t)
	if r := call(t, app, "GET /slow", noBody); r.status != http.StatusGatewayTimeout {
		t.Errorf("the timeout around a replacement: %d %s", r.status, r.body)
	}
	if r := call(t, app, "GET /me", noBody); r.status != http.StatusUnauthorized {
		t.Errorf("an anonymous request to a replaced Auth endpoint: %d %s", r.status, r.body)
	}
	token := signIn(t, app, "acc_1", "ann@example.com", "Ann")
	var me Profile
	r := call(t, app, "GET /me", noBody, "Cookie", "sid="+token)
	r.json(t, &me)
	if r.status != http.StatusOK || me != (Profile{ID: "acc_1", Name: "stand-in"}) {
		t.Errorf("the replacement, as the signed-in user: %d %s", r.status, r.body)
	}
}

// In process, the same; and every replacement is listed with the mocks, with
// the runs it changed.
func TestAReplacementRunsInProcessAndIsListed(t *testing.T) {
	app := startReplaced(t)
	if out, err := CountAPI.Call(t.Context(), kit.EmptyValue{}); err != nil || out.Items != 99 {
		t.Errorf("a replaced endpoint in process: %+v %v", out, err)
	}
	hits := map[string]int64{}
	for _, m := range app.Graph().Runtime.Mocks {
		if m.Mode == model.MockReplace {
			hits[m.Node] = m.Hits
		}
	}
	if want := map[string]int64{"shop/endpoint/Search": 0, "shop/endpoint/Slow": 0, "members/endpoint/Me": 0, "shop/endpoint/Count": 1}; !maps.Equal(hits, want) {
		t.Errorf("runtime.mocks: %v, want %v", hits, want)
	}
}

// A replacement that panics is an internal error, as a handler's panic is:
// nothing of the panic reaches the caller.
func TestAPanickingReplacementIsAnInternalError(t *testing.T) {
	app := start(t, kit.Replace(SearchAPI, func(context.Context, SearchInput) (SearchInput, error) {
		panic("canary: do-not-leak-replace-4c1d")
	}))
	r := call(t, app, "GET /search?q=x", noBody)
	if r.status != http.StatusInternalServerError || r.errorCode(t) != kit.WireInternal || strings.Contains(string(r.body), "canary") {
		t.Errorf("a panicking replacement: %d %s", r.status, r.body)
	}
}

// A replaced implementation still validates its request; a replaced port
// answers in place of whatever it would call.
func TestReplaceAPortOrItsImplementation(t *testing.T) {
	start(t, kit.Replace(MemberQuoteAPI, func(_ context.Context, in QuoteInput) (Quoted, error) {
		return Quoted{Price: 7, By: "replaced " + in.ID}, nil
	}))
	if q, err := Quote.Call(t.Context(), QuoteInput{ID: "item_1"}); err != nil || q.By != "replaced item_1" {
		t.Errorf("a replaced implementation behind its port: %+v %v", q, err)
	}
	var ke *kit.Error
	if _, err := Quote.Call(t.Context(), QuoteInput{}); !errorsAs(err, &ke) || ke.Status != http.StatusBadRequest {
		t.Errorf("an invalid request reached the replacement: %v", err)
	}
}

// A port with nothing bound starts once a test replaces it: a module tests
// itself without a host.
func TestAReplacedPortNeedsNoBinding(t *testing.T) {
	module := kit.NewService("module", "A module's service, tested without its host.")
	host := module.Port[QuoteInput, Quoted]("host")
	module.Endpoint("GET /module/{id}", func(ctx context.Context, in ByID) (Quoted, error) {
		return host.Call(ctx, QuoteInput(in))
	})
	app := startApp(t, []*kit.Service{module}, kit.Replace(host, func(_ context.Context, in QuoteInput) (Quoted, error) {
		return Quoted{Price: 3, By: "the test, for " + in.ID}, nil
	}))
	r := call(t, app, "GET /module/x", noBody)
	var q Quoted
	r.json(t, &q)
	if r.status != http.StatusOK || q.By != "the test, for x" {
		t.Fatalf("a replaced port: %d %s", r.status, r.body)
	}
	if s := spanOf(t, traceOf(t, app, r), "module/port/host"); s.Attrs["mock"] != model.MockReplace {
		t.Errorf("the port's span: %+v", s.Attrs)
	}
	g := app.Graph()
	if p := g.Node("module/port/host").Port; p.Via != model.ViaReplace || p.Bound != "" {
		t.Errorf("the port's info: %+v", p)
	}
	for _, e := range g.Edges {
		if e.From == "module/port/host" {
			t.Errorf("a replaced port draws a binding: %s", e.ID)
		}
	}
}

// A replacement that cannot run refuses the start, each where it is
// written.
func TestReplaceProblemsRefuseTheStart(t *testing.T) {
	err := kit.NewApp("shop", Shop, Audit).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
		kit.Replace(CountAPI, nil),
		kit.Replace(MeAPI, func(context.Context, kit.EmptyValue) (Profile, error) { return Profile{}, nil }),
	).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	for said, where := range map[string]string{
		"kit.Replace replaces shop/endpoint/Count with a nil function":                                 `kit.Replace(CountAPI, nil)`,
		`kit.Replace replaces members/endpoint/Me, of service "members", which the app does not mount`: `kit.Replace(MeAPI, func(context.Context, kit.EmptyValue) (Profile, error) { return Profile{}, nil })`,
	} {
		d := diagnosticSaying(de.Diagnostics, said)
		if d == nil {
			t.Errorf("no problem says %q:\n%v", said, err)
			continue
		}
		if line := lineIn(t, "replace_test.go", where); d.Source == nil || d.Source.File != "framework/internal/kit/replace_test.go" || d.Source.Line != line {
			t.Errorf("%q is said at %+v, want kit/replace_test.go:%d", said, d.Source, line)
		}
	}
}
