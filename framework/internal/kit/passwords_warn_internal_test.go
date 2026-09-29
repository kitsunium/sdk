package kit

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
)

// passwordChange asks for an account's new password.
type passwordChange struct {
	ID   string `path:"id"`
	Next string `json:"next" kit:"secret"`
}

// warnedApp runs a store under a policy and four endpoints: one that sets
// passwords with no rate limit, one behind a rate limit, one that only
// verifies — each called once, so that the run draws their edges — and one
// whose edge only the static analysis finds.
func warnedApp(t *testing.T) (*App, *model.Graph) {
	t.Helper()
	svc := NewService("locks-warn", "")
	accounts := svc.Store("accounts", func(a lockAccount) string { return a.ID })
	policy := accounts.Passwords(func(a *lockAccount) *string { return &a.Password })
	set := func(ctx context.Context, in passwordChange) (EmptyValue, error) {
		return EmptyValue{}, policy.Set(ctx, in.ID, []byte(in.Next))
	}
	check := func(ctx context.Context, in passwordChange) (bool, error) {
		return policy.Verify(ctx, in.ID, []byte(in.Next))
	}
	calls := []interface {
		Call(context.Context, passwordChange) (EmptyValue, error)
	}{svc.Endpoint("POST /open/{id}", set), svc.Endpoint("POST /guarded/{id}", set, RateLimitPerClient(1, 5))}
	verify := svc.Endpoint("POST /check/{id}", check)
	svc.Endpoint("POST /static/{id}", func(context.Context, passwordChange) (EmptyValue, error) { return EmptyValue{}, nil })
	app := NewApp("warn", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	in := passwordChange{ID: "a1", Next: string(pw("one"))}
	must(t, accounts.Insert(t.Context(), lockAccount{ID: "a1"}))
	for _, e := range calls {
		_, err := e.Call(t.Context(), in)
		must(t, err)
	}
	_, err := verify.Call(t.Context(), in)
	must(t, err)
	static := &model.Graph{Edges: []model.Edge{{From: "locks-warn/endpoint/POST /static/{id}", To: "locks-warn/store/accounts", Kind: model.EdgeWrites, Label: policyLabel}}}
	app.mu.Lock()
	app.static = static
	app.mu.Unlock()
	return app, static
}

// In dev, an endpoint that sets or changes a password with no rate limit is
// warned of — found by what a run drew, or by the static analysis —, one
// with a rate limit is not, nor one that only verifies; outside dev, kit
// says nothing.
func TestAPasswordChangeWithNoRateLimitIsWarnedOf(t *testing.T) {
	useFakeHashing(t)
	app, static := warnedApp(t)
	var warned []string
	for _, d := range app.Graph().Diagnostics {
		if d.Severity != "warning" || !strings.Contains(d.Message, "with no rate limit") {
			continue
		}
		if d.Source == nil || d.Texts["fr"] == "" {
			t.Errorf("a warning with no position or no French: %+v", d)
		}
		warned = append(warned, d.Node)
	}
	slices.Sort(warned)
	if want := []string{"locks-warn/endpoint/POST /open/{id}", "locks-warn/endpoint/POST /static/{id}"}; !slices.Equal(warned, want) {
		t.Errorf("warned of %v, want %v", warned, want)
	}
	prod := &App{cfg: config{env: EnvProduction}}
	if got := prod.passwordWarnings(app.Graph(), static); got != nil {
		t.Errorf("outside dev: %v", got)
	}
}
