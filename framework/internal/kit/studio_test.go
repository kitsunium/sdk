package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

func TestStudioIsOffInProduction(t *testing.T) {
	app := start(t, kit.Env(kit.EnvProduction))
	for _, path := range []string{"/_kit/", "/_kit/api/graph", "/_kit/api/events", "/_kit/api/source?file=internal/kit/product_test.go&line=1"} {
		if r := call(t, app, "GET"+" "+path, noBody); r.status != http.StatusNotFound {
			t.Errorf("production serves %s: %d", path, r.status)
		}
	}
	for _, probe := range []string{"live", "ready", "startup"} {
		if r := call(t, app, "GET /_kit/health/"+probe, noBody); r.status != http.StatusOK {
			t.Errorf("probe %s: %d %s", probe, r.status, r.body)
		}
	}
	if g := app.Graph(); g.App.Root != "" {
		t.Errorf("a production graph discloses the module root %q", g.App.Root)
	}
}

func TestStudioChecksTheHost(t *testing.T) {
	app := start(t)
	for _, host := range []string{"evil.example", "localhost.evil.example", "127.0.0.1.nip.io", "[::2]"} {
		req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+"/_kit/api/graph", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Host = host
		resp, respErr := http.DefaultClient.Do(req)
		if respErr != nil {
			t.Fatal(respErr)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q reached the Studio: %d", host, resp.StatusCode)
		}
	}
	req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+"/_kit/health/ready", nil)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	req.Host = "10.0.0.7:4000"
	resp, respErr := http.DefaultClient.Do(req)
	if respErr != nil {
		t.Fatal(respErr)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a probe by IP must pass the host check: %d", resp.StatusCode)
	}
	// The Studio's pages are the kit tool's (ADR 0147 §1): the product serves
	// its API only, and still under the Studio's policy.
	r := call(t, app, "GET /_kit/", noBody)
	if r.status != http.StatusNotFound || !strings.Contains(r.header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("the Studio page: %d, CSP %q", r.status, r.header.Get("Content-Security-Policy"))
	}
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("the Studio must never allow another origin")
	}
}

func TestSourceServesOnlyTheGraphsFiles(t *testing.T) {
	app := start(t)
	r := call(t, app, "GET /_kit/api/source?file=internal/kit/product_test.go&line=30&end=32", noBody)
	var sn model.Snippet
	r.json(t, &sn)
	if r.status != http.StatusOK || sn.Focus != 30 || sn.FocusEnd != 32 || sn.Language != "go" || sn.StartLine != 18 {
		t.Fatalf("snippet %d %+v", r.status, sn)
	}
	if !strings.Contains(strings.Join(sn.Lines, "\n"), "kit.NewService") {
		t.Errorf("snippet content: %v", sn.Lines)
	}
	for _, file := range []string{
		"../../../../etc/passwd", "/etc/passwd", "go.mod", ".git/config", ".kit/data/shop/items.json",
		"internal/kit/app.go", "internal/kit/product_test.go\x00", "internal/kit/../kit/product_test.go", `internal\kit\product_test.go`,
	} {
		r := call(t, app, "GET /_kit/api/source?file="+urlEscape(file)+"&line=1", noBody)
		if r.status != http.StatusNotFound {
			t.Errorf("file %q answered %d", file, r.status)
		}
	}
	r = call(t, app, "GET /_kit/api/source?file=internal/kit/product_test.go&line=-4&end=999999999", noBody)
	r.json(t, &sn)
	if r.status != http.StatusOK || sn.Focus != 1 || len(sn.Lines) > 400+2*12 {
		t.Errorf("the range is not clamped: focus %d, %d lines", sn.Focus, len(sn.Lines))
	}
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "\x00", "%00", "\\", "%5C", " ", "%20").Replace(s)
}

func TestStudioBrowsesInstancesAndItems(t *testing.T) {
	app := start(t)
	it := create(t, app, "browse", 1)
	r := call(t, app, "GET /_kit/api/instances?workflow=shop/workflow/lifecycle", noBody)
	var inst []model.Instance
	r.json(t, &inst)
	if len(inst) != 1 || inst[0].ID != it.ID || inst[0].State != "draft" || len(inst[0].History) != 1 {
		t.Fatalf("instances %s", r.body)
	}
	if inst[0].History[0].Caller != "shop/endpoint/CreateItem" {
		t.Errorf("the history does not name who fired: %+v", inst[0].History[0])
	}
	r = call(t, app, "GET /_kit/api/items?store=shop/store/items", noBody)
	var items []Item
	r.json(t, &items)
	if len(items) != 1 || items[0].Name != "browse" {
		t.Fatalf("items %s", r.body)
	}
	for _, path := range []string{"/_kit/api/items?store=nope", "/_kit/api/instances?workflow=shop/store/items", "/_kit/api/nothing"} {
		if r := call(t, app, "GET"+" "+path, noBody); r.status != http.StatusNotFound {
			t.Errorf("%s: %d", path, r.status)
		}
	}
}

