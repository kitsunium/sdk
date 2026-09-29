package kit_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// A frontend is loaded from the outside: the edge is declared, so the
// diagram draws the way in before any browser has come.
func TestAFrontendIsLoadedFromTheOutside(t *testing.T) {
	site := kit.NewService("site", "A site.")
	// Through a method value: go1.27's vet, in kit's test build only, lends
	// (*Service).problem's printf fact to Static and reads its file system
	// as a format string. A product calling Static is not affected.
	static := site.Static
	static("pages", "/", fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>x</title>")}})
	app := kit.NewApp("site", site).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	g := app.Graph()
	for _, e := range g.Edges {
		if e.From == model.ExternalID && e.To == "site/frontend/pages" {
			if e.Kind != model.EdgeCalls || !e.Declared {
				t.Fatalf("the way in: %+v", e)
			}
			return
		}
	}
	t.Fatal("no edge from the outside to the frontend")
}

// A frontend is a single-page application: a route with no extension is its
// page, a file it does not have — a script, a stylesheet — is a 404, never a
// page of HTML; no directory is listed; every answer carries its policy.
func TestAFrontendServesItsPages(t *testing.T) {
	site := kit.NewService("pages", "A single-page application.")
	static := site.Static
	static("app", "/", fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app.js":   {Data: []byte("console.log(1)")},
		"docs/readme.txt": {Data: []byte("notes")},
	})
	app := kit.NewApp("pages", site).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"/", http.StatusOK, "<title>app</title>"},
		{"/tasks/42", http.StatusOK, "<title>app</title>"},
		{"/assets/app.js", http.StatusOK, "console.log(1)"},
		{"/assets/missing.js", http.StatusNotFound, ""},
		{"/docs/", http.StatusNotFound, ""},
	} {
		r := call(t, app, "GET"+" "+c.path, noBody)
		if r.status != c.status || !strings.Contains(string(r.body), c.body) {
			t.Errorf("GET %s: %d %q, want %d with %q", c.path, r.status, r.body, c.status, c.body)
		}
		if c.status == http.StatusNotFound && strings.Contains(string(r.body), "<title>app</title>") {
			t.Errorf("GET %s answered the page", c.path)
		}
		if strings.Contains(string(r.body), "readme.txt") {
			t.Errorf("GET %s listed a directory: %s", c.path, r.body)
		}
		h := r.header
		if !strings.HasPrefix(h.Get("Content-Security-Policy"), "default-src 'self'") || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("GET %s: headers %v", c.path, h)
		}
	}
	if ct := call(t, app, "GET /assets/app.js", noBody).header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("a script is served as %q", ct)
	}
}
