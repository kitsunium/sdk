package kit_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// setCookies parses the Set-Cookie headers of a response.
func setCookies(t *testing.T, r response) []*http.Cookie {
	t.Helper()
	var out []*http.Cookie
	for _, line := range r.header.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("Set-Cookie %q: %v", line, err)
		}
		out = append(out, c)
	}
	return out
}

// signIn creates an account and logs it in; it returns the session token.
func signIn(t *testing.T, app *kit.App, id, email, name string) string {
	t.Helper()
	if err := Accounts.Put(t.Context(), Account{ID: id, Email: email, Name: name}); err != nil {
		t.Fatal(err)
	}
	r := call(t, app, "POST /login", LoginInput{Email: email})
	if r.status != http.StatusOK {
		t.Fatalf("login: %d %s", r.status, r.body)
	}
	for _, c := range setCookies(t, r) {
		if c.Name == "sid" {
			return c.Value
		}
	}
	t.Fatalf("login set no session cookie: %v", r.header)
	return ""
}

func TestAuthRequiredEndpoints(t *testing.T) {
	app := start(t)
	r := call(t, app, "GET /me", noBody)
	if r.status != http.StatusUnauthorized || r.errorCode(t) != kit.WireUnauth {
		t.Fatalf("no credentials: %d %s", r.status, r.body)
	}
	token := signIn(t, app, "acc_ada", "ada@x.dev", "Ada")

	var me Profile
	r = call(t, app, "GET /me", noBody, "Cookie", "sid="+token, "User-Agent", "kit-test/1.0")
	r.json(t, &me)
	if r.status != http.StatusOK || me.ID != "acc_ada" || me.Name != "Ada" || me.IP != "127.0.0.1" || me.Agent != "kit-test/1.0" {
		t.Fatalf("with the cookie: %d %s", r.status, r.body)
	}
	// The handler slid the cookie: the response carries it again.
	if cs := setCookies(t, r); len(cs) != 1 || cs[0].Value != token || cs[0].MaxAge != 3600 {
		t.Errorf("the sliding cookie: %+v", cs)
	}
	r = call(t, app, "GET /me", noBody, "Authorization", "Bearer "+token)
	r.json(t, &me)
	if r.status != http.StatusOK || me.ID != "acc_ada" {
		t.Fatalf("with a bearer token: %d %s", r.status, r.body)
	}
	if len(setCookies(t, r)) != 0 {
		t.Error("a bearer request got a cookie")
	}

	// A credential that does not resolve is refused by the handler's error,
	// and the stale cookie is cleared with the 401.
	r = call(t, app, "GET /me", noBody, "Cookie", "sid=tok_forged")
	if r.status != http.StatusUnauthorized || !strings.Contains(string(r.body), "the session is not valid") {
		t.Fatalf("a forged session: %d %s", r.status, r.body)
	}
	if cs := setCookies(t, r); len(cs) != 1 || cs[0].Name != "sid" || cs[0].MaxAge >= 0 {
		t.Errorf("the stale cookie was not cleared: %+v", cs)
	}

	// Logout: one Set-Cookie for sid — the clearing one, set after the
	// handler's slide — and the session is gone.
	r = call(t, app, "POST /logout", noBody, "Cookie", "sid="+token)
	if r.status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", r.status, r.body)
	}
	if cs := setCookies(t, r); len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Errorf("logout cookies: %+v", cs)
	}
	if r := call(t, app, "GET /me", noBody, "Cookie", "sid="+token); r.status != http.StatusUnauthorized {
		t.Errorf("after logout: %d", r.status)
	}
}

func TestAuthOptionalEndpoints(t *testing.T) {
	app := start(t)
	var g Greeting
	r := call(t, app, "GET /greeting", noBody, "Cookie", "theme=dark")
	r.json(t, &g)
	if r.status != http.StatusOK || g.Text != "hello, stranger" || g.Theme != "dark" {
		t.Fatalf("anonymous: %d %s", r.status, r.body)
	}
	token := signIn(t, app, "acc_bob", "bob@x.dev", "Bob")
	r = call(t, app, "GET /greeting", noBody, "Cookie", "sid="+token)
	g = Greeting{}
	r.json(t, &g)
	if g.Text != "hello, Bob" || g.Theme != "" {
		t.Fatalf("signed in: %s", r.body)
	}
	// A credential that fails is refused even where anonymous callers are
	// served: the handler said no.
	if r := call(t, app, "GET /greeting", noBody, "Cookie", "sid=tok_forged"); r.status != http.StatusUnauthorized {
		t.Fatalf("a forged session on an optional endpoint: %d", r.status)
	}
	// A panicking handler is an internal error that says nothing.
	r = call(t, app, "GET /greeting", noBody, "Authorization", "Bearer panic")
	if r.status != http.StatusInternalServerError || strings.Contains(string(r.body), "do-not-leak") {
		t.Fatalf("a panicking handler: %d %s", r.status, r.body)
	}
}