// Declaring never fails; starting reports every problem at once, each with
// the line that caused it.
func TestDeclarationProblemsAreReportedTogether(t *testing.T) {
	broken := kit.NewService("Broken_Name", "")
	broken.Endpoint("get /x", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	broken.Endpoint("GET /y/{id}", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	broken.Endpoint("GET /_kit/steal", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	broken.Endpoint("OPTIONS /preflight", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	broken.Store("s", (func(Item) string)(nil))
	broken.Store("s", func(i Item) string { return i.ID })
	broken.Workflow("w", Items, func(i *Item) *State { return &i.State }).On("go", Draft, Live)
	broken.Every("tick", 0, func(context.Context) error { return nil })
	broken.Cron("bad", "every tuesday", func(context.Context) error { return nil })
	app := kit.NewApp("broken", broken).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	err := app.Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want a DiagnosticsError", err)
	}
	text := err.Error()
	for _, want := range []string{
		"service name", "must start with an HTTP method", `route "OPTIONS /preflight"`, "wildcard {id} is not bound", "/_kit/",
		"nil key function", "already declares a store", "no initial state", "period must be positive", "cron expression",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	for _, d := range de.Diagnostics {
		if d.Source == nil || d.Source.File != "internal/kit/studio_test.go" {
			t.Errorf("diagnostic without its position: %+v", d)
		}
	}
	if app.Graph().Runtime != nil {
		t.Error("a refused app reports a runtime")
	}
}

func TestRouteConflictsAreDiagnostics(t *testing.T) {
	a := kit.NewService("conflict-a", "")
	b := kit.NewService("conflict-b", "")
	a.Endpoint("GET /same", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	b.Endpoint("GET /same", func(context.Context, kit.EmptyValue) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	err := kit.NewApp("conflict", a, b).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "conflicts") || !strings.Contains(err.Error(), "conflict-a") {
		t.Fatalf("Start = %v", err)
	}
}

func TestAServiceRunsInOneAppAtATime(t *testing.T) {
	start(t)
	err := kit.NewApp("second", Shop).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second app mounted a running service: %v", err)
	}
}

func TestGraphCommand(t *testing.T) {
	app := kit.NewApp("shop", Shop, Audit).With(kit.InMemory(), kit.Logs(io.Discard))
	out := captureStdout(t, func() {
		if code := app.Main(t.Context(), []string{"graph", "-static=false"}); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	var g model.Graph
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		t.Fatalf("graph output is not JSON: %v\n%s", err, out)
	}
	if g.Node("shop/workflow/lifecycle") == nil || g.Runtime != nil {
		t.Errorf("graph without running: %d nodes, runtime %v", len(g.Nodes), g.Runtime)
	}
	mermaid := captureStdout(t, func() {
		app.Main(t.Context(), []string{"graph", "-static=false", "-format", "mermaid"})
	})
	if !strings.HasPrefix(mermaid, "flowchart LR") {
		t.Errorf("mermaid output: %q", mermaid)
	}
	if code := app.Main(t.Context(), []string{"frobnicate"}); code != 2 {
		t.Errorf("an unknown command exits %d", code)
	}
}

// Goroutine lifecycle: one goroutine reads the pipe until fn ends and the
// writer closes, and hands back what it read.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		raw, err := io.ReadAll(r)
		if err != nil {
			raw = append(raw, "\n(reading the pipe: "+err.Error()+")"...)
		}
		done <- string(raw)
	}()
	fn()
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// devRoutes are the dev tools' routes, with a body that would pass. They
// read: none acts on the product (ADR 0010, D13).
var devRoutes = []struct{ method, path, body string }{
	{"GET", "/_kit/api/process", ""},
	{"GET", "/_kit/api/databases", ""},
	{"GET", "/_kit/api/goroutines", ""},
	{"GET", "/_kit/api/profile/heap", ""},
	{"GET", "/_kit/api/logs", ""},
	{"GET", "/_kit/api/mail", ""},
	{"GET", "/_kit/api/mail/m1", ""},
	{"GET", "/_kit/api/former?store=shop/store/items&key=x", ""},
	{"GET", "/_kit/api/revisions?store=shop/store/items&key=x", ""},
	{"GET", "/_kit/api/privacy", ""},
}

// actionRoutes are the Studio's former controls (D13, ADR 0147 §8): none of
// them exists, in dev either.
var actionRoutes = []struct{ method, path, body string }{
	{"POST", "/_kit/api/profile/cpu", `{"seconds":1}`},
	{"GET", "/_kit/api/mocks", ""},
	{"POST", "/_kit/api/mocks", `{"node":"shop/endpoint/Search","mode":"fail"}`},
	{"DELETE", "/_kit/api/mocks", ""},
	{"POST", "/_kit/api/jobs/run", `{"job":"audit/job/tally"}`},
	{"POST", "/_kit/api/workflows/fire", `{"workflow":"shop/workflow/lifecycle","instance":"x","event":"publish"}`},
	{"POST", "/_kit/api/loops/wake", `{"loop":"bench/loop/tick"}`},
	{"POST", "/_kit/api/revisions/restore", `{"store":"shop/store/items","key":"x","number":1}`},
	{"POST", "/_kit/api/commands/dispatch", `{"command":"bench/command/none","input":{}}`},
	{"POST", "/_kit/api/queries/ask", `{"query":"bench/query/none"}`},
	{"POST", "/_kit/api/privacy/retention", `{}`},
	{"POST", "/_kit/api/privacy/export", `{"ids":["ann@x.dev"]}`},
	{"POST", "/_kit/api/privacy/erase", `{"ids":["ann@x.dev"],"reason":"asked"}`},
}

// The Studio does not act on the product (D13): its former controls answer
// 404 in dev, from this machine, through an allowed Host — the one place
// they used to exist.
func TestTheStudioActsOnNothing(t *testing.T) {
	app := startBench(t)
	for _, rt := range actionRoutes {
		var body any
		if rt.body != "" {
			body = rt.body
		}
		if r := call(t, app, rt.method+" "+rt.path, body); r.status != http.StatusNotFound {
			t.Errorf("dev answers %s %s: %d %s", rt.method, rt.path, r.status, r.body)
		}
	}
	if m := app.Graph().Runtime.Mocks; len(m) != 0 {
		t.Errorf("the product holds mocks it was never given: %+v", m)
	}
}

// The dev tools exist in dev only: in production every one of their routes
// answers 404, whatever the method — not even a 405 says they exist.
func TestDevToolsAreAbsentInProduction(t *testing.T) {
	app := startBench(t, kit.Env(kit.EnvProduction))
	for _, rt := range devRoutes {
		var body any
		if rt.body != "" {
			body = rt.body
		}
		if r := call(t, app, rt.method+" "+rt.path, body); r.status != http.StatusNotFound {
			t.Errorf("production answers %s %s: %d %s", rt.method, rt.path, r.status, r.body)
		}
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE", "POST"} {
		if r := call(t, app, method+" /_kit/api/anything", noBody); r.status != http.StatusNotFound {
			t.Errorf("production answers %s under /_kit/: %d", method, r.status)
		}
	}
}

// The dev tools answer this machine only, through an allowed Host, and a
// browser's cross-origin write never reaches them.
func TestDevToolsRefuseStrangers(t *testing.T) {
	app := startBench(t)
	for _, rt := range devRoutes {
		req := httptest.NewRequest(rt.method, "http://localhost"+rt.path, strings.NewReader(rt.body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.1.9:5555"
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s from another machine: %d %s", rt.method, rt.path, rec.Code, rec.Body)
		}
		req = httptest.NewRequest(rt.method, "http://evil.example"+rt.path, strings.NewReader(rt.body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:5555"
		rec = httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s through a foreign Host: %d %s", rt.method, rt.path, rec.Code, rec.Body)
		}
		if rt.method == "GET" {
			continue
		}
		r := call(t, app, rt.method+" "+rt.path, rt.body, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
		if r.status != http.StatusForbidden {
			t.Errorf("a cross-site %s %s: %d %s", rt.method, rt.path, r.status, r.body)
		}
	}
}

// The Studio's data browser never shows a secret a store holds: entities go
// through the same redaction as payloads.
func TestStoreItemsAreRedacted(t *testing.T) {
	app := start(t)
	token := signIn(t, app, "acc_red", "red@x.dev", "Red")
	r := call(t, app, "GET /_kit/api/items?store=members/store/sessions", noBody)
	if r.status != http.StatusOK {
		t.Fatalf("items: %d %s", r.status, r.body)
	}
	if strings.Contains(string(r.body), token) || !strings.Contains(string(r.body), "[redacted]") {
		t.Fatalf("a session token reached the Studio: %s", r.body)
	}
}