// An in-process call carries its caller's user; without one, an Auth()
// endpoint refuses it before running.
func TestCallPropagatesTheUser(t *testing.T) {
	start(t)
	_, err := WhoAPI.Call(t.Context(), kit.EmptyValue{})
	var ke *kit.Error
	if !errors.As(err, &ke) || ke.Status != http.StatusUnauthorized {
		t.Fatalf("a call without a user: %v", err)
	}
	ctx := kit.WithUser(t.Context(), "acc_eve", Who{Name: "Eve"})
	out, err := WhoAPI.Call(ctx, kit.EmptyValue{})
	if err != nil || out.ID != "acc_eve" || out.Name != "Eve" {
		t.Fatalf("a call on Eve's behalf: %+v %v", out, err)
	}
	if uid, ok := kit.UserID(ctx); !ok || uid != "acc_eve" {
		t.Errorf("UserID = %q %v", uid, ok)
	}
	if _, ok := kit.AuthData[string](ctx); ok {
		t.Error("AuthData of the wrong type answered")
	}
	if _, ok := kit.UserID(kit.WithUser(t.Context(), "", Who{})); ok {
		t.Error("an empty user is a user")
	}
}

// The auth handler runs in its own span, on its node, naming the endpoint
// it guards — and draws no edge from it: only what it does is drawn.
func TestAuthSpansDrawNoEdge(t *testing.T) {
	app := start(t)
	token := signIn(t, app, "acc_cy", "cy@x.dev", "Cy")
	call(t, app, "GET /me", noBody, "Cookie", "sid="+token)
	r := call(t, app, "GET /_kit/api/traces?root=members/endpoint/Me", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 1 {
		t.Fatalf("traces of /me: %s", r.body)
	}
	var auth *model.Span
	for i, s := range traces[0].Spans {
		if s.Op == model.OpAuth {
			auth = &traces[0].Spans[i]
		}
	}
	if auth == nil || auth.Node != "members/auth/session" || auth.From != "members/endpoint/Me" || auth.Edge != "" || auth.Name != "session" {
		t.Fatalf("the auth span: %+v", auth)
	}
	g := app.Graph()
	for _, e := range g.Edges {
		if e.To == "members/auth/session" {
			t.Errorf("an edge reaches the auth handler: %s", e.ID)
		}
	}
	if e := g.Edge("members/auth/session|reads|members/store/sessions"); e == nil || e.Observed == nil {
		t.Error("what the handler does is not drawn from its node")
	}
	if n := g.Node("members/auth/session"); n.Stats == nil || n.Stats.Count == 0 {
		t.Errorf("the auth node has no stats: %+v", n.Stats)
	}
}

func TestAuthInTheGraph(t *testing.T) {
	app := start(t)
	g := app.Graph()
	me := g.Node("members/endpoint/Me").Endpoint
	if me.Auth != model.AuthRequired || len(me.Pipeline) == 0 || me.Pipeline[0].Kind != "auth" || me.Pipeline[0].Label != "session" {
		t.Errorf("/me: auth %q pipeline %+v", me.Auth, me.Pipeline)
	}
	if greet := g.Node("members/endpoint/Greet").Endpoint; greet.Auth != model.AuthOptional || greet.Pipeline[0].Config["mode"] != model.AuthOptional {
		t.Errorf("/greeting: %+v", greet)
	}
	if login := g.Node("members/endpoint/Login").Endpoint; login.Auth != "" {
		t.Errorf("/login asks for auth: %q", login.Auth)
	}
	auth := g.Node("members/auth/session")
	if auth == nil || auth.Auth == nil || auth.Auth.Endpoints != 5 {
		t.Fatalf("auth node %+v", auth)
	}
	var ins []string
	for _, f := range auth.Auth.Credentials.Fields {
		ins = append(ins, f.In+":"+f.Name)
	}
	if !slices.Equal(ins, []string{"cookie:sid", "header:Authorization"}) {
		t.Errorf("credentials %v", ins)
	}
	if auth.Handler == nil || !strings.HasSuffix(auth.Handler.Func, ".Authenticate") {
		t.Errorf("handler %+v", auth.Handler)
	}
	var kinds []string
	for _, m := range kit.Catalog() {
		kinds = append(kinds, m.Kind)
	}
	if !slices.Contains(kinds, "auth") {
		t.Errorf("the catalog lacks auth: %v", kinds)
	}
}

func TestCookiesAreHardened(t *testing.T) {
	for _, c := range []struct {
		env    string
		secure bool
	}{{kit.EnvDev, false}, {kit.EnvProduction, true}} {
		t.Run(c.env, func(t *testing.T) {
			app := start(t, kit.Env(c.env))
			if err := Accounts.Put(t.Context(), Account{ID: "acc_h", Email: "h@x.dev"}); err != nil {
				t.Fatal(err)
			}
			r := call(t, app, "POST /login", LoginInput{Email: "h@x.dev"})
			cs := setCookies(t, r)
			if len(cs) != 1 {
				t.Fatalf("cookies %v", r.header.Values("Set-Cookie"))
			}
			sid := cs[0]
			if !sid.HttpOnly || sid.SameSite != http.SameSiteLaxMode || sid.Path != "/" || sid.Secure != c.secure {
				t.Errorf("%s: %q", c.env, r.header.Get("Set-Cookie"))
			}
		})
	}
}

func TestClientAddress(t *testing.T) {
	for _, c := range []struct {
		trust, forwarded, want string
	}{
		{"", "203.0.113.9", "127.0.0.1"},
		{"on", "203.0.113.9", "203.0.113.9"},
		// The client forged a first hop; the trusted proxy appended the
		// address it really came from, and that is the one kept.
		{"on", "6.6.6.6, 203.0.113.9", "203.0.113.9"},
		{"on", "not-an-ip", "127.0.0.1"},
		{"on", "", "127.0.0.1"},
		{"on", "::ffff:198.51.100.4", "198.51.100.4"},
	} {
		t.Run(c.trust+"/"+c.forwarded, func(t *testing.T) {
			t.Setenv("KIT_TRUST_PROXY", c.trust)
			app := start(t)
			token := signIn(t, app, "acc_ip", "ip@x.dev", "Ip")
			headers := []string{"Cookie", "sid=" + token, "User-Agent", strings.Repeat("é", 300)}
			if c.forwarded != "" {
				headers = append(headers, "X-Forwarded-For", c.forwarded)
			}
			var me Profile
			call(t, app, "GET /me", noBody, headers...).json(t, &me)
			if me.IP != c.want {
				t.Errorf("ClientIP = %q, want %q", me.IP, c.want)
			}
			if n := len([]rune(me.Agent)); n != 256 {
				t.Errorf("the user agent kept %d runes", n)
			}
		})
	}
}

// The helpers have nothing to say outside an HTTP request.
func TestRequestHelpersOutsideARequest(t *testing.T) {
	ctx := context.Background()
	kit.SetCookie(ctx, &http.Cookie{Name: "x", Value: "y"})
	kit.ClearCookie(ctx, "x")
	if kit.ClientIP(ctx) != "" || kit.UserAgent(ctx) != "" {
		t.Error("a request helper answered outside a request")
	}
	if kit.Now(ctx).IsZero() {
		t.Error("Now outside an app")
	}
}

func TestAuthDeclarationProblems(t *testing.T) {
	t.Run("an endpoint asks for a user no handler provides", func(t *testing.T) {
		svc := kit.NewService("no-handler", "")
		svc.Endpoint("GET /mine", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil }, kit.Auth())
		err := kit.NewApp("x", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
		if err == nil || !strings.Contains(err.Error(), "the app mounts no auth handler") {
			t.Fatalf("Start = %v", err)
		}
	})
	t.Run("two handlers", func(t *testing.T) {
		svc := kit.NewService("second-handler", "")
		svc.AuthHandler("other", func(context.Context, Credentials) (kit.UID, Who, error) { return "", Who{}, nil })
		err := kit.NewApp("x", Members, svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
		if err == nil || !strings.Contains(err.Error(), "two auth handlers, members/auth/session") || !strings.Contains(err.Error(), "second-handler/auth/other") {
			t.Fatalf("Start = %v", err)
		}
	})
	t.Run("credentials read elsewhere than cookies and headers", func(t *testing.T) {
		type Loose struct {
			Token string `query:"token"`
			Body  string `json:"body"`
		}
		svc := kit.NewService("loose-credentials", "")
		svc.AuthHandler("loose", func(context.Context, Loose) (kit.UID, Who, error) { return "", Who{}, nil })
		svc.AuthHandler("scalar", func(context.Context, string) (kit.UID, Who, error) { return "", Who{}, nil })
		err := kit.NewApp("x", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
		for _, want := range []string{"field Token of the credentials is read from the query", "field Body of the credentials is not tagged", "the credentials type string must be a struct"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Start lacks %q: %v", want, err)
			}
		}
	})
}
